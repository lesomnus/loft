#!/usr/bin/env bash
# The reference deployment (`deploy/k8s`) in a real cluster: k3d, the image
# this checkout builds, and the overlay in `scripts/cluster/`.
#
#     ./scripts/cluster.sh            # up, check, down
#     ./scripts/cluster.sh --hold     # leave it up to look at
#
# What it says, each a thing only a cluster can:
#
#   - the manifests are admitted under `restricted` Pod Security, and the pod
#     comes up: the migration in its init container with no volume mounted,
#     then the server and the operator's entry point beside it;
#   - a person signs in through the ingress, and the answer carries the
#     headers and the policy (`app.csp`);
#   - the operator's entry point answers a key over `kubectl port-forward`,
#     and nothing in the cluster reaches it, or the server, over the network
#     but the ingress controller (the NetworkPolicy);
#   - a new build replaces the pod and migrates first, and `rollout undo` puts
#     the one before back -- with the session signed in before both still
#     signed in, because nothing a person holds was in the pod.
#
# Not in `go test`: it needs an engine and a few minutes. CI runs it (the
# `cluster` job).
#
# # Your kubeconfig is left alone
#
# k3d writes every cluster it makes into the default kubeconfig and switches to
# it, unless it is told not to -- and the default kubeconfig is where a
# person's real clusters are. So it is told not to, and nothing here reads that
# file either.
#
# # Why everything runs in a container
#
# As roster's rig does (lesomnus/roster `scripts/cluster.sh`): the engine may
# be somewhere else, so neither the cluster's published ports nor a path in
# this checkout mean anything to it. Everything that talks to the cluster is a
# container on the cluster's own network, the kubeconfig is rewritten to the
# in-network address, and what the containers read is carried in a volume.
set -o errexit
set -o nounset
set -o pipefail

__dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
__root="$(cd "${__dir}/.." && pwd)"
cd "${__root}"

: "${CLUSTER:=rove-rig}"
: "${NS:=rove}"
: "${KUBECTL_IMAGE:=alpine/k8s:1.31.0}"
: "${CURL_IMAGE:=curlimages/curl:8.11.1}"
: "${OPENSSL_IMAGE:=alpine/openssl:3.3.2}"
: "${ALPINE_IMAGE:=alpine:3.22}"

hold=""
for v in "$@"; do
	case "${v}" in --hold) hold=1 ;; esac
done

for bin in k3d docker; do
	command -v "${bin}" >/dev/null || { echo "cluster: ${bin} is not here" >&2; exit 1; }
done

work="$(mktemp -d)"
vol="rove-cluster-$$"
docker volume create "${vol}" >/dev/null
carry() { tar -C "${work}" -cf - . | docker run --rm -i -v "${vol}:/w" "${ALPINE_IMAGE}" sh -c 'tar -C /w -xf -'; }

kube() {
	docker run --rm --network "k3d-${CLUSTER}" \
		-v "${vol}:/w" -e KUBECONFIG=/w/kubeconfig \
		"${KUBECTL_IMAGE}" sh -c "$*"
}

# What did not come up says why in the pods, their init containers and the
# namespace's events, and only on the way out red.
diagnose() {
	{
		echo
		echo "-- ${NS}, in case what did not come up says why"
		kube "kubectl -n ${NS} get pods -o wide" 2>/dev/null || true
		kube "kubectl -n ${NS} logs deploy/rove --all-containers --tail=40 --prefix" 2>/dev/null || true
		kube "kubectl -n ${NS} logs statefulset/postgres --tail=20" 2>/dev/null || true
		kube "kubectl -n ${NS} get events --sort-by=.lastTimestamp" 2>/dev/null | tail -30 || true
	} >&2
}

cleanup() {
	local out=$?
	if [ "${out}" -ne 0 ] && docker inspect "k3d-${CLUSTER}-server-0" >/dev/null 2>&1; then
		diagnose
	fi

	rm -rf "${work}"
	docker volume rm -f "${vol}" >/dev/null 2>&1 || true
	if [ -z "${hold}" ]; then
		k3d cluster delete "${CLUSTER}" >/dev/null 2>&1 || true
		# The names it gave what it built, and not the layers, which the
		# next run builds on.
		docker image rm "rove-cluster:rig" "rove-cluster:next" "rove-postgres:rig" >/dev/null 2>&1 || true
	else
		echo
		echo "cluster: left up, with its kubeconfig written nowhere. To look:"
		echo "  k3d kubeconfig get ${CLUSTER} > ./rig.kubeconfig && KUBECONFIG=./rig.kubeconfig kubectl -n ${NS} get pods"
		echo "and to take it down: k3d cluster delete ${CLUSTER}"
	fi
}
trap cleanup EXIT

