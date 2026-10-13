# 한 대의 서버에 Rove 띄우기 (운영자)

`docker compose` 하나로 운영 구성 전체를 띄웁니다.

| 서비스 | 하는 일 |
| --- | --- |
| `postgres` | PostgreSQL 18 + pg_bigm(`deploy/postgres`). Rove와 내장 roster가 데이터베이스를 하나씩 씁니다(`rove`, `roster`) |
| `migrate` | 이 빌드의 스키마로 테이블을 맞추고 끝납니다(`rove migrate`). 서버는 이것이 끝난 뒤에 뜨고, 스스로는 테이블을 바꾸지 않습니다(`db.migrate: false`) |
| `rove` ×2 | `rove serve`. 백그라운드 작업은 리스로 한 대만 돌고(`spin.lease: postgres`), 변경 알림은 DB로 주고받습니다(`watch.broker: postgres`) |
| `rove-admin` | 운영자 진입점. 호스트의 `127.0.0.1:8081`에만 열립니다 |
| `caddy` | 80/443. Let's Encrypt 인증서를 받고 두 `rove`에 나눠 보냅니다 |

인프라에 붙일 것은 이것뿐입니다: 이 파일들을 둘 리눅스 서버 한 대(Docker와 compose), 그 서버를 가리키는 도메인(DNS A 레코드), 열린 80/443 포트.

## 채울 값

### `.env` (비밀값과 이 배포의 것)

```sh
cp .env.example .env && chmod 600 .env
```

| 값 | 무엇 | 만드는 법 |
| --- | --- | --- |
| `ROVE_DOMAIN` | 사람들이 여는 주소. 라벨 QR이 이 주소를 담으므로 **라벨을 인쇄하기 전에 정하고 바꾸지 않습니다**(rove#35) | |
| `POSTGRES_PASSWORD` | DB 비밀번호. 영숫자만(DB 주소에 그대로 들어갑니다) | `openssl rand -hex 24` |
| `ROVE_SIGNING_KEY` | 다운로드 링크 서명. 모든 서버가 같아야 합니다 | `openssl rand -base64 32` |
| `ROVE_MAIL_*` | 초대 메일을 보낼 SMTP. 비우면 메일 대신 로그에 남습니다 | |
| `ROVE_VERSION` | 이미지 이름과 `rove version`이 말할 릴리스 이름 | `git describe --tags --always` |

### `secrets/` (파일로 주는 키)

```sh
mkdir -m 700 secrets
openssl genpkey -algorithm ed25519 -out secrets/checkpoints.pem   # 감사 기록 체크포인트 서명 키
openssl rand -hex 32 > secrets/admin_key                          # 운영자 키 (32자 이상)
chmod 444 secrets/*   # 컨테이너 안의 사용자가 읽게. 디렉터리가 700이라 호스트의 다른 사용자는 못 읽습니다
```

- 감사 기록 서명은 파일 키로 켜져 있습니다(rove#35). 키를 잃으면 이전 체크포인트를 검증할 수 없으니 **백업에 함께 넣습니다**.
- 운영자가 여럿이면 `rove.yaml`의 `admin.keys`에 이름마다 키 파일을 하나씩 더합니다.

### `rove.yaml` (비밀이 아닌 설정)

그대로 써도 되는 값이 채워져 있습니다. 운영 전에 정할 것은 주석으로 남겨 두었습니다.

- **감사 기록 보존 기간**(`audit.by`): 정하기 전에는 아무것도 보관소로 옮기거나 지우지 않습니다. 규제에 맞는 값을 고릅니다(plan M5 제안: 구성원 자격은 `pipa`).
- **약관·개인정보 처리방침**(`app.consent`): 문서를 게시한 뒤 버전과 주소를 넣습니다. `keep`은 탈퇴한 계정의 동의 기록 보존 기간이며, 비우면 계속 둡니다.
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

## 운영자 진입점

`rove-admin`은 호스트의 루프백에만 열립니다. 다른 곳에서는 VPN이나 SSH 터널로 들어옵니다.

```sh
ssh -L 8081:127.0.0.1:8081 <서버>
curl -H "authorization: Bearer $(cat secrets/admin_key)" http://127.0.0.1:8081/admin/workspaces
```

## 새 버전으로 올리기

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
| `caddy_data` | 인증서 |

백업할 것은 이 볼륨들과 `.env`, `secrets/`입니다. PostgreSQL 시점 복원, 백업을 다른 장소로 보내기, 복구 리허설은 rove#37입니다. DB를 백업에서 복원한 뒤에는 런북의 "DB를 백업에서 복원했을 때"를 따릅니다.

## 이 구성에 없는 것

- 백업과 복구 리허설: rove#37
- 관측(수집기, 대시보드, 경보): rove#38
- 스테이징과 배포 절차: rove#39
- 보안 점검과 서버 하드닝: rove#40
- 워크스페이스별 라벨 도메인(on-demand TLS): `app.labels.tenant_domains`를 켤 때 Caddy에 더합니다
