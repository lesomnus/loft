#!/usr/bin/env bash
# search-init.sh -- what compose.search.yaml needs and nothing else has, made
# once into secrets/ beside the bundle's other keys (README.md):
#
#   search-ca.pem              the CA Rove trusts the cluster by
#   search.pem, search-key.pem the cluster's certificate, for `opensearch`
#   search_password            what Rove signs in with
#   search-users.yml           the cluster's one user, Rove, by that password
#
# The CA's own key signs the certificate and is thrown away: making them all
# again is deleting secrets/search* and running this again, and then the
# cluster's volume, which is a copy (`docker volume rm rove_opensearch`).
set -euo pipefail
cd "$(dirname "$0")"

if ls secrets/search* > /dev/null 2>&1; then
	echo "secrets/ has search files already; delete secrets/search* to make them again" >&2
	exit 1
fi
mkdir -p -m 700 secrets
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

openssl req -x509 -newkey rsa:3072 -nodes -days 3650 -subj "/CN=rove search CA" \
	-keyout "$work/ca.key" -out secrets/search-ca.pem 2> /dev/null
openssl req -newkey rsa:3072 -nodes -subj "/CN=opensearch" \
	-keyout secrets/search-key.pem -out "$work/node.csr" 2> /dev/null
printf 'subjectAltName=DNS:opensearch\nextendedKeyUsage=serverAuth,clientAuth\n' > "$work/ext"
openssl x509 -req -in "$work/node.csr" -CA secrets/search-ca.pem -CAkey "$work/ca.key" -CAcreateserial \
	-CAserial "$work/ca.srl" -days 3650 -extfile "$work/ext" -out secrets/search.pem 2> /dev/null

openssl rand -hex 24 > secrets/search_password

# The cluster keeps a bcrypt hash of the password, which its own tool makes:
# the image compose.search.yaml runs, built here first.
docker build -q -t rove-opensearch:local ../opensearch > /dev/null
hash=$(docker run --rm --entrypoint /usr/share/opensearch/plugins/opensearch-security/tools/hash.sh \
	rove-opensearch:local -p "$(cat secrets/search_password)" | tail -n 1)
case $hash in
'$2'*) ;;
*)
	echo "search-init.sh: the hash tool said $hash" >&2
	exit 1
	;;
esac
cat > secrets/search-users.yml <<YAML
_meta:
  type: "internalusers"
  config_version: 2

rove:
  hash: "$hash"
  reserved: true
  description: "Rove, as compose.search.yaml signs it in"
YAML

# Read by the users inside the containers; the directory keeps everybody else
# out, as for the bundle's other keys.
chmod 444 secrets/search*
echo "made secrets/search-ca.pem, search.pem, search-key.pem, search_password and search-users.yml"
