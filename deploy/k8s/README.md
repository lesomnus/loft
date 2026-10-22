# Kubernetes에 Rove 배포하기 (운영자)

이 디렉터리는 Rove의 **참조 배포**입니다. 배포가 실제로 돌리는 매니페스트를 kustomize base로 두고, 실제 배포는 이 위에 얹는 **오버레이**입니다(GitOps 저장소, Argo CD 등). roster의 `deploy/`와 같은 방식입니다.

```yaml
resources:
  - github.com/lesomnus/rove//deploy/k8s?ref=<커밋>   # 브랜치가 아니라 커밋으로 고정합니다
```

`scripts/cluster.sh`가 이 base를 오버레이(`scripts/cluster/`)째로 k3d에 띄워 확인하고, CI의 `cluster` 작업이 PR마다 같은 것을 돌립니다. 그래서 매니페스트가 조용히 깨지지 않습니다. `scripts/cluster/`는 그대로 오버레이의 작성 예이기도 합니다(Secret만 빼고. Secret은 스크립트가 만듭니다).

## 무엇이 있나

| | |
| --- | --- |
| `rove` Deployment | 파드 하나에 컨테이너 셋: `migrate`(init 컨테이너, `rove migrate`), `rove`(`rove serve`, 8080), `rove-admin`(운영자 진입점, 8081) |
| PVC 셋 | `rove-files`(첨부), `rove-audit`(감사 기록 보관소), `rove-checkpoints`(그 체크포인트). 모두 ReadWriteOnce |
| 컴포넌트 `witness/` (선택) | 감사 기록의 증인 저장소(`store:`)를 위한 PVC `rove-witness`와 그 마운트. base에는 넣지 않습니다(아래 6) |
| `rove` Service | 8080 하나. 인그레스가 이리로 보냅니다. 운영자 진입점에는 Service가 없습니다 |
| NetworkPolicy `rove` | 네트워크로는 8080만 받습니다. gRPC(50051)와 운영자 진입점(8081)에는 아무것도 닿지 않습니다 |
| ConfigMap `rove` | `config.yaml`, 모든 프로세스가 `--config`로 읽는 파일 |

**Secret은 없습니다.** 오버레이가 같은 이름으로 가져옵니다. base에 Secret이 있으면 오버레이의 것과 이름이 부딪혀 빌드가 되지 않습니다(roster가 겪은 일).

파드는 `restricted` Pod Security를 통과합니다. root가 아닌 사용자(65532)로 돌고, 루트 파일 시스템은 읽기 전용이며, 권한은 모두 내려놓고, 서비스 계정 토큰도 받지 않습니다.

## 왜 파드 하나인가

