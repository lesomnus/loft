# Rove

물리 자산과 공간을 관리하는 멀티 테넌트 서비스입니다. 무엇이 어디에 있었고 누가 가지고 있었는지를 **시간을 따라** 기록하고, 공간과 장비의 예약, 지급·대여, 재고, 실사, 정비, 구매를 한곳에서 다룹니다.

이 브랜치(`prototype`)는 파일럿을 위한 **localhost 프로토타입**입니다. 설계는 [`design.md`](design.md), 개발 계획은 [`plan.md`](plan.md), 프로토타입을 만들며 내린 주요 결정은 [`report.md`](report.md)에 있습니다.

> 이 저장소는 공개되어 있지만 오픈소스가 아닙니다. [`LICENSE`](LICENSE)를 보세요.

## 바로 실행하기

필요한 것: Go 1.27, Node 22+ (UI를 빌드할 때만). 데이터베이스는 기본이 SQLite 파일이라 따로 설치할 것이 없습니다.

```sh
# UI 빌드 (한 번)
cd ts && npm install && npm run build && cd ..

# 첫 워크스페이스와 소유자, 그리고 체험용 데이터(5개 팀 50명, 공간, 자산 140여 개)
go run ./cmd/rove init --tenant rove --name "우리 회사" --login admin --password admin1234 --demo

# 서버
go run ./cmd/rove serve
```

<http://localhost:8080> 에서 `admin` / `admin1234` 로 로그인합니다. `--demo` 로 만든 사람들은 모두 비밀번호 `demo1234` 로 로그인할 수 있고, `init` 이 역할별 예시 아이디를 출력합니다 (예: 매니저 `minjun-kim`, 관리자 `gunwoo-lee`, 감사자 `naeun-park`, 구성원 `haeun-lee`).

체험용 데이터 없이 실제로 쓰려면 `--demo` 를 빼고, `--password` 를 빼면 비밀번호를 만들어 출력합니다. 처음부터 다시 하려면 `data/` 디렉터리를 지웁니다.

### 계정과 워크스페이스 (roster)