# said asserts that a read says something, and shows what it said when it did
# not -- one read, judged and shown, rather than `| grep -q`, whose early exit
# makes a match that raced the writer read as a miss (roster's rig found it).
said() {
	local what="$1" why="$2"
	shift 2

	local got
	if ! got="$("$@" 2>&1)"; then
		echo "cluster: ${why}" >&2
		echo "   (and the read itself failed)" >&2
		printf '%s\n' "${got}" >&2
		return 1
	fi

	case "${got}" in
	*"${what}"*) return 0 ;;
	esac

	echo "cluster: ${why}" >&2
	printf '%s\n' "${got}" >&2
	return 1
}

# unsaid is the other way round: the read must not say it.
unsaid() {
	local what="$1" why="$2"
	shift 2

	local got
	got="$("$@" 2>&1)" || true
	case "${got}" in
	*"${what}"*)
		echo "cluster: ${why}" >&2
		printf '%s\n' "${got}" >&2
		return 1
		;;
	esac
}

# tried is a build that survives a registry's bad minute: twice, ten seconds
# apart, and saying so.
tried() {
	local i
	for i in 1 2; do
		if "$@"; then
			return 0
		fi
		echo "   (the registry refused; trying once more)" >&2
		sleep 10
	done

	"$@"
}

# standing is the node ready to be copied into, which `--wait` does not
# promise: containerd listening, which is what `k3d image import` needs and
# reports the lack of with an exit code of zero (roster#39).
standing() {
	local until_ state
	until_=$(($(date +%s) + 180))
	while [ "$(date +%s)" -lt "${until_}" ]; do
		state="$(docker inspect -f '{{.State.Status}}' "k3d-${CLUSTER}-server-0" 2>/dev/null || true)"
		if [ "${state}" = "running" ] &&
			docker exec "k3d-${CLUSTER}-server-0" ctr version >/dev/null 2>&1; then
			return 0
		fi

		sleep 2
	done

	echo "cluster: the node never got ready to be copied into; it is ${state:-gone}" >&2
	return 1
}

# web is a request through the ingress, as a browser makes one: to the host
# the certificate and the routes are for, at the node, which is where k3s
# answers for Traefik. `-k` because the certificate is Traefik's own.
web() {
	docker run --rm --user 0 --network "k3d-${CLUSTER}" -v "${vol}:/w" "${CURL_IMAGE}" \
		-sk --max-time 20 --resolve "rove.localhost:443:${node}" "$@"
}

# reachable says whether ingress answers at all, and waits for it to: a route
# is Traefik's to pick up, a moment after the Ingress is applied.
reachable() {
	local i
	for i in $(seq 1 60); do
		if [ "$(web -o /dev/null -w '%{http_code}' https://rove.localhost/healthz || true)" = "200" ]; then
			return 0
		fi
		sleep 2
	done

	echo "cluster: nothing answered through the ingress" >&2
	return 1
}

echo "== from nothing"
k3d cluster delete "${CLUSTER}" >/dev/null 2>&1 || true
# Traefik is left in: it is the ingress the overlay routes through. k3d's own
# load balancer is not, since nothing here comes from outside the network.
#
# And the kubelet is told a disk is full only when it is: by default it stops
# scheduling with a tenth of the disk left, which on a desk that keeps its
# images and volumes is a cluster that runs nothing and says nothing about the
# manifests -- the pod sits `Pending` on a taint (k3d's FAQ, "pods evicted").
k3d cluster create "${CLUSTER}" --servers 1 --agents 0 --no-lb \
	--kubeconfig-update-default=false --kubeconfig-switch-context=false \
	--k3s-arg "--disable=metrics-server@server:0" \
	--k3s-arg "--kubelet-arg=eviction-hard=imagefs.available<1%,nodefs.available<1%@server:0" \
	--k3s-arg "--kubelet-arg=eviction-minimum-reclaim=imagefs.available=1%,nodefs.available=1%@server:0" \
	--wait >/dev/null

