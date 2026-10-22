# 운영 구성을 한 대에서 띄워 보기

운영 배포는 Kubernetes입니다(`deploy/k8s`). 이 디렉터리는 같은 이미지와 같은 설정을 `docker compose` 하나로 한 대에 띄워 봅니다. 운영의 모양을 로컬에서 시험하고, CI의 `bundle` 작업이 이미지·마이그레이션·두 복제본·프록시를 PR마다 확인하는 데 씁니다.

| 서비스 | 하는 일 |
| --- | --- |
| `postgres` | PostgreSQL 18(공식 이미지). Rove와 내장 roster가 데이터베이스를 하나씩 씁니다(`rove`, `roster`) |
| `migrate` | 이 빌드의 스키마로 테이블을 맞추고 끝납니다(`rove migrate`). 서버는 이것이 끝난 뒤에 뜨고, 스스로는 테이블을 바꾸지 않습니다(`db.migrate: false`) |
| `rove` ×2 | `rove serve`. 백그라운드 작업은 리스로 한 대만 돌고(`spin.lease: postgres`), 변경 알림은 DB로 주고받습니다(`watch.broker: postgres`) |
| `rove-admin` | 운영자 진입점. 호스트의 `127.0.0.1:8081`에만 열립니다 |
| `caddy` | 80/443. Let's Encrypt 인증서를 받고 두 `rove`에 나눠 보냅니다. 켜면 워크스페이스 도메인의 인증서도 요청 시점에 받습니다(`compose.domains.yaml`, 아래 "워크스페이스 도메인을 받으려면") |
| `opensearch` | 선택. 켜면 검색을 OpenSearch와 한국어 분석기 nori가 답합니다(`compose.search.yaml`, 아래 "검색을 켜려면") |