- **볼륨이 ReadWriteOnce라서.** 어느 클러스터에나 있는 종류이지만 한 노드에만 붙습니다. 그래서 첨부와 감사 기록을 여는 프로세스(서버와 운영자 진입점)를 같은 파드에 둡니다.
- **`Recreate`라서.** 이전 파드가 내려간 뒤에 새 파드의 init 컨테이너가 마이그레이션합니다. 그래서 어떤 빌드도 자기가 모르는 스키마로 서비스하지 않습니다. 대신 배포할 때마다 수십 초 끊깁니다.
- **두 대 이상으로 가려면** 첨부와 감사 기록을 모든 노드가 닿는 곳에 둡니다(S3는 rove#45, 아니면 ReadWriteMany 스토리지). 그다음에 할 일:
  - 복제본을 늘리고 `RollingUpdate`로 바꿉니다.
  - 운영자 진입점을 Deployment 하나로 따로 뺍니다.
  - 마이그레이션은 이전 빌드가 견딜 수 있는 것만 합니다(아래 "되돌리기").

## 오버레이가 가져올 것

1. **namespace.** `pod-security.kubernetes.io/enforce: restricted` 라벨을 붙이기를 권합니다.
2. **Secret 셋.** 저장소에는 SOPS(ksops) 같은 것으로 암호화해 둡니다.

   | Secret | 키 | 내용 | 만드는 법 |
   | --- | --- | --- | --- |
   | `rove` | `db-dsn` | Rove의 DB 주소. 예: `postgres://rove:…@db:5432/rove?sslmode=require` | |
   | | `roster-db-dsn` | 내장 roster의 DB 주소. Rove와 다른 데이터베이스입니다 | |
   | | `signing-key` | 다운로드 링크 서명. 모든 프로세스가 같아야 합니다 | `openssl rand -base64 32` |
   | | `mail-password` | SMTP 비밀번호. 없어도 됩니다 | |
   | `rove-checkpoints` | `checkpoints.pem` | 감사 기록 서명 키. 잃으면 이전 체크포인트를 검증할 수 없으니 **백업에 함께 넣습니다** | `openssl genpkey -algorithm ed25519` |
   | | `digest.key` | 감사 기록이 행마다 남기는 지문(HMAC)의 키. `rove trail rows`가 이것으로 DB에서 직접 바뀐 행을 찾습니다. 잃으면 이전 지문을 검증할 수 없으니 **백업에 함께 넣습니다**. DB 백업과는 따로 둡니다 | `openssl rand -base64 32` |
   | `rove-admin` | 운영자 이름마다 파일 하나(`operator`) | 운영자 키, 32자 이상. 운영자를 더하면 `admin.keys`에도 이름을 더합니다 | `openssl rand -hex 32` |
   | `rove-search` | `password`, `ca.pem` | 검색 클러스터의 Rove 사용자 비밀번호와 그 클러스터의 CA. 검색을 켤 때만 둡니다(아래 9) | |

3. **설정.** base의 `config.yaml`을 복사해 채우고 바꿔 끼웁니다.

   ```yaml
   configMapGenerator:
     - name: rove
       behavior: replace
       files:
         - rove.yaml=config.yaml
   ```

   채울 것:
   - `app.public_url`: 반드시 넣습니다. 없으면 서버가 뜨지 않습니다(rove#40). 라벨 QR이 이 주소를 담으므로 라벨을 인쇄하기 전에 정하고 바꾸지 않습니다.
   - `app.trusted_proxies`: 인그레스 컨트롤러의 주소입니다(아래 5).
   - `app.mail`, `app.consent`, `audit.by`: 정해지는 대로 넣습니다(rove#35).
   - 감사 기록의 보존 기간(`audit.retain`, `audit.by`)이 하나도 없으면 보존 패스가 돌지 않아 체크포인트도 남지 않습니다. 지우지 않는 기간이라도 하나 둡니다(`docs/runbook/restore.md`).
4. **인그레스.** 호스트, TLS(cert-manager 등), 백엔드 `rove:8080`을 정합니다.
   - 실시간 화면(watch)은 끝나지 않는 응답입니다. 그래서 프록시가 응답을 모아 두거나 일찍 끊으면 안 됩니다.
   - ingress-nginx라면 `nginx.ingress.kubernetes.io/proxy-buffering: "off"`를 주고 `proxy-read-timeout`을 길게 잡습니다.
   - 업로드 크기 제한도 확인합니다. 서버는 32 MiB까지 받습니다.
5. **NetworkPolicy 좁히기.** `from`을 인그레스 컨트롤러로 좁힙니다.
   - 그래야 `trusted_proxies`에 파드 네트워크 전체를 넣어도 안전합니다. 서버에 바로 닿는 파드는 X-Forwarded-For를 마음대로 적을 수 있기 때문입니다.
   - 예는 `scripts/cluster/kustomization.yaml`의 patch에 있습니다(Traefik, `kube-system`).
6. **스토리지.** `storageClassName`과 크기를 정합니다. 체크포인트는 삭제를 거부하는 클래스가 있으면 그것을 씁니다(`docs/runbook/trail-integrity.md`).
   - **감사 기록의 증인.** base의 설정은 `audit.checkpoints.witnesses: ["log:"]`입니다. `log:`는 볼륨이 필요 없지만, 런북의 경보 두 개를 걸어야 증인 노릇을 합니다. 체크포인트를 따로 들어 두는 저장소(`store:`)도 두려면 컴포넌트를 넣고 설정에 이름을 적습니다. `scripts/cluster/`가 그 예입니다.

     ```yaml
     components:
       - github.com/lesomnus/rove//deploy/k8s/witness?ref=<base와 같은 커밋>
     ```

     ```yaml
     audit:
       checkpoints:
         witnesses: ["log:", "store:/var/lib/rove/witness"]
     ```

     `rove-witness`는 보관소와 다른 스토리지에, DB·보관소와 다른 권한으로 둡니다. 삭제를 거부하는 클래스면 더 좋습니다. 보관소와 같은 디스크의 증인은 증인이 아닙니다. 둘 다에 손댈 수 있는 사람은 둘에서 함께 지웁니다.
7. **이미지.** 태그가 아니라 digest로 고정합니다: `images: [{name: ghcr.io/lesomnus/rove, digest: sha256:…}]`.
8. **PostgreSQL.** Rove와 내장 roster의 데이터베이스 두 개가 필요합니다.
   - 필요한 확장은 `btree_gist` 하나입니다. 검색은 9의 검색 엔진이 답하고, 없으면 데이터베이스가 인덱스 없이 답합니다.
   - 어디서 돌릴지(operator인지 관리형인지)는 백업과 함께 정합니다(rove#37).
   - LISTEN과 세션 잠금은 트랜잭션 모드 풀러를 거치면 안 됩니다. 풀러를 쓴다면 `watch.dsn`과 `spin.dsn`에 DB로 바로 가는 주소를 줍니다.
9. **검색(선택).** OpenSearch와 한국어 분석기 nori가 검색에 답하게 합니다(D31).
   - 클러스터는 이 구성에 들어 있지 않습니다. 공식 Helm 차트나 OpenSearch Kubernetes Operator로 따로 띄웁니다. 이미지는 nori를 넣은 `deploy/opensearch`를 씁니다.
   - 클러스터에 Rove의 role과 사용자를 만듭니다. role은 bulk 쓰기와 자기 인덱스뿐입니다(루트 README의 "검색 엔진").
   - Secret `rove-search`에 그 사용자의 `password`와 클러스터의 `ca.pem`을 넣습니다. 파드의 세 컨테이너(migrate, rove, rove-admin)가 모두 받습니다. 운영자가 파기한 워크스페이스도 색인에서 빠져야 하기 때문입니다.
   - `config.yaml`의 `app.search.opensearch` 주석을 풀어 주소를 넣습니다.
   - 색인은 서버가 처음 뜰 때 DB로 만들고, 그다음부터 바뀐 것을 따라갑니다. 색인은 사본이라 클러스터의 데이터를 백업하지 않아도 됩니다.
   - NetworkPolicy는 들어오는 것만 막으므로, 파드에서 클러스터로 나가는 길은 열려 있습니다. 클러스터 쪽에서 Rove의 파드만 받도록 좁힙니다.
10. **수집기(선택).** `config.yaml`의 `otel:`이 로그·추적·지표를 OTLP로 수집기에 보냅니다. 없으면 요청 로그는 표준 출력에만 나옵니다.
    - 노드마다 수집기가 있으면 파드가 받는 `HOST_IP`(그 노드의 주소)로 보냅니다. Service 뒤의 수집기라면 그 주소를 씁니다. 예는 `config.yaml`의 주석에 있습니다.
    - 서버와 운영자 진입점이 같은 설정으로 함께 보냅니다. 둘 다 `service.name`이 `rove`입니다.
    - 대시보드는 [`deploy/observe`](../observe/README.md)에 있습니다. 이미 있는 ClickHouse와 Loki 위의 Grafana입니다. 경보는 아직 없고, 무엇에 걸지는 `docs/runbook/observe.md`에 있습니다(rove#38).

## 처음 띄우기

```sh
# 1. 데이터베이스 둘. roster의 것은 Rove의 것 옆에 하나 더:
#      CREATE DATABASE roster OWNER rove;
# 2. Secret 셋 (위의 표)
# 3. sync. Argo CD가 아니라면:
kubectl apply -k <오버레이>
kubectl -n rove rollout status deploy/rove

# 4. 첫 워크스페이스와 소유자. --password를 빼면 만들어서 한 번 보여 줍니다.
kubectl -n rove exec deploy/rove -c rove -- rove --config /etc/rove/rove.yaml init \
  --tenant acme --name "ACME" --login owner --person "홍길동" --email owner@example.com

curl https://<호스트>/healthz   # ok
```

## 새 버전 올리기

1. **빌드.** main에 머지되면 CI의 `image` 작업이 모든 검사가 통과한 뒤 `ghcr.io/lesomnus/rove:edge`로 올립니다. digest는 그 실행의 요약(Summary)에 남습니다. 오버레이에는 `edge`가 아니라 이 digest를 적습니다.
2. **새로 필요한 것.** 지금 올라가 있는 커밋과 새 커밋 사이에 이 디렉터리가 어떻게 바뀌었는지 봅니다: `git diff <지금 커밋>..<새 커밋> -- deploy/k8s`. 위 표에 Secret의 키가 늘었거나 `config.yaml`이 새 파일을 가리키면, **그 Secret을 먼저 넣고** 설정과 digest는 그다음에 sync합니다. 예를 들어 610c1c7부터는 `rove-checkpoints`에 `digest.key`가 있어야 뜹니다.
   - 순서를 거꾸로 하면 새 파드의 `rove`와 `rove-admin`이 파일이 없다며 뜨지 않습니다(`digest: key: open /run/secrets/rove/digest.key: no such file or directory`). `Recreate`라 이전 파드는 이미 내려가 있으니, Secret을 넣을 때까지 끊깁니다.
   - Secret을 오버레이에 함께 두었다면(SOPS) 같은 커밋에 넣어 한 번에 sync하면 됩니다. 따로 넣는다면 먼저 넣습니다.
3. **스테이징.** 스테이징 오버레이의 digest를 바꿔 sync합니다. `healthz`, 로그인, 바꾼 기능을 확인합니다.
4. **운영.** 같은 digest를 운영 오버레이에 넣고 sync합니다.
   - 순서는 이렇습니다: 이전 파드가 내려가고, 새 파드의 `migrate`가 테이블을 맞추고, 그다음 `rove`와 `rove-admin`이 뜹니다. 그동안 수십 초 끊깁니다.
   - 마이그레이션은 따로 하는 단계가 아니라 init 컨테이너입니다. 실패하면 파드가 뜨지 않고 다시 시도합니다. 이전 파드는 이미 없으니 그때는 되돌립니다.
5. **확인.** `kubectl -n rove rollout status deploy/rove`로 상태를 보고 `kubectl -n rove logs deploy/rove --all-containers`로 로그를 봅니다.

## 되돌리기

- **오버레이의 digest를 이전 것으로 되돌려 sync합니다.** 급하면 `kubectl -n rove rollout undo deploy/rove`로 먼저 되돌리고 Git도 맞춥니다. 자동 sync라면 Git을 맞춰야 다음 sync가 다시 올리지 않습니다.
- **스키마는 되돌리지 않습니다.**
  - 이전 빌드는 새 빌드가 **더한** 테이블·컬럼·인덱스를 무시하고 뜹니다. `serve`의 스키마 확인은 없어진 것만 따지기 때문입니다.
  - 하지만 새 빌드가 컬럼의 타입이나 NULL 허용을 바꿨다면 이전 빌드는 뜨지 않습니다.
  - 새 빌드가 기본값 없는 필수 컬럼을 더했다면 이전 빌드는 그 표에 쓰지 못합니다.
  - 그런 변경은 두 번에 나눠 배포합니다. 먼저 넓게 더하고(NULL 허용, 기본값), 다음 배포에서 좁힙니다.
- **DB를 백업에서 복원했다면** 런북의 "DB를 백업에서 복원했을 때"를 따릅니다(`docs/runbook/trail-integrity.md`). 복원 순서와 확인은 `docs/runbook/restore.md`에 있습니다.

## 설정을 바꿀 때

- **ConfigMap.** 이름에 내용의 해시가 붙으므로 설정을 바꾸면 파드가 새로 뜹니다. 파드가 하나라 모든 프로세스가 함께 바뀝니다. 동의 문서 버전처럼 "모든 서버를 함께 재시작"해야 하는 것도 저절로 그렇게 됩니다.
- **Secret.** 바꿔도 파드가 새로 뜨지 않습니다. `kubectl -n rove rollout restart deploy/rove`로 다시 띄웁니다.

## 운영자 진입점

네트워크로는 닿지 않습니다. Service가 없고, NetworkPolicy가 8081을 닫습니다. 들어오는 길은 `kubectl port-forward` 하나입니다.

```sh
kubectl -n rove port-forward deploy/rove 8081:8081 &
export ROVE_ADMIN_URL=http://127.0.0.1:8081 ROVE_ADMIN_KEY=file:./operator
rove-admin workspace ls
```

들어올 수 있는 사람은 그 namespace의 `pods/portforward` 권한(RBAC)을 가진 사람입니다. VPN 대신 클러스터 권한이 첫 번째 문이고, 운영자 키가 두 번째 문입니다. `pods/exec`도 운영자에게만 줍니다. 파드 안에서는 DB 접속 정보가 보이고, 셸의 운영자 명령은 그것으로 DB에 바로 붙습니다(`docs/security.md`).

## 워크스페이스 내보내기와 복구

이미지에는 셸도 `tar`도 없어서 `kubectl cp`를 쓸 수 없습니다. 파일은 `kubectl exec`의 표준 입출력으로 옮깁니다. `--out -`는 내보내기를 stdout으로 쓰고, 명령이 하는 말은 stderr로 보냅니다. `--file -`는 stdin에서 읽습니다. 순서는 README의 '조직 탈퇴'와 '워크스페이스 복구'와 같습니다.

```sh
kubectl -n rove exec deploy/rove -c rove -- rove --config /etc/rove/rove.yaml tenant export --tenant acme --out - > acme.zip
kubectl -n rove exec -i deploy/rove -c rove -- rove --config /etc/rove/rove.yaml tenant verify --file - < acme.zip
kubectl -n rove exec deploy/rove -c rove -- rove --config /etc/rove/rove.yaml tenant purge --tenant acme --yes
kubectl -n rove exec -i deploy/rove -c rove -- rove --config /etc/rove/rove.yaml tenant import --file - --yes < acme.zip
```

- `-t`는 붙이지 않습니다. 터미널이 끼면 바이너리가 깨집니다. `-i`는 stdin을 넘길 때만 붙입니다.
- `verify`와 `import`는 stdin을 파드의 `/tmp`(emptyDir)에 임시 파일로 받아 읽고, 끝나면 지웁니다. zip은 순서대로 읽을 수 없기 때문입니다. 내보내기 크기만큼 자리가 필요합니다. 자산 10만 개 워크스페이스의 내보내기가 125 MB였습니다.
- stdout으로 쓰다가 실패하면 받은 쪽에 반쯤 된 파일이 남습니다. 종료 코드로 알 수 있고, `tenant verify`도 그 파일을 거부합니다.
- 명령의 로그는 배포의 로그와 같은 곳으로 갑니다. 기본은 stderr이고, stdout에는 내보내기만 나갑니다.

## 스테이징

같은 base에 오버레이를 하나 더 둡니다. namespace, 데이터베이스 둘, 호스트, Secret을 모두 따로 갖습니다. 운영 키는 쓰지 않습니다. 라벨 QR이 `public_url`을 담으므로 스테이징에서 인쇄한 라벨은 운영에서 쓸 수 없습니다.

## 시험해 보기

```sh
./scripts/cluster.sh          # 띄우고, 확인하고, 내림
./scripts/cluster.sh --hold   # 띄운 채로 둠
```

k3d와 Docker가 필요합니다. 이 체크아웃으로 이미지를 빌드해서 `scripts/cluster/`로 띄우고 다음을 확인합니다.

- `restricted` 아래에서 파드가 뜨고, 마이그레이션이 볼륨 없이 먼저 돕니다.
- 인그레스를 거쳐 로그인되고, 보안 헤더와 CSP가 붙습니다.
- 운영자 진입점은 port-forward로는 키에 답하고, 인그레스 컨트롤러가 아닌 파드에서는 서버에도 진입점에도 닿지 않습니다.
- 새 빌드가 파드를 바꾸고 `rollout undo`가 이전 것을 되돌립니다. 그 사이에도 처음 로그인한 세션이 그대로 살아 있습니다.

기본 kubeconfig는 건드리지 않습니다. k3d에 그러지 말라고 하고, 스크립트도 그 파일을 읽지 않습니다.

## 이 구성에 없는 것

- PostgreSQL과 그 백업. 무엇을 백업하고 어떤 순서로 되살리는지, 리허설은 `docs/runbook/restore.md`에 있습니다(rove#37)
- 관측(수집기, 대시보드, 경보): rove#38
- 자동 배포: 오버레이의 digest를 바꾸는 것은 사람이 합니다
- 워크스페이스 도메인의 인증서(rove#155): K8s에서는 on-demand TLS 대신 도메인마다 인증서(cert-manager)가 필요해서 아직 없습니다. rove#155의 TLS 앞단 PR이 다룹니다
- 파드 둘 이상: rove#45
