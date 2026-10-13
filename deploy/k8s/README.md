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
   | `rove-admin` | 운영자 이름마다 파일 하나(`operator`) | 운영자 키, 32자 이상. 운영자를 더하면 `admin.keys`에도 이름을 더합니다 | `openssl rand -hex 32` |

3. **설정.** base의 `config.yaml`을 복사해 채우고 바꿔 끼웁니다.

   ```yaml
   configMapGenerator:
     - name: rove
       behavior: replace
       files:
         - rove.yaml=config.yaml
   ```

   채울 것:
   - `app.public_url`: 반드시 넣습니다. 라벨 QR이 이 주소를 담으므로 라벨을 인쇄하기 전에 정하고 바꾸지 않습니다.
   - `app.trusted_proxies`: 인그레스 컨트롤러의 주소입니다(아래 5).
   - `app.mail`, `app.consent`, `audit.by`: 정해지는 대로 넣습니다(rove#35).
4. **인그레스.** 호스트, TLS(cert-manager 등), 백엔드 `rove:8080`을 정합니다.
   - 실시간 화면(watch)은 끝나지 않는 응답입니다. 그래서 프록시가 응답을 모아 두거나 일찍 끊으면 안 됩니다.
   - ingress-nginx라면 `nginx.ingress.kubernetes.io/proxy-buffering: "off"`를 주고 `proxy-read-timeout`을 길게 잡습니다.
   - 업로드 크기 제한도 확인합니다. 서버는 32 MiB까지 받습니다.
5. **NetworkPolicy 좁히기.** `from`을 인그레스 컨트롤러로 좁힙니다.
   - 그래야 `trusted_proxies`에 파드 네트워크 전체를 넣어도 안전합니다. 서버에 바로 닿는 파드는 X-Forwarded-For를 마음대로 적을 수 있기 때문입니다.
   - 예는 `scripts/cluster/kustomization.yaml`의 patch에 있습니다(Traefik, `kube-system`).
6. **스토리지.** `storageClassName`과 크기를 정합니다. 체크포인트는 삭제를 거부하는 클래스가 있으면 그것을 씁니다(`docs/runbook/trail-integrity.md`).
7. **이미지.** 태그가 아니라 digest로 고정합니다: `images: [{name: ghcr.io/lesomnus/rove, digest: sha256:…}]`.
8. **PostgreSQL.** Rove와 내장 roster의 데이터베이스 두 개가 필요합니다.
   - pg_bigm이 있으면 두 글자 한국어 검색이 빨라집니다(`deploy/postgres`). 없어도 검색은 됩니다.
   - 어디서 돌릴지(operator인지 관리형인지)는 백업과 함께 정합니다(rove#37).
   - LISTEN과 세션 잠금은 트랜잭션 모드 풀러를 거치면 안 됩니다. 풀러를 쓴다면 `watch.dsn`과 `spin.dsn`에 DB로 바로 가는 주소를 줍니다.

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

1. **빌드.** 이미지를 만들어(`docker build .`) 레지스트리에 올리고 digest를 적어 둡니다. 어느 레지스트리에 올릴지는 아직 정하지 않았습니다.
2. **스테이징.** 스테이징 오버레이의 digest를 바꿔 sync합니다. `healthz`, 로그인, 바꾼 기능을 확인합니다.
3. **운영.** 같은 digest를 운영 오버레이에 넣고 sync합니다.
   - 순서는 이렇습니다: 이전 파드가 내려가고, 새 파드의 `migrate`가 테이블을 맞추고, 그다음 `rove`와 `rove-admin`이 뜹니다. 그동안 수십 초 끊깁니다.
   - 마이그레이션은 따로 하는 단계가 아니라 init 컨테이너입니다. 실패하면 파드가 뜨지 않고 다시 시도합니다. 이전 파드는 이미 없으니 그때는 되돌립니다.
4. **확인.** `kubectl -n rove rollout status deploy/rove`로 상태를 보고 `kubectl -n rove logs deploy/rove --all-containers`로 로그를 봅니다.

## 되돌리기

- **오버레이의 digest를 이전 것으로 되돌려 sync합니다.** 급하면 `kubectl -n rove rollout undo deploy/rove`로 먼저 되돌리고 Git도 맞춥니다. 자동 sync라면 Git을 맞춰야 다음 sync가 다시 올리지 않습니다.
- **스키마는 되돌리지 않습니다.**
  - 이전 빌드는 새 빌드가 **더한** 테이블·컬럼·인덱스를 무시하고 뜹니다. `serve`의 스키마 확인은 없어진 것만 따지기 때문입니다.
  - 하지만 새 빌드가 컬럼의 타입이나 NULL 허용을 바꿨다면 이전 빌드는 뜨지 않습니다.
  - 새 빌드가 기본값 없는 필수 컬럼을 더했다면 이전 빌드는 그 표에 쓰지 못합니다.
  - 그런 변경은 두 번에 나눠 배포합니다. 먼저 넓게 더하고(NULL 허용, 기본값), 다음 배포에서 좁힙니다.
- **DB를 백업에서 복원했다면** 런북의 "DB를 백업에서 복원했을 때"를 따릅니다(`docs/runbook/trail-integrity.md`).

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

들어올 수 있는 사람은 그 namespace의 `pods/portforward` 권한(RBAC)을 가진 사람입니다. VPN 대신 클러스터 권한이 첫 번째 문이고, 운영자 키가 두 번째 문입니다.

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

- PostgreSQL과 백업, 복구 리허설: rove#37
- 관측(수집기, 대시보드, 경보): rove#38
- 이미지 레지스트리와 자동 배포: 정할 것(rove#39)
- 워크스페이스별 라벨 도메인(`app.labels.tenant_domains`): K8s에서는 on-demand TLS 대신 도메인마다 인증서(cert-manager)가 필요해서 아직 없습니다
- 파드 둘 이상: rove#45
