# Loft와 roster를 한 대에 띄우기 (self-hosted)

직접 운영하는 배포의 모양입니다. **roster**가 Loft 곁에서 사람과 로그인을 맡고, 테넌트의 관리자가 roster의 화면(user console)에서 구성원을 넣고 뺍니다(design 9.10, D87). Loft는 그 테넌트에 설치된 앱이고, 테넌트의 키로 roster에 묻습니다. 운영의 기준은 Kubernetes(`deploy/k8s`)이고, 이 디렉터리는 같은 이미지와 같은 설정을 `docker compose` 하나로 한 대에 띄웁니다. CI의 `bundle` 작업이 roster부터 로그인까지 PR마다 그대로 확인합니다.

**한 대에서 써 보기만 할 때**는 roster를 따로 띄우지 않아도 됩니다. Loft 프로세스 안의 roster(내장 roster)로 `loft init --demo` 를 하면 됩니다([저장소의 README](../../README.md#바로-써-보기)). 내장 roster는 시험과 개발, 써 보기용이고 사람을 넣고 빼는 화면이 없습니다. 사람이 쓰는 배포는 이 디렉터리의 모양입니다.

| 서비스 | 하는 일 |
| --- | --- |
| `postgres` | PostgreSQL 18(공식 이미지). Loft의 데이터베이스(`loft`)와, 사용자가 따로인 roster의 데이터베이스 둘(`roster`, `roster_control`)이 있습니다 |
| `roster` | 사람, 로그인, 구성원인지가 이것의 것입니다. Loft는 compose 네트워크 안에서 gRPC(50051)로 묻고, 사람 화면(user console)은 Caddy를 거쳐 `ROSTER_DOMAIN`에서 열립니다. roster 운영자의 일은 상자에서 합니다(`docker compose exec roster roster …`) |
| `migrate` | 이 빌드의 스키마로 테이블을 맞추고 끝납니다(`loft migrate`). 서버는 이것이 끝난 뒤에 뜨고, 스스로는 테이블을 바꾸지 않습니다(`db.migrate: false`) |
| `loft` ×2 | `loft serve`. 백그라운드 작업은 리스로 한 대만 돌고(`spin.lease: postgres`), 변경 알림은 DB로 주고받습니다(`watch.broker: postgres`) |
| `loft-admin` | 운영자 진입점. 호스트의 `127.0.0.1:8081`에만 열립니다 |
| `caddy` | 80/443. Let's Encrypt 인증서를 받고, `LOFT_DOMAIN`은 두 `loft`에, `ROSTER_DOMAIN`은 roster의 화면에 보냅니다 |
| `opensearch` | 선택. 켜면 검색을 OpenSearch와 한국어 분석기 nori가 답합니다(`compose.search.yaml`, 아래 "검색을 켜려면") |

서버 한 대에 이대로 올릴 수도 있습니다(Let's Encrypt까지 됩니다). 그럴 때 붙일 것은 리눅스 서버 한 대(Docker와 compose), 그 서버를 가리키는 도메인 둘(Loft의 것과 roster 화면의 것, DNS A 레코드), 열린 80/443 포트입니다. 운영의 기준은 `deploy/k8s`입니다.

## 채울 값

### `.env` (비밀값과 이 배포의 것)

```sh
cp .env.example .env && chmod 600 .env
```

| 값 | 무엇 | 만드는 법 |
| --- | --- | --- |
| `LOFT_DOMAIN` | 사람들이 여는 주소. 라벨 QR이 이 주소를 담으므로 **라벨을 인쇄하기 전에 정하고 바꾸지 않습니다**(loft#35) | |
| `ROSTER_DOMAIN` | roster 화면의 주소. 테넌트의 관리자가 여기서 구성원을 넣고 뺍니다. 세션 쿠키가 호스트에만 붙으므로 Loft와 다른 호스트입니다. Loft의 사람 화면이 이리로 갑니다(`auth.roster.console_url`) | |
| `LOFT_TENANT` | 이 배포가 섬기는 roster 테넌트의 별칭(`auth.roster.tenant`). 그 테넌트가 곧 워크스페이스입니다(D85). roster에 먼저 만들고(아래 "처음 띄우기"), `loft init`이 세웁니다 | |
| `POSTGRES_PASSWORD` | Loft의 DB 비밀번호. 영숫자만(DB 주소에 그대로 들어갑니다) | `openssl rand -hex 24` |
| `ROSTER_DB_PASSWORD` | roster의 DB 사용자 비밀번호. Loft의 사용자는 roster의 행에 닿지 않습니다. 영숫자만 | `openssl rand -hex 24` |
| `ROSTER_VOUCH_KEY` | roster가 2단계 인증의 비밀을 봉하는 키. 누가 2단계 인증을 쓰기 시작한 뒤에는 바꾸지 않고, **백업에 함께 넣습니다** | `openssl rand -base64 32` |
| `ROSTER_KEY` | Loft가 roster에 묻는 키. 테넌트의 키(`rt_…`)이고, Loft를 테넌트에 설치한 뒤 `roster key add`가 한 번 보여 줍니다(아래). 그 전에는 비워 두고, 비어 있으면 Loft가 뜨지 않습니다 | |
| `LOFT_SIGNING_KEY` | 다운로드 링크 서명, 그리고 세션의 키를 보관하는 키([세션의 키](../../docs/dev/security.md#세션의-키)). 모든 서버가 같아야 하고, 바꾸면 모두 로그아웃됩니다 | `openssl rand -base64 32` |
| `LOFT_MAIL_*` | 알림 메일을 보낼 SMTP. 비우면 메일로 보내지 않고 앱에서만 알립니다 | |
| `LOFT_VERSION` | 이미지 이름과 `loft version`이 말할 릴리스 이름 | `git describe --tags --always` |
| `ROSTER_IMAGE` | roster의 이미지. 비우면 `compose.yaml`이 고른 것(이 빌드의 Loft와 맞는 roster)입니다. 운영에서는 digest로 고정합니다 | |

### `secrets/` (파일로 주는 키)

```sh
mkdir -m 700 secrets
openssl genpkey -algorithm ed25519 -out secrets/checkpoints.pem   # 감사 기록 체크포인트 서명 키
openssl rand -base64 32 > secrets/digest.key                      # 감사 기록이 행마다 남기는 지문의 키
openssl rand -hex 32 > secrets/admin_key                          # 운영자 키 (32자 이상)
chmod 444 secrets/*   # 컨테이너 안의 사용자가 읽게. 디렉터리가 700이라 호스트의 다른 사용자는 못 읽습니다
```

- 감사 기록 서명은 파일 키로 켜져 있습니다(loft#35). 키를 잃으면 이전 체크포인트를 검증할 수 없으니 **백업에 함께 넣습니다**.
- 운영자가 여럿이면 `loft.yaml`의 `admin.keys`에 이름마다 키 파일을 하나씩 더합니다.

### `loft.yaml` (비밀이 아닌 설정)

그대로 써도 되는 값이 채워져 있습니다. 운영 전에 정할 것은 주석으로 남겨 두었습니다.

- **감사 기록 보존 기간**(`audit.by`): 정하기 전에는 아무것도 보관소로 옮기거나 지우지 않습니다. 규제에 맞는 값을 고릅니다(plan M5 제안: 구성원 자격은 `pipa`).
- `app.csp`: `report-only`로 시작합니다([콘텐츠 보안 정책](../../docs/dev/security.md#콘텐츠-보안-정책-csp)).
- roster는 곁의 `roster` 서비스입니다(`auth.roster.addr: roster:50051`, compose 네트워크 안이라 평문 gRPC). 사람은 roster의 비밀번호로 Loft에 로그인합니다. SSO(roster의 발급자, `auth.sso`)는 이 구성에 없습니다.

## 처음 띄우기

roster가 먼저입니다. Loft는 roster의 테넌트에 설치되는 앱이고, 그 테넌트의 키로 뜨기 때문입니다.

```sh
# 1. roster: 운영자, 테넌트와 그 관리자, 화면의 주소
docker compose up -d postgres roster caddy
roster() { docker compose exec -T roster /usr/local/bin/roster "$@"; }
roster init                                            # roster 운영자 admin. 비밀번호를 한 번 보여 줍니다
roster --as admin tenant add @acme '{"name": "ACME"}'  # 테넌트(.env의 LOFT_TENANT)와 그 관리자 admin
roster vouch reset @acme/admin                         # 테넌트 관리자의 비밀번호. 한 번 보여 줍니다
roster --as admin host add '{"tenant": {"alias": "acme"}, "name": "id.example.com"}'   # ROSTER_DOMAIN이 이 테넌트의 화면

# 2. Loft를 그 테넌트에 설치하고, 테넌트의 키를 만듭니다
m=/roster.VouchService/Verify,/roster.HolderService/Get,/roster.TenantService/Get,/roster.MeService/Get
m=$m,/roster.EmailService/List,/roster.EmailService/Get,/roster.SyncService/Watch
m=$m,/roster.HolderService/Reaches,/roster.RoleService/Add,/roster.RoleService/Get,/roster.RoleService/Widen,/roster.BindingService/Add
m=$m,'/loft.*/*'
roster app install --tenant acme --role "$m" --administer '/loft.*/*' loft
roster key add --tenant acme --holder loft --name loft --allow "$m"   # rt_… 를 한 번 보여 줍니다 → .env의 ROSTER_KEY

# 3. Loft: 그 테넌트를 워크스페이스로 세우고, 테넌트의 관리자를 소유자로 둡니다
docker compose up -d --build
docker compose ps                     # migrate는 Exited (0), 나머지는 running
docker compose run --rm loft --config /etc/loft/loft.yaml init --name "ACME"

curl https://$LOFT_DOMAIN/healthz     # ok
```

- **`m`** 은 Loft가 roster에 묻는 메서드 전부(`identity.AgentMethods`)입니다. Loft가 테넌트에 써 넣는 처음 역할을 쓰고 넓히고 첫 소유자에게 묶는 데 드는 메서드(`RoleService/Add`·`Get`·`Widen`, `BindingService/Add`)가 그 안에 있습니다(D89). `/loft.*/*` 를 쥐어야 자기 메서드로 된 역할을 쓸 수 있고, `--administer '/loft.*/*'` 가 있어야 테넌트의 관리자가 그 역할을 사람에게 줄 수 있습니다(roster `docs/apps.md` 의 *The roles an app ships*). 키는 허용한 메서드로도 좁혀지므로 `--allow` 에 같은 목록을 줍니다.
- **소유자**는 `loft init --login` 의 사람이고, 기본은 테넌트를 만들 때 roster가 넣은 관리자 `admin` 입니다. `init` 이 Loft의 처음 역할 다섯을 roster에 쓰고 그 사람에게 `loft-owner` 를 묶습니다(D89). 그 사람이 `https://$LOFT_DOMAIN` 에 roster의 비밀번호로 로그인합니다.
- **구성원**은 테넌트의 관리자가 `https://$ROSTER_DOMAIN` 에 로그인해 roster의 화면에서 넣고 Loft의 역할(보통 `loft-member`)을 묶습니다. 그 사람이 처음 Loft에 로그인하면 구성원이 됩니다. 역할이 하나도 없으면 로그인이 거절됩니다([관리자 안내서](../../docs/guide/admin.md#사람-들이기)).
- roster 운영자의 명령은 상자에서, 그 운영자의 이름으로(`--as`) 합니다. roster의 접속기록이 그 사람을 남깁니다(roster `docs/operating.md` 의 *The CLI on the box is the deployment*).

## 검색을 켜려면 (OpenSearch)

검색은 기본으로 데이터베이스가 답합니다. `compose.search.yaml`을 함께 읽게 하면 OpenSearch와 한국어 분석기 nori가 답합니다(D31). 붙여 쓴 낱말(웹서버), 조사(의자를), 오타도 찾습니다. 서버에 메모리가 약 1 GB 더 듭니다(힙 512 MB, `.env`의 `LOFT_SEARCH_HEAP`로 바꿉니다).

```sh
./search-init.sh                                               # 한 번. secrets/에 인증서와 비밀번호를 만듭니다
echo 'COMPOSE_FILE=compose.yaml:compose.search.yaml' >> .env
docker compose up -d --build
docker compose exec loft-admin /usr/local/bin/loft --url http://127.0.0.1:8081 --key file:/run/secrets/admin_key search status
```

- **색인은 서버가 만듭니다.** 처음 뜰 때 색인이 없으면 DB의 자산으로 만들고, 그다음부터 바뀐 것을 1초쯤 안에 반영합니다. `loft search reindex`를 따로 할 필요는 없습니다.
- **`search-init.sh`가 `secrets/`에 만드는 것:**
  - 이 배포만의 CA와, 그 CA가 서명한 `opensearch`의 인증서
  - Loft가 로그인할 비밀번호
  - 그 비밀번호의 해시를 담은 사용자 파일
  - CA의 키는 서명한 뒤 버립니다.
- **클러스터에는 사용자가 Loft 하나뿐입니다.** 데모 계정과 admin이 없습니다. Loft의 role은 bulk 쓰기와 자기 인덱스(`loft-*`)뿐입니다(`opensearch/roles.yml`).
- **인증서나 비밀번호를 바꾸려면** 클러스터를 새로 만듭니다. 색인은 사본이라 잃는 것이 없습니다.

  ```sh
  docker compose down && docker volume rm loft_opensearch
  rm secrets/search* && ./search-init.sh
  docker compose up -d
  ```

- **끄려면** `.env`에서 `COMPOSE_FILE` 줄을 지우고 `docker compose up -d --remove-orphans`를 합니다. 검색은 다시 데이터베이스가 답합니다.
- `loft`, `loft-admin`, `migrate` 모두 같은 검색 설정을 받습니다. 운영자가 워크스페이스를 파기한 것도 색인에 닿아야 하기 때문입니다.

## 운영자 진입점

`loft-admin`은 호스트의 루프백에만 열립니다. 다른 곳에서는 VPN이나 SSH 터널로 들어옵니다.

```sh
ssh -L 8081:127.0.0.1:8081 <서버>
LOFT_ADMIN_URL=http://127.0.0.1:8081 LOFT_ADMIN_KEY=file:secrets/admin_key loft tenant ls
# 서버에서, 운영자의 컴퓨터에 loft 가 없으면 컨테이너의 것으로
docker compose exec loft-admin /usr/local/bin/loft --url http://127.0.0.1:8081 --key file:/run/secrets/admin_key tenant ls
# 또는 RPC를 그대로 (Connect, 운영자의 키)
curl -H "authorization: Bearer $(cat secrets/admin_key)" -H 'content-type: application/json' \
  -H 'connect-protocol-version: 1' -d '{}' http://127.0.0.1:8081/loft.TenantService/List
```

운영자의 일(워크스페이스의 내보내기·파기·복구, 작업 기록의 점검, 계정, 검색 색인, 보존)은 모두 이것으로 합니다([운영자 안내서](../../docs/guide/operator.md#운영자-진입점-loft-admin)). 파일은 호출 안의 바이트로 오가므로 컨테이너에서 꺼낼 것이 없습니다. 서버 상자의 `loft` 에 남은 것은 `init`, `migrate`, `serve` 입니다.

## 새 버전으로 올리기

넘는 판에 한 번 할 일이 있으면 [판을 올릴 때](../../docs/runbook/upgrade.md)에 있습니다. 출시 전에는 이전 빌드의 데이터를 옮기지 않으므로, 호환 세대(`.github/compat-epoch`)가 바뀐 빌드로는 올리지 않고 처음부터 다시 만듭니다.

먼저 새로 필요한 것을 봅니다: `git fetch`한 뒤 `git diff HEAD..<올릴 커밋> -- deploy/compose`. `secrets/`에 만들 파일이 늘었으면 위 `secrets/`의 명령으로(`chmod 444`까지) **먼저 만듭니다**. 없으면 `docker compose up`이 `bind source path does not exist`로 멈춥니다.

```sh
git pull                              # 또는 올릴 태그로
LOFT_VERSION=$(git describe --tags --always) docker compose up -d --build
```

`migrate`가 먼저 테이블을 맞추고, 그다음 서버가 새 이미지로 바뀝니다. 바뀌는 동안 잠깐 끊깁니다. 끊김 없는 배포와 되돌리는 방법은 loft#39에서 정합니다.

## 데이터가 있는 곳

| 볼륨 | 내용 |
| --- | --- |
| `postgres` | Loft의 DB와 roster의 DB 둘(데이터 플레인과 컨트롤 플레인) |
| `files` | 첨부 파일 |
| `audit` | 감사 기록 보관소 |
| `checkpoints` | 감사 기록 체크포인트. 삭제를 거부하는 디스크에 두면 더 좋습니다(`docs/runbook/trail-integrity.md`) |
| `witness` | 감사 기록의 증인(`store:`)이 들은 체크포인트. 보관소와 다른 디스크, DB·보관소와 다른 권한에 두어야 증인 노릇을 합니다(`docs/runbook/trail-integrity.md`의 "체크포인트의 증인") |
| `caddy_data` | 인증서 |
| `opensearch` | 검색 색인. 켰을 때만 있고, DB로 다시 만들 수 있어서 백업하지 않아도 됩니다 |

백업할 것은 이 볼륨들과 `.env`(roster의 `ROSTER_VOUCH_KEY`까지), `secrets/`입니다. PostgreSQL 시점 복원, 백업을 다른 장소로 보내기, 복구 리허설은 loft#37입니다. DB를 백업에서 복원한 뒤에는 런북의 "DB를 백업에서 복원했을 때"를 따릅니다.

## 이 구성에 없는 것

- roster의 발급자(Hydra와 Login App)를 거치는 SSO(`auth.sso`), roster의 front door(계정 페이지, `auth.roster.account_url`): roster의 `deploy/`와 `docs/operating.md`

- 백업과 복구 리허설: loft#37
- 관측(수집기, 대시보드, 경보): loft#38
- 스테이징과 배포 절차: loft#39
- 보안 점검과 서버 하드닝: loft#40
