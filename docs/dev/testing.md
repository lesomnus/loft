# 테스트

```sh
go test ./...                                                    # SQLite
PDTEST_POSTGRES='postgres://loft:loft@localhost:5432/loft?sslmode=disable' go test ./...   # PostgreSQL (공식 postgres:18 이미지)
PDTEST_OPENSEARCH=http://127.0.0.1:9200 go test ./...           # 검색 색인까지 (deploy/opensearch; TLS면 https://user:pass@... 와 PDTEST_OPENSEARCH_CA)
cd ts && npm run check                                           # 타입 검사
```

- 도메인 테스트는 실제 호출과 같은 경로(게이트, 테넌트 벽, 도메인 계층)를 지나고, `cmd` 테스트는 gRPC 체인(세션 쿠키, 역할 표, 배치) 전체를 지납니다.
- 클러스터가 필요한 테스트는 `internal/opensearchtest.Config(t)` 로 클러스터를 받고, 없으면 건너뜁니다. CI의 `opensearch` 작업은 그 패키지들을 security 플러그인을 켠 `deploy/opensearch` 에서, 운영자에게 주는 것과 같은 role로 돌립니다([검색 엔진](search.md#운영-구성에서-켜기)). 그 role보다 많은 권한이 필요한 호출은 거기서 실패합니다(`scripts/opensearch.sh`).
- 테스트는 세션을 직접 만들고 그 쿠키를 보냅니다. 내장 roster의 DB도 테스트마다 따로 둡니다.
- 개발용 PostgreSQL과 클러스터를 띄우는 법은 [PostgreSQL](postgres.md)과 [검색 엔진](search.md#개발용-클러스터)에 있습니다.

## 브라우저 테스트 (Playwright)

`ts/e2e` 는 주요 흐름을 빌드된 화면에서 사람처럼 따라갑니다. 화면의 것은 사람이 하듯 역할과 이름표로 찾습니다. 흐름이 지나는 화면을 고쳤다면 이 테스트가 통과해야 끝난 것입니다.

따라가는 흐름은 이렇습니다.

- **로그인:** 비밀번호, 그리고 roster의 발급자를 거치는 SSO. 워크스페이스 만들기와 전환, 닫기·내려받기·복구, 초대 보내기와 수락·거절, 약관 동의, 터미널 로그인 승인
- **자산:** 등록·검색·이동, CSV 내보내기, 라벨 인쇄와 코드 스캔, 카메라로 라벨 스캔, 대량 등록
- **지급·대여:** 지급·인수 확인·반납(부분 반납 포함, 자산 화면에서 시작하는 지급), 구성원이 라벨을 스캔해 하는 인수 확인, 데스크에서 대여한 카메라(대여 창이 겹치는 예약을 보여 주고, 먼저 한 예약은 나가 있는 자산으로 보이며, 그 시간의 새 예약은 거절됨)
- **예약:** 예약 승인, 예약 창의 이력(예약·승인과 사유·수령을 예약한 사람과 매니저가 읽고, 다른 사람은 거절됨), 대시보드와 자산 화면에서 여는 예약 창(대시보드의 승인 대기에서 승인), 키트 구성품 충돌, 키트 수령(예약이 잡은 구성품), 폐기하거나 실사에서 분실 처리한 구성품을 키트에서 빼기, 키트에 남긴 채 불용으로 바꾼 구성품이 예약한 사람의 알림·내 예약·예약 창에 건넬 수 없는 자산으로 보임, 불용인 물품의 예약 거절, 불용이 된 회의실의 예약이 쓸 수 없는 공간으로 보이고 체크인·새 예약을 거절함, 삭제한 그룹의 예약이 건넬 수 없는 자산으로 보이고 알림이 가며 수령을 거절함, 풀 수량 부족(구성원을 폐기하면 나중 예약이 기다림)과 풀 예약의 수령(미리 채운 구성원을 바꿔 건네면 다른 예약에는 남은 것만 채움)
- **현장:** 실사 스캔, 고장 신고, 구매와 입고, 재고 사용·입고
- **리포트:** 다섯 리포트와 그 CSV(같은 이름의 두 사람을 팀으로 가르고 오늘 지난 기한을 '오늘'로 말하는 대여 현황, 대시보드의 연체, 지급·대여 목록)
- **결제:** 결제 링크 페이지, 결제 계정 만들기와 결제창, 결제 계정의 이름(이름이 없는 둘을 가르기, 붙이기·바꾸기·지우기), 연체, 결제 담당 제안과 수락, 지불자 없음·연체·제한의 배너, 제한된 워크스페이스의 거절된 쓰기, 읽기 전용의 배너와 거절된 쓰기(그래도 되는 알림의 모두 읽음과 메일 끄기), 브라우저가 다른 시간대에 있어도 배포의 시계로 말하는 결제 화면, 결제할 수 없는 결제 계정(결제 화면과 소유자의 배너), 받는 사람과 사업자 정보(계좌이체의 사업자는 읽기만), 내가 결제 담당 맡기, 혜택과 크레딧(설정 › 결제의 추가 무료량·사용량 크레딧의 남은 양·줄마다 준 것과 뺀 것·워크스페이스의 크레딧·지난 명세서의 줄, 결제 화면의 크레딧과 잔액·청구서에 쓰인 크레딧), 청구서의 줄마다 그 명세서(휴대폰 너비에서도 창 안에서), 휴대폰 너비에서 옆으로 밀리지 않는 결제 화면과 설정 › 결제, 휴대폰에서 한 건씩 상자로 보이고 표를 밀지 않고 여는 청구서와 정정 청구
- **그 밖:** 콘텐츠 보안 정책(어느 화면이든 정책을 어기면 실패, `ts/e2e/loft.ts`), 오프라인에서 보관한 것과 로그아웃·다른 사람의 로그인, 접속기록 점검, 작업 기록과 감사 기록의 페이지 넘김, 워크스페이스 설정(시간대·통화·노쇼, 기록 보관), 워크스페이스 도메인

### 테스트가 띄우는 서버

서버는 테스트가 띄웁니다. `ts/e2e/serve.sh` 가 이 체크아웃의 loft를 빌드해 새 SQLite(아래처럼 주면 PostgreSQL)에 `init --demo` 를 하고 띄웁니다.

| 포트 | 무엇 |
| --- | --- |
| `127.0.0.1:18510` | 기본 서버 |
| `18520` | 약관 동의를 켠 서버 |
| `18530` | SSO를 켠 서버 |
| `18540` | SSO의 발급자. Go 테스트가 쓰는 `internal/oidctest` 이고, `ts/e2e/issuer.sh` 가 `internal/oidctest/cmd/oidctest` 로 띄웁니다 |
| `18550` | 과금하는 서버 |
| `18560`, `18561` | 그 서버가 청구하는 levyan. Go 테스트의 가짜(`internal/levyantest`)이고, `ts/e2e/levyan.sh` 가 `internal/levyantest/cmd/levyantest` 로 띄웁니다. 테스트는 `18561` 로 그 가짜에게 워크스페이스의 상태(지불자 없음, 제한, 읽기 전용, 연체)와 운영자가 준 것(추가 무료량, 사용량 크레딧, 크레딧, 잔액), 건넨 값, 닫힌 기간, 확정된 청구서와 그 정정 청구를 말합니다 |

- Paddle.js는 테스트의 가짜(`ts/e2e/paddle.ts`)이고, Paddle에는 아무 요청도 가지 않습니다.
- 설정은 그 스크립트들의 환경변수와 인자가 전부라서 `data/`, `loft.yaml`, 셸의 `LOFT_*` 는 쓰지 않습니다.
- 서버의 DB와 로그는 `ts/test-results/loft-<포트>/` 에 남고, 다음 실행이 지웁니다.
- 메일 서버를 정하지 않은 서버는 보낼 메일을 로그에 쓰므로, 초대 테스트는 그 로그에서 초대 링크를 읽습니다.
- 포트가 겹치면 `E2E_PORT=19510` 처럼 옮깁니다(그 포트와 +1, +10, +11, +20, +21, +30, +40, +41, +50, +51을 씁니다).

### 돌리기

```sh
cd ts
npm ci && npm run build                      # 테스트는 ts/dist를 씁니다
npx playwright install --with-deps chromium  # 처음 한 번
npx playwright test                          # 또는 npm run e2e
npx playwright show-trace test-results/<실패한 테스트>/trace.zip
```

브라우저를 설치하지 않고 Playwright 이미지에서 돌릴 수도 있습니다. 이미지에는 Go가 없으니 미리 빌드한 loft와 발급자와 levyan을 `E2E_LOFT`, `E2E_ISSUER`, `E2E_LEVYAN` 으로 넘기고, 태그의 버전은 `ts/package-lock.json` 이 설치하는 Playwright에서 읽습니다. 이미지의 브라우저는 그 버전이 다루는 브라우저라서, Playwright를 올리면 이미지도 따라 바뀝니다.

```sh
CGO_ENABLED=0 GOOS=linux go build -o /tmp/loft ./cmd/loft
CGO_ENABLED=0 GOOS=linux go build -o /tmp/oidctest ./internal/oidctest/cmd/oidctest
CGO_ENABLED=0 GOOS=linux go build -o /tmp/levyantest ./internal/levyantest/cmd/levyantest
v=$(jq -r '.packages["node_modules/playwright-core"].version' ts/package-lock.json)
docker run --rm --user "$(id -u):$(id -g)" -e HOME=/tmp \
  -v "$PWD:$PWD" -v /tmp/loft:/tmp/loft -v /tmp/oidctest:/tmp/oidctest -v /tmp/levyantest:/tmp/levyantest \
  -e E2E_LOFT=/tmp/loft -e E2E_ISSUER=/tmp/oidctest -e E2E_LEVYAN=/tmp/levyantest -w "$PWD/ts" \
  "mcr.microsoft.com/playwright:v$v-noble" npx playwright test
```

CI도 이렇게 돌립니다(`.github/workflows/ci.yaml` 의 `e2e`). 다만 러너와 같은 우분투의 이미지(`-resolute`)여서 loft를 러너에서 빌드한 그대로 넘기고, `--network host` 로 러너에 띄운 PostgreSQL과 OpenSearch에 닿습니다.

### PostgreSQL에서

`E2E_POSTGRES` 에 데이터베이스를 만들 수 있는 계정으로 그 서버의 주소를 줍니다. 서버마다 Loft와 roster의 데이터베이스를 하나씩(`e2e_loft_<포트>`, `e2e_roster_<포트>`) 그 서버에 두고, 실행할 때마다 지우고 새로 만듭니다(`ts/e2e/postgres.mjs`). CI는 SQLite와, `go` 잡과 같은 PostgreSQL(공식 `postgres:18`) 둘 다에서 돌립니다.

```sh
docker run -d --rm --name loft-e2e-pg -p 127.0.0.1:55432:5432 \
  -e POSTGRES_USER=loft -e POSTGRES_PASSWORD=loft -e POSTGRES_DB=loft postgres:18
E2E_POSTGRES='postgres://loft:loft@127.0.0.1:55432/loft?sslmode=disable' npx playwright test
docker stop loft-e2e-pg
```

Playwright 이미지로 돌릴 때는 앞의 Playwright `docker run` 에 `--network host` 와 `-e E2E_POSTGRES=...` 를 더해, 컨테이너 안에서도 그 주소가 닿게 합니다.

### OpenSearch에서

검색을 OpenSearch로 돌리려면 `E2E_OPENSEARCH` 에 클러스터 주소를 줍니다. TLS면 `https://사용자:비밀번호@…` 로 주고, CA는 `E2E_OPENSEARCH_CA` 에 줍니다. 서버마다 자기 인덱스(`e2e-<포트>-…`)를 쓰고, 뜨기 전에 지운 뒤 자기 DB로 다시 만듭니다. CI의 `e2e (sqlite, opensearch)` 가 이렇게 돕니다(`scripts/opensearch.sh`). 개발용 클러스터는 [검색 엔진](search.md#개발용-클러스터)에 있습니다.

```sh
E2E_OPENSEARCH=http://127.0.0.1:9200 npx playwright test
```