k3d kubeconfig get "${CLUSTER}" |
	sed "s|server: https://.*|server: https://k3d-${CLUSTER}-server-0:6443|" \
		>"${work}/kubeconfig"
node="$(docker inspect -f "{{(index .NetworkSettings.Networks \"k3d-${CLUSTER}\").IPAddress}}" "k3d-${CLUSTER}-server-0")"

echo "== the images this checkout builds"
tried docker build -q -t "rove-cluster:rig" . >/dev/null
# PostgreSQL as it is published, made an image of this host's own so that it
# goes onto the node with the rest and nothing in the cluster waits on a
# registry: a pulled image saves with the index of every platform it was
# published for and the layers of one, which the node refuses to import.
mkdir -p "${work}/postgres"
printf 'FROM postgres:18\n' >"${work}/postgres/Dockerfile"
tried docker build -q -t "rove-postgres:rig" "${work}/postgres" >/dev/null
# The next build, which is this one under another name: a rollout is a new
# image, whatever is in it, and what is under test is the rollout.
docker tag "rove-cluster:rig" "rove-cluster:next"
standing
imported="$(k3d image import "rove-cluster:rig" "rove-cluster:next" "rove-postgres:rig" -c "${CLUSTER}" 2>&1)" || true
case "${imported}" in
*ERRO*)
	printf '%s\n' "${imported}" >&2
	echo "cluster: the images did not reach the node" >&2
	exit 1
	;;
esac

echo "== what a deployment brings: its secrets"
mkdir -p "${work}/scripts" "${work}/secrets"
cp -r deploy "${work}/deploy"
cp -r scripts/cluster "${work}/scripts/cluster"
docker run --rm --entrypoint sh -v "${vol}:/w" "${OPENSSL_IMAGE}" -c '
	set -e
	mkdir -p /w/secrets && cd /w/secrets
	openssl genpkey -algorithm ed25519 -out checkpoints.pem
	openssl rand -hex 32 > operator
	pw="$(openssl rand -hex 24)"
	printf "password=%s\n" "${pw}" > postgres.env
	{
		printf "db-dsn=postgres://rove:%s@postgres:5432/rove?sslmode=disable\n" "${pw}"
		printf "roster-db-dsn=postgres://rove:%s@postgres:5432/roster?sslmode=disable\n" "${pw}"
		printf "signing-key=%s\n" "$(openssl rand -base64 32)"
	} > rove.env
' >/dev/null
carry
kube "kubectl create namespace ${NS} >/dev/null &&
	kubectl label namespace ${NS} pod-security.kubernetes.io/enforce=restricted pod-security.kubernetes.io/warn=restricted >/dev/null &&
	kubectl -n ${NS} create secret generic postgres --from-env-file=/w/secrets/postgres.env >/dev/null &&
	kubectl -n ${NS} create secret generic rove --from-env-file=/w/secrets/rove.env >/dev/null &&
	kubectl -n ${NS} create secret generic rove-checkpoints --from-file=checkpoints.pem=/w/secrets/checkpoints.pem >/dev/null &&
	kubectl -n ${NS} create secret generic rove-admin --from-file=operator=/w/secrets/operator >/dev/null"
key="$(docker run --rm -v "${vol}:/w" "${ALPINE_IMAGE}" cat /w/secrets/operator)"

echo "== rolled out"
# The database first and alone, since a deployment's is there before it: the
# migration would otherwise spend its first tries on one still starting.
kube "kubectl apply -k /w/scripts/cluster -l app.kubernetes.io/name=postgres >/dev/null &&
	kubectl -n ${NS} rollout status statefulset/postgres --timeout=300s >/dev/null"
kube "kubectl apply -k /w/scripts/cluster >/dev/null &&
	kubectl -n ${NS} rollout status deploy/rove --timeout=300s >/dev/null"
said "this build's shape" "the init container did not migrate" \
	kube "kubectl -n ${NS} logs deploy/rove -c migrate"

echo "== through the ingress"
reachable
said "ok" "healthz did not answer" web https://rove.localhost/healthz
headers="$(web -o /dev/null -D - https://rove.localhost/ | tr 'A-Z' 'a-z')"
for h in "strict-transport-security" "content-security-policy-report-only" "x-frame-options: deny" "x-content-type-options: nosniff"; do
	said "${h}" "the page is served without ${h}" printf '%s' "${headers}"
done

echo "== a workspace, signed in to"
kube "kubectl -n ${NS} exec deploy/rove -c rove -- rove --config /etc/rove/rove.yaml init \
	--tenant acme --name ACME --login owner --person Owner --email owner@example.com \
	--password owner-pass-1234 >/dev/null"
web -o /dev/null -c /w/jar -H 'content-type: application/json' \
	-d '{"login":"owner","password":"owner-pass-1234"}' https://rove.localhost/session
me() {
	web -o /dev/null -w '%{http_code}' -b /w/jar -H 'content-type: application/json' \
		-H 'connect-protocol-version: 1' -d '{}' https://rove.localhost/rove.PartyService/Me
}
said "200" "a signed-in session was not answered as anybody" me

echo "== the operator's entry point, over port-forward and over nothing else"
admin() {
	kube "kubectl -n ${NS} port-forward deploy/rove 8081:8081 >/dev/null 2>&1 & pf=\$!
		for i in \$(seq 1 30); do curl -s -o /dev/null http://127.0.0.1:8081/ && break; sleep 1; done
		curl -s -w ' %{http_code}' $* http://127.0.0.1:8081/admin/workspaces; kill \${pf}"
}
said '"alias":"acme"' "the entry point did not list the workspace to a key" admin "-H 'authorization: Bearer ${key}'"
said " 401" "the entry point answered without a key" admin

# From a pod that is not the ingress controller, at the pod itself: neither
# the server nor the entry point is to answer. The same pod through the
# ingress is what says the probe had a network to be refused on.
ip="$(kube "kubectl -n ${NS} get pod -l app.kubernetes.io/component=server -o jsonpath='{.items[0].status.podIP}'")"
probe() {
	kube "kubectl create namespace rig >/dev/null 2>&1; kubectl -n rig delete pod probe --ignore-not-found >/dev/null 2>&1
		kubectl -n rig run probe --image=${CURL_IMAGE} --restart=Never --command -- sh -c \
			'curl -sk -m 10 -H \"Host: rove.localhost\" https://traefik.kube-system.svc.cluster.local/healthz && echo through-the-ingress; curl -s -m 5 http://${ip}:8080/healthz && echo reached-the-server; curl -s -m 5 http://${ip}:8081/ && echo reached-the-entry-point; echo probed' >/dev/null
		kubectl -n rig wait --for=jsonpath='{.status.phase}'=Succeeded pod/probe --timeout=120s >/dev/null
		kubectl -n rig logs probe"
}
out="$(probe)"
said "probed" "the probe did not run" printf '%s' "${out}"
said "through-the-ingress" "the probe could not reach the server through the ingress either" printf '%s' "${out}"
unsaid "reached-the-server" "a pod that is not the ingress controller reached the server" printf '%s' "${out}"
unsaid "reached-the-entry-point" "a pod reached the operator's entry point over the network" printf '%s' "${out}"

echo "== a new build replaces the pod, and the one before comes back"
kube "kubectl -n ${NS} set image deploy/rove migrate=rove-cluster:next rove=rove-cluster:next rove-admin=rove-cluster:next >/dev/null &&
	kubectl -n ${NS} rollout status deploy/rove --timeout=300s >/dev/null"
said "rove-cluster:next" "the pod was not replaced" \
	kube "kubectl -n ${NS} get pods -l app.kubernetes.io/component=server -o jsonpath='{.items[*].spec.containers[0].image}'"
said "this build's shape" "the new pod did not migrate before it served" \
	kube "kubectl -n ${NS} logs deploy/rove -c migrate"
reachable
said "200" "the session did not outlive the pod it began on" me

kube "kubectl -n ${NS} rollout undo deploy/rove >/dev/null &&
	kubectl -n ${NS} rollout status deploy/rove --timeout=300s >/dev/null"
said "rove-cluster:rig" "rollout undo did not put the build before back" \
	kube "kubectl -n ${NS} get pods -l app.kubernetes.io/component=server -o jsonpath='{.items[*].spec.containers[0].image}'"
reachable
said "200" "the session did not outlive the rollback" me

echo "== cluster: the reference deployment holds"