서버 한 대에 이대로 올릴 수도 있습니다(Let's Encrypt까지 됩니다). 그럴 때 붙일 것은 리눅스 서버 한 대(Docker와 compose), 그 서버를 가리키는 도메인(DNS A 레코드), 열린 80/443 포트입니다. 운영의 기준은 `deploy/k8s`입니다.

## 채울 값

### `.env` (비밀값과 이 배포의 것)

```sh
cp .env.example .env && chmod 600 .env
```

| 값 | 무엇 | 만드는 법 |
| --- | --- | --- |
| `ROVE_DOMAIN` | 사람들이 여는 주소. 라벨 QR이 이 주소를 담으므로 **라벨을 인쇄하기 전에 정하고 바꾸지 않습니다**(rove#35) | |
| `POSTGRES_PASSWORD` | DB 비밀번호. 영숫자만(DB 주소에 그대로 들어갑니다) | `openssl rand -hex 24` |
| `ROVE_SIGNING_KEY` | 다운로드 링크 서명, 그리고 세션의 키를 보관하는 키(README의 "세션의 키"). 모든 서버가 같아야 하고, 바꾸면 모두 로그아웃됩니다 | `openssl rand -base64 32` |
| `ROVE_MAIL_*` | 초대 메일을 보낼 SMTP. 비우면 메일 대신 로그에 남습니다 | |
| `ROVE_VERSION` | 이미지 이름과 `rove version`이 말할 릴리스 이름 | `git describe --tags --always` |

### `secrets/` (파일로 주는 키)

```sh
mkdir -m 700 secrets
openssl genpkey -algorithm ed25519 -out secrets/checkpoints.pem   # 감사 기록 체크포인트 서명 키
openssl rand -base64 32 > secrets/digest.key                      # 감사 기록이 행마다 남기는 지문의 키
openssl rand -hex 32 > secrets/admin_key                          # 운영자 키 (32자 이상)
chmod 444 secrets/*   # 컨테이너 안의 사용자가 읽게. 디렉터리가 700이라 호스트의 다른 사용자는 못 읽습니다
```

- 감사 기록 서명은 파일 키로 켜져 있습니다(rove#35). 키를 잃으면 이전 체크포인트를 검증할 수 없으니 **백업에 함께 넣습니다**.
- 운영자가 여럿이면 `rove.yaml`의 `admin.keys`에 이름마다 키 파일을 하나씩 더합니다.

### `rove.yaml` (비밀이 아닌 설정)

그대로 써도 되는 값이 채워져 있습니다. 운영 전에 정할 것은 주석으로 남겨 두었습니다.

- **감사 기록 보존 기간**(`audit.by`): 정하기 전에는 아무것도 보관소로 옮기거나 지우지 않습니다. 규제에 맞는 값을 고릅니다(plan M5 제안: 구성원 자격은 `pipa`).
- **약관·개인정보 처리방침**(`app.consent`): 문서를 게시한 뒤 버전과 주소를 넣습니다. 동의 기록은 roster의 것이라(D37), 삭제된 계정의 동의 기록 보존 기간은 `auth.roster.consent_keep`이며, 비우면 계속 둡니다.
- `app.csp`: `report-only`로 시작합니다(README의 "콘텐츠 보안 정책").
- SSO(roster의 발급자, `auth.sso`)와 외부 roster(`auth.roster.addr`)는 이 구성에 없습니다. 지금은 Rove 안의 roster에 비밀번호로 로그인합니다.

## 처음 띄우기

```sh
docker compose up -d --build
docker compose ps                     # migrate는 Exited (0), 나머지는 running

# 첫 워크스페이스와 소유자. --password를 빼면 만들어서 한 번 보여 줍니다.
docker compose run --rm rove --config /etc/rove/rove.yaml init \
  --tenant acme --name "ACME" --login owner --person "홍길동" --email owner@example.com

curl https://$ROVE_DOMAIN/healthz     # ok
```

## 검색을 켜려면 (OpenSearch)

검색은 기본으로 데이터베이스가 답합니다. `compose.search.yaml`을 함께 읽게 하면 OpenSearch와 한국어 분석기 nori가 답합니다(D31). 붙여 쓴 낱말(웹서버), 조사(의자를), 오타도 찾습니다. 서버에 메모리가 약 1 GB 더 듭니다(힙 512 MB, `.env`의 `ROVE_SEARCH_HEAP`로 바꿉니다).

```sh
./search-init.sh                                               # 한 번. secrets/에 인증서와 비밀번호를 만듭니다
echo 'COMPOSE_FILE=compose.yaml:compose.search.yaml' >> .env
docker compose up -d --build
docker compose run --rm rove --config /etc/rove/rove.yaml search status
```

- **색인은 서버가 만듭니다.** 처음 뜰 때 색인이 없으면 DB의 자산으로 만들고, 그다음부터 바뀐 것을 1초쯤 안에 반영합니다. `rove search reindex`를 따로 할 필요는 없습니다.
- **`search-init.sh`가 `secrets/`에 만드는 것:**
  - 이 배포만의 CA와, 그 CA가 서명한 `opensearch`의 인증서
  - Rove가 로그인할 비밀번호
  - 그 비밀번호의 해시를 담은 사용자 파일
  - CA의 키는 서명한 뒤 버립니다.
- **클러스터에는 사용자가 Rove 하나뿐입니다.** 데모 계정과 admin이 없습니다. Rove의 role은 bulk 쓰기와 자기 인덱스(`rove-*`)뿐입니다(`opensearch/roles.yml`).
- **인증서나 비밀번호를 바꾸려면** 클러스터를 새로 만듭니다. 색인은 사본이라 잃는 것이 없습니다.

  ```sh
  docker compose down && docker volume rm rove_opensearch
  rm secrets/search* && ./search-init.sh
  docker compose up -d
  ```

- **끄려면** `.env`에서 `COMPOSE_FILE` 줄을 지우고 `docker compose up -d --remove-orphans`를 합니다. 검색은 다시 데이터베이스가 답합니다.
- `rove`, `rove-admin`, `migrate` 모두 같은 검색 설정을 받습니다. 운영자가 워크스페이스를 파기한 것도 색인에 닿아야 하기 때문입니다.

## 워크스페이스 도메인을 받으려면

워크스페이스가 자기 도메인(`assets.acme.co.kr`)에서 앱을 쓰게 하려면 `compose.domains.yaml`을 함께 읽게 합니다(rove#155). 서비스 주소(`ROVE_DOMAIN`)의 인증서는 지금과 같습니다.

```sh
echo 'COMPOSE_FILE=compose.yaml:compose.domains.yaml' >> .env   # 검색도 켰다면 compose.yaml:compose.search.yaml:compose.domains.yaml
docker compose up -d
```

- **무엇이 바뀌나.**
  - Caddy가 `Caddyfile` 대신 `Caddyfile.domains`를 읽습니다. 서비스 주소는 `Caddyfile` 그대로 가져오고, 그 밖의 호스트는 인증서를 요청 시점에 받습니다.
  - 받기 전에(불러올 때도, 갱신할 때도) 운영자 진입점의 `/internal/tls-ask`에 묻습니다. `rove-admin`이 그 물음만 받는 포트(8082, `admin.ask.addr`)를 엽니다.
  - 아무도 확인하지 않은 호스트에는 인증서가 없습니다. DNS가 떠나 `lapsed`가 된 도메인은 갱신도 거절됩니다.
  - 인증서와 ACME 계정은 지금처럼 `caddy_data` 볼륨에 남습니다.
- **발급 횟수**는 묻는 곳이 제한합니다(`rove.yaml`의 `admin.ask`). 기본은 호스트마다 한꺼번에 10번, 그 뒤 5분에 한 번이고, 모두 합쳐 한꺼번에 300번, 그 뒤 30초에 한 번입니다. 이유는 `deploy/k8s/README.md`의 11에 있습니다.
- **고객의 CNAME이 가리킬 이름**은 `ROVE_DOMAIN`입니다. 다른 이름(같은 서버를 가리키는 `edge.rove.example` 등)을 쓰려면 `.env`의 `ROVE_EDGE_HOST`에 적습니다. 워크스페이스 설정에 이 이름이 나오고, 한 시간마다 하는 DNS 재확인이 이것을 봅니다.
- **ACME 계정의 메일**은 `.env`에 `ROVE_EDGE_OPTIONS=email ops@example.com`으로 줍니다. 인증서를 갱신하지 못할 때 알림을 받습니다.
- **고객이 할 일:** 설정 › 도메인과 라벨에 도메인을 더하고, 화면의 레코드 둘을 만듭니다. 확인된 뒤에도 지우지 않습니다.
  - CNAME `<도메인>` → `ROVE_EDGE_HOST`(없으면 `ROVE_DOMAIN`). apex라면 DNS 업체의 ALIAS·CNAME 펼치기를 씁니다.
  - TXT `_rove-challenge.<도메인>` = 토큰.
- **끄려면** `COMPOSE_FILE`에서 `compose.domains.yaml`을 빼고 `docker compose up -d`를 합니다. 워크스페이스의 링크는 활성 앱 도메인으로 계속 만들어지니, 끄기 전에 워크스페이스들이 도메인을 거두게 합니다.

## 운영자 진입점

`rove-admin`은 호스트의 루프백에만 열립니다. 다른 곳에서는 VPN이나 SSH 터널로 들어옵니다.

```sh
ssh -L 8081:127.0.0.1:8081 <서버>
curl -H "authorization: Bearer $(cat secrets/admin_key)" http://127.0.0.1:8081/admin/workspaces
```

## 새 버전으로 올리기

세션을 키의 HMAC으로 보관하는 버전으로 처음 올리면 모두 한 번 다시 로그인합니다(README의 "세션의 키"). 그 밖에 할 일은 없습니다.

먼저 새로 필요한 것을 봅니다: `git fetch`한 뒤 `git diff HEAD..<올릴 커밋> -- deploy/compose`. `secrets/`에 만들 파일이 늘었으면 위 `secrets/`의 명령으로(`chmod 444`까지) **먼저 만듭니다**. 예를 들어 610c1c7부터는 `secrets/digest.key`가 있어야 뜹니다. 없으면 `docker compose up`이 `bind source path does not exist`로 멈춥니다.

```sh
git pull                              # 또는 올릴 태그로
ROVE_VERSION=$(git describe --tags --always) docker compose up -d --build
```

`migrate`가 먼저 테이블을 맞추고, 그다음 서버가 새 이미지로 바뀝니다. 바뀌는 동안 잠깐 끊깁니다. 끊김 없는 배포와 되돌리는 방법은 rove#39에서 정합니다.

## 데이터가 있는 곳

| 볼륨 | 내용 |
| --- | --- |
| `postgres` | Rove와 roster의 DB |
| `files` | 첨부 파일 |
| `audit` | 감사 기록 보관소 |
| `checkpoints` | 감사 기록 체크포인트. 삭제를 거부하는 디스크에 두면 더 좋습니다(`docs/runbook/trail-integrity.md`) |
| `witness` | 감사 기록의 증인(`store:`)이 들은 체크포인트. 보관소와 다른 디스크, DB·보관소와 다른 권한에 두어야 증인 노릇을 합니다(`docs/runbook/trail-integrity.md`의 "체크포인트의 증인") |
| `caddy_data` | 인증서 |
| `opensearch` | 검색 색인. 켰을 때만 있고, DB로 다시 만들 수 있어서 백업하지 않아도 됩니다 |

백업할 것은 이 볼륨들과 `.env`, `secrets/`입니다. PostgreSQL 시점 복원, 백업을 다른 장소로 보내기, 복구 리허설은 rove#37입니다. DB를 백업에서 복원한 뒤에는 런북의 "DB를 백업에서 복원했을 때"를 따릅니다.

## 이 구성에 없는 것

- 백업과 복구 리허설: rove#37
- 관측(수집기, 대시보드, 경보): rove#38
- 스테이징과 배포 절차: rove#39
- 보안 점검과 서버 하드닝: rove#40
- 워크스페이스 도메인의 인증서(on-demand TLS): rove#155의 TLS 앞단 PR이 Caddy에 더합니다