**계정**은 [roster](https://github.com/lesomnus/roster)의 것이고, **워크스페이스**는 Rove의 것입니다(design 9.10, D27). 계정과 비밀번호, 로그인할 수 있는지는 roster가 정하고, 계정 하나가 여러 워크스페이스에 속할 수 있습니다. 워크스페이스마다의 역할은 Rove의 사람 화면에서 정합니다. 기본은 roster를 Rove 프로세스 안에서 함께 띄우는 **내장** 모드라 따로 설치할 것이 없습니다. roster의 DB는 Rove의 SQLite 파일 옆 `data/roster.db` 이고, 계정은 그 안의 디렉터리 `rove`에 있습니다.

- 처음 로그인한 계정이 어느 워크스페이스에도 없으면 자기 워크스페이스가 하나 생깁니다(가입). 워크스페이스는 계정마다 3개까지 더 만들 수 있고(`app.workspaces.owned`), 그 이상은 결제가 필요합니다. 이름은 직접 정하고 주소(alias)는 무작위로 정해집니다.
- 사람 화면의 로그인 발급은 내장 roster에 계정을 만들고, 그 워크스페이스의 구성원으로 넣습니다. 비밀번호 재설정은 그 계정이 이 워크스페이스에만 있을 때 할 수 있습니다(다른 워크스페이스의 사람을 이 워크스페이스의 관리자가 막을 수는 없으니까요).
- **로그인 중지**와 **개인정보 삭제**는 그 워크스페이스의 구성원 자격만 끝냅니다. 계정은 그 사람의 것이라 남습니다.
- 화면 없이 셸에서 할 때(예: 소유자가 비밀번호를 잊었을 때):

  ```sh
  go run ./cmd/rove holder add --tenant rove --login minsu --name 김민수 --role member   # 계정이 없으면 만들고 비밀번호를 한 번 출력
  go run ./cmd/rove holder password --login admin   # 계정에 새 비밀번호를 한 번 출력, 로그인된 곳은 모두 끝남
  ```

- 로그인은 아이디나 이메일로 하고, 어느 워크스페이스인지는 묻지 않습니다. 이메일은 내장 roster면 사람 기록의 이메일이고, 외부 roster면 roster에서 확인된 주소입니다. 2단계 인증을 켠 사람은 비밀번호만으로 들어올 수 없고, SSO로 들어옵니다.
- 세션은 계정의 것이고, 호출마다 어느 워크스페이스인지를 `rove-workspace` 헤더로 밝힙니다(화면은 `/@<워크스페이스>` 주소에서 자동으로). 워크스페이스가 하나뿐인 계정은 밝히지 않아도 됩니다.
- 로그인한 사람이 roster에서 중지·삭제되거나 "모든 곳에서 로그아웃"되면 Rove의 세션도 30초 안에 끝납니다. roster에 물을 수 없으면 로그인과 세션이 거부됩니다(503).

**roster를 따로 운영할 때**는 `auth.roster.addr` 에 주소를, `auth.roster.key` 에 키를, `auth.roster.directory` 에 계정이 있는 roster 테넌트를 줍니다. SaaS의 Rove는 roster 운영자가 만든 배포 키(`rk_…`)를 쓰고, 계정은 모두 그 디렉터리 하나에 있습니다. 한 조직이 직접 운영하는 Rove는 그 조직의 키(`rt_…`)를 쓰고, 디렉터리는 그 키의 테넌트입니다. 이때 `rove init`, `rove holder`, 사람 화면의 로그인 발급·비밀번호 변경은 거부되고 roster에서 합니다.

```sh
# roster 운영자: 배포 키를 만들고(rk_… 출력), 디렉터리에 Rove를 설치
roster control key add --allow /roster.NominationService/List rove
roster tenant add @accounts '{"name": "Rove accounts"}'
roster app install --tenant accounts --role /roster.VouchService/Verify,/roster.HolderService/Get,/roster.TenantService/Get,/roster.MeService/Get,/roster.EmailService/List,/roster.EmailService/Get rove

# Rove
ROVE_AUTH_ROSTER_ADDR=roster.example.com:443 ROVE_AUTH_ROSTER_DIRECTORY=accounts \
ROVE_AUTH_ROSTER_KEY=env:ROSTER_KEY ROSTER_KEY=rk_… go run ./cmd/rove serve
```

`EmailService`의 두 권한은 초대(D30)를 위한 것입니다. 초대장은 roster가 확인한 주소를 가진 계정의 초대함에 보이고, Rove는 그 주소를 이 권한으로 읽습니다. 이 권한이 없던 때 설치한 배포는 위의 `app install`을 다시 실행하세요. 다시 설치하기 전까지는 초대 메일의 링크로 받은 초대장만 초대함에 보입니다.

**SSO로 로그인할 때**(SaaS, D28)는 roster의 issuer(Hydra와 Login App)를 거칩니다. 로그인 화면에 Google·Microsoft·GitHub 버튼이 생기고, 누르면 roster를 잠깐 지나 그 IdP에서 로그인한 뒤 돌아옵니다. 돌아온 계정은 비밀번호로 들어온 계정과 같고, 처음이면 개인 워크스페이스가 생깁니다. 로그아웃은 issuer의 세션까지 끝냅니다.

```yaml
auth:
  sso:
    issuer: https://id.rove.example      # roster 앞의 Hydra
    client_id: rove
    client_secret: env:ROVE_SSO_SECRET
    providers:                           # 버튼: roster 디렉터리의 연결(Connection) 이름
      - { connection: google, label: Google, mark: google }
      - { connection: microsoft, label: Microsoft, mark: microsoft }
      - { connection: github, label: GitHub, mark: github }
  sso_only: true                         # 비밀번호 로그인을 끈다
```

roster 쪽에 필요한 것:

- **Hydra 클라이언트**: `token_endpoint_auth_method: client_secret_basic`, `redirect_uris: ["<app_url>/sso/callback"]`, `post_logout_redirect_uris: ["<app_url>/"]`, `scope: openid`.
- **디렉터리에 앱 호스트의 `Host` 행**: Login App은 돌아갈 주소의 호스트로 디렉터리를 찾습니다(`roster host add`에 `{"tenant": {"alias": "accounts"}, "name": "rove.example"}`).
- **디렉터리의 연결** `google`·`microsoft`·`github`: roster `docs/operating.md`의 *GitHub*, *Any Microsoft account*. 버튼이 이름 붙인 연결로 Login App이 바로 보내는 것은 roster#103부터이고, 디렉터리에 없는 이름이면 roster의 로그인 화면이 나옵니다.
- **누구나 가입**하게 하려면 Login App의 `login.enrol: enrolling`.

**예전 배포**(roster 이전에 만든 `data/`)는 로그인이 Rove의 DB에만 있어서, 업그레이드한 뒤에는 그대로 로그인할 수 없습니다. 한 번 옮깁니다.

```sh
go run ./cmd/rove identity migrate                       # 사람마다 로그인과 새 비밀번호를 한 번 출력
go run ./cmd/rove identity migrate --password demo1234   # 체험용 배포라면: 모두 같은 비밀번호로
```

로그인마다 같은 ID로 내장 roster에 계정이 생기므로 이력에 남은 행위자는 그대로입니다. 두 워크스페이스에 같은 아이디가 있었다면 하나는 `kim-beta`처럼 워크스페이스 이름이 붙습니다(출력에 나옵니다). 예전 비밀번호는 옮기지 않고 새로 발급하며, 예전 세션은 끝나고, 비밀번호를 담던 `credential` 테이블은 지웁니다. 다시 돌려도 이미 옮긴 사람은 건드리지 않습니다. roster를 따로 운영하면 대신 roster 운영자가 실행할 명령(같은 ID로 계정 만들기)을 출력하고, 운영자가 만든 뒤 다시 돌리면 연결합니다.

### UI 개발

```sh
go run ./cmd/rove serve          # :8080
cd ts && npm run dev             # :5173, API 호출은 Vite가 :8080으로 넘김
```

스캔한 라벨이 개발 서버로 열리게 하려면 `ROVE_APP_APP_URL=http://localhost:5173` 을 주고 서버를 띄웁니다.

### PostgreSQL로 실행

배포 데이터베이스는 PostgreSQL입니다. SQLite에는 없는 안전장치(예약·위치가 겹치면 DB가 거부하는 EXCLUDE 제약 등)가 `serve` 의 마이그레이션 때 함께 설치됩니다.

내장 roster도 같은 서버의 다른 데이터베이스를 씁니다.

```sh
docker run -d --name rove-pg -e POSTGRES_USER=rove -e POSTGRES_PASSWORD=rove -p 5432:5432 postgres:18
docker exec rove-pg createdb -U rove roster
export ROVE_DB_DRIVER=pgx ROVE_DB_DSN='postgres://rove:rove@localhost:5432/rove?sslmode=disable'
export ROVE_AUTH_ROSTER_DB_DRIVER=pgx ROVE_AUTH_ROSTER_DB_DSN='postgres://rove:rove@localhost:5432/roster?sslmode=disable'
go run ./cmd/rove init --demo
go run ./cmd/rove serve
```

설정할 수 있는 모든 값은 `go run ./cmd/rove config env` 로 볼 수 있습니다. 기본값은 [`rove.yaml`](rove.yaml)에 있습니다.

### 휴대폰으로 스캔하려면

브라우저는 **HTTPS 이거나 localhost 일 때만** 카메라를 열어 줍니다. 그래서 PC의 `http://localhost:8080` 은 그 PC에서만 카메라 스캔이 됩니다.

- 같은 PC에서 USB/블루투스 바코드 스캐너를 쓰면 스캔 화면의 입력칸에 그대로 입력됩니다 (QR 주소와 태그 모두 읽습니다).
- 휴대폰에서 쓰려면 서버를 HTTPS로 열어야 합니다. 예를 들어 [Caddy](https://caddyserver.com)를 앞에 두고(`reverse_proxy localhost:8080`, 사내망이면 `tls internal`) `app.public_url` 과 `app.labels.suffix` 를 그 주소에 맞춥니다. 라벨 QR은 `https://<조직>.<suffix>/l/<코드>` 로 인쇄되므로, 휴대폰 기본 카메라로 찍어도 바로 그 자산이 열립니다.

### 조직별 이력 보존 (운영자)

조직마다 이력을 얼마나 뒤까지 보여 주고 얼마나 보관할지는 그 조직의 **계약**이 정합니다(design 8). 계약과 법적 보존(hold)은 운영자의 것이라 셸에서만 씁니다. 조직의 사람은 읽기만 합니다.

```sh
# 내년 1월부터: 1년치를 보여 주고 2년치를 보관
go run ./cmd/rove contract set --tenant rove --name free --view 365 --keep 730 --effective 2027-01-01
# 줄어드는 보관 기간은 유예(--grace, 일)가 지나야 적용됩니다. 보기 기간은 바로 줄어듭니다
go run ./cmd/rove contract set --tenant rove --name trial --view 90 --keep 180 --grace 30
go run ./cmd/rove contract show --tenant rove   # 계약들과 지금 적용되는 기간, 걸린 hold

go run ./cmd/rove hold place --tenant rove --why "사건 2026-1"   # 이력을 아무것도 지우지 않음
go run ./cmd/rove hold lift <hold-id>
```

보기 기간은 바로 적용됩니다: 자산 이력, 시점 조회·비교, 작업 기록, 감사 기록, 끝난 예약·지급·작업·실사·재고 내역이 그 기간 안의 것만 답합니다. 아직 끝나지 않은 것(돌려받지 않은 대여 등)은 아무리 오래되어도 보입니다.

**지우는 일은 기본으로 꺼져 있습니다.** 고지·내보내기·유예·백업 정책이 정해지기 전에는 파괴적인 동작을 켜지 않는다는 원칙(design 1장) 때문입니다. 계약이 없는 조직은 `app.retention.view`·`keep` 을 따르고, 그것도 없으면 전부 보여 주고 전부 보관합니다.

```sh
go run ./cmd/rove retention plan                 # 지금 지우면 무엇이 몇 건 사라지는지 (아무것도 바꾸지 않음)
go run ./cmd/rove retention plan --tenant rove   # 한 조직만
go run ./cmd/rove retention run                  # 실제로 지움 — app.retention.apply 가 켜져 있을 때만
```

`plan` 은 삭제를 트랜잭션 안에서 실제로 해 보고 되돌리므로, 말하는 숫자가 `run` 이 지우는 숫자와 같습니다. `app.retention.apply: true` 이면 `serve` 가 `app.retention.every`(기본 1시간)마다 같은 일을 하고, 감사 기록의 이력 종류도 같은 기간으로 지웁니다. 지운 뒤에는 무엇을 몇 건, 어느 날짜 이전 것을 지웠는지만 적은 기록(`retention.expired` 작업 기록)이 남습니다. 지워지는 것은 보관 기간이 시작되기 전에 **끝난** 이력입니다: 끝났거나 정정된 위치·담당·관계 기록, 그 뒤에 바뀐 속성 값, 아무 기록도 가리키지 않는 작업 기록, 끝난 예약·다 돌려받은 지급·닫힌 실사·끝난 작업, 재고 이동. 기간이 시작될 때의 상태, 아직 끝나지 않은 것, 등록(취득) 기록과 폐기·불용·분실 상태는 남습니다. hold가 걸린 조직은 아무것도 지우지 않습니다.

### 조직 탈퇴 (운영자)

설계 8.2의 순서대로 **내보내기 → 유예 → 전체 삭제**입니다. 순서는 운영자가 지킵니다.

```sh
go run ./cmd/rove tenant export --tenant rove --out rove.zip   # 모든 행(종류마다 JSON 줄), 감사 기록, 첨부 파일
go run ./cmd/rove tenant purge --tenant rove                   # 무엇이 몇 건 사라지는지만 (아무것도 바꾸지 않음)
go run ./cmd/rove tenant purge --tenant rove --yes             # 실제로 지움: 모든 행, 파일, 감사 기록(DB와 보관소), 마지막으로 조직
```

내보내기에는 세션과 운영자의 hold는 들어가지 않고, 무엇을 왜 뺐는지가 `manifest.json`에 적힙니다. 로그인과 비밀번호는 roster의 것이라 Rove의 내보내기에 없습니다. hold가 걸린 조직은 지우지 않습니다. 사람 한 명의 개인정보 삭제는 사람 화면의 '개인정보 삭제'(가명화)이며, 감사 기록의 사본도 DB와 보관소에서 함께 비웁니다(hold가 걸린 것은 남김).

내장 roster면 `purge --yes` 가 그 워크스페이스에만 있던 계정을 roster에서도 먼저 파기합니다. 다른 워크스페이스에도 있는 계정은 그 사람의 것이라 남깁니다. roster를 따로 운영하면 계정은 roster의 것이고 Rove는 건드리지 않습니다.

### 감사 기록 보관소 확인 (운영자)

`audit.archive` 를 두면 DB에서 나간 감사 기록이 보관소에 쌓이고, 보관소에 무엇이 있어야 하는지는 DB가 따로 기록합니다(payday의 manifest). 감사 기록의 보존 패스는 시작할 때마다 둘을 가볍게 맞춰 보고, 끝날 때 체크포인트를 남깁니다.

```sh
go run ./cmd/rove trail verify          # 보관소를 DB의 기록·최신 체크포인트와 비교 (아무것도 바꾸지 않음)
go run ./cmd/rove trail verify --full   # 모든 청크를 다 읽어 내용이 바뀌지 않았는지까지
go run ./cmd/rove trail accept --why "백업에서 복원한 청크를 확인함"   # 살펴본 뒤, 보관소가 맞다고 DB의 기록을 맞춤
```

`verify` 는 무언가 찾으면 실패로 끝나므로 일정에 걸어 둘 수 있습니다. 체크포인트는 삭제를 거부하는 디스크(`audit.checkpoints.dir`)에 따로 두고, `audit.checkpoints.key` 로 서명하는 것을 권합니다. 키는 파일 경로로 주거나 `ROVE_AUDIT_CHECKPOINTS_KEY` 에 PEM 문서 자체를 넣습니다. 조직 내보내기의 감사 기록도 DB가 기록해 둔 청크만, 내용이 그대로인 것만 읽습니다.

## 무엇이 있나

| 화면 | 할 수 있는 일 |
| --- | --- |
| 대시보드 | 현황 숫자, 내 자산·예약, 승인 대기, 연체, 재고 부족, 알림 |
| 자산 | 검색·필터, 등록, 엑셀/CSV 가져오기(미리 보기)·내보내기, 라벨 인쇄 |
| 자산 상세 | 이동(지난 일은 그 시점으로), 정보·상태 변경, 소유·관리 담당, 키트·그룹 연결, 지급, 고장 신고, 첨부, 라벨, **이력과 정정**, 플러그인(랙 배치도, 보증) |
| 공간 | 공간 트리, **과거 어느 시점의 모습**, 두 시점 사이 변화 |
| 예약 | 주간 캘린더, 반복 예약, 임시 홀드, 승인, 체크인, 노쇼 자동 해제, 장비 수령 |
| 지급·대여 | 지급/대여, 인수 확인, 부분 반납, 기한 연장, 연체 알림 |
| 재고 | 입고·사용·조정·이동, 자산으로 전환, 부족 알림, 내역 |
| 실사 | 범위 지정, 카메라/스캐너로 스캔(오프라인이면 모아 두었다 전송), 대조, 위치 반영·분실 처리 |
| 작업 | 수리·점검·정비, 예약 막기, 정기 점검 반복, 구성원의 고장 신고 |
| 구매 | 주문, 입고 시 자산이나 재고로 등록 |
| 사람 | 조직도, 역할, 로그인 중지, 개인정보 삭제, 로그인 발급·비밀번호 재설정(내장 roster일 때) |
| 설정 | 유형과 속성, 모델, 예약 자원, 라벨 도메인, 작업 기록, 감사 기록, 사용량 |

역할: **소유자 · 관리자 · 매니저 · 구성원 · 감사자**. 무엇을 누가 할 수 있는지는 [`server/policy/policy.go`](server/policy/policy.go) 의 표 하나가 정하고, 표에 없는 것은 거부됩니다.

## 구조

```
proto/rove/*.proto          엔터티 (스키마의 원천)
proto/ext/rove/*.ext.proto  엔터티에 더한 RPC
server/domain/              도메인 계층: 시간 기록, 예약 충돌, 지급, 재고, 실사 … 그리고 백그라운드 작업
server/policy/              역할 → 호출할 수 있는 RPC
server/session/             로그인(roster에 묻는다)과 DB에 두는 세션
server/tenancy/             워크스페이스가 처음 갖는 것과, 워크스페이스를 만드는 일
internal/identity/          roster: 따로 운영하는 것(gRPC) 또는 프로세스 안의 것(bufconn)
server/storage/             첨부 파일과 서명된 다운로드 주소
cmd/, cli/                  서버 조립, init, serve
ts/src/                     React UI
```

대부분은 [payday](https://github.com/lesomnus/payday)가 `proto/`에서 생성합니다. 스키마를 고쳤다면:

```sh
go tool pd gen .          # Go
go tool pd gen --ts .     # TypeScript
go tool pd gen --check .  # 생성물이 스키마와 같은지 (CI가 하는 일)
```

생성 파일(`*.g.go`, `*.pb.go`, `server/bare/`, `server/pd/`, `internal/ent/`, `ts/gen/`)은 직접 고치지 않습니다. 자세한 규칙은 [`CLAUDE.md`](CLAUDE.md).

## 테스트

```sh
go test ./...                                                    # SQLite
PDTEST_POSTGRES='postgres://rove:rove@localhost:5432/rove?sslmode=disable' go test ./...   # PostgreSQL
cd ts && npm run check                                           # 타입 검사
```

도메인 테스트는 실제 호출과 같은 경로(게이트, 테넌트 벽, 도메인 계층)를 지나고, `cmd` 테스트는 gRPC 체인(세션 쿠키, 역할 표, 배치) 전체를 지납니다.
