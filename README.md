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

체험용 데이터 없이 실제로 쓰려면 `--demo` 를 빼고, `--password` 를 빼면 비밀번호를 만들어 출력합니다. 소유자의 이름은 `--person`(기본 `관리자`)으로 정하고, 사람 기록과 roster 계정에 같은 이름이 들어갑니다. 그래서 그 계정이 나중에 화면에서 만드는 워크스페이스에서도 그 이름으로 불립니다. 처음부터 다시 하려면 `data/` 디렉터리를 지웁니다.

### 계정과 워크스페이스 (roster)

**계정**은 [roster](https://github.com/lesomnus/roster)의 것이고, **워크스페이스**는 Rove의 것입니다(design 9.10, D27). 계정과 비밀번호, 로그인할 수 있는지는 roster가 정하고, 계정 하나가 여러 워크스페이스에 속할 수 있습니다. 워크스페이스마다의 역할은 Rove의 사람 화면에서 정합니다. 기본은 roster를 Rove 프로세스 안에서 함께 띄우는 **내장** 모드라 따로 설치할 것이 없습니다. roster의 DB는 Rove의 SQLite 파일 옆 `data/roster.db` 이고, 계정은 그 안의 디렉터리 `rove`에 있습니다.

- **약관 동의**(design 9.11): `app.consent.terms`와 `app.consent.privacy`에 버전과 문서 주소를 주면, 계정은 처음 들어올 때(그리고 버전이 바뀔 때마다) 동의 화면을 봅니다. 동의하기 전에는 모든 호출이 거부되고, 터미널에는 `rove consent`(무엇에 동의해야 하는지와 주소)와 `rove consent --agree`가 있습니다. 동의는 문서·버전·시각·화면/터미널로 계정마다 기록되고(내 정보 › 동의한 문서), 개인 워크스페이스는 동의한 뒤에 생깁니다. 동의하지 않아도 자기 워크스페이스의 데이터는 내려받을 수 있습니다. 문서 내용은 운영자가 따로 게시합니다. 버전을 비워 두면(기본값) 아무것도 묻지 않습니다. 계정이 탈퇴하면 그 동의 기록에 탈퇴 시각이 남고, `app.consent.keep`(예: `26280h`)이 지나면 지워집니다. 비워 두면(기본값) 지우지 않습니다. 기간은 법률 검토 뒤에 정합니다.
- 처음 로그인한 계정이 어느 워크스페이스에도 없으면 자기 워크스페이스가 하나 생깁니다(가입). 워크스페이스는 계정마다 3개까지 더 만들 수 있고(`app.workspaces.owned`), 그 이상은 결제가 필요합니다(닫은 워크스페이스는 세지 않습니다). 이름은 직접 정하고, 주소(`/@…`)는 영문 소문자와 숫자 7자로 무작위로 정해집니다. 헷갈리는 i·l·o·0·1은 쓰지 않고 첫 글자는 영문입니다. 7자 주소가 붐비면 저절로 8자가 됩니다(`app.workspaces.alias_length`).
- 주소는 사용자가 바꿀 수 없습니다. 운영자가 `rove tenant alias --tenant <지금 주소> --to <새 주소> --yes`로 바꾸면, 이전 주소는 1년 동안 아무도 쓰지 못하게 보류되고 그동안 새 주소로 넘겨 줍니다. 예약어(`www`, `api`, `admin`, `l`, `sso`, `login` 등, `app.workspaces.reserved`로 더함)는 쓸 수 없습니다.
- **워크스페이스 닫기**(D29): 소유자는 설정 › 워크스페이스에서 이름을 입력해 닫습니다. 닫으면 소유자 말고는 아무도 들어오지 못하고, 소유자는 30일(`app.workspaces.restore_within`) 안에 복구하거나 모든 데이터를 zip으로 내려받을 수 있습니다. 그 뒤에는 모든 데이터·파일·trail이 지워지고(법적 보존이 걸려 있으면 풀릴 때까지 기다림), 주소는 1년(`app.workspaces.alias_hold`) 동안 보류됩니다. 계정 탈퇴로 구성원이 아무도 남지 않은 워크스페이스는 저절로 닫힙니다. 운영자는 `rove tenant close|restore --tenant <주소> --yes`로 대신 닫고 엽니다.
- 사람 화면의 로그인 발급은 내장 roster에 계정을 만들고, 그 워크스페이스의 구성원으로 넣습니다. 비밀번호 재설정은 그 계정이 이 워크스페이스에만 있을 때 할 수 있습니다(다른 워크스페이스의 사람을 이 워크스페이스의 관리자가 막을 수는 없으니까요).
- **로그인 중지**와 **개인정보 삭제**는 그 워크스페이스의 구성원 자격만 끝냅니다. 계정은 그 사람의 것이라 남습니다. 개인정보 삭제는 사람 기록을 가명화하고(이름 `익명 …`, 연락처 삭제), 구성원 자격의 이름과 그 사람에게 보낸 초대의 주소, trail의 사본까지 지웁니다. 작업 기록과 자산 이력은 사람을 이름이 아니라 ID로 기억하고 읽을 때 지금의 이름을 붙이므로, 그 사람은 이력에서도 가명으로 보입니다. 다만 2026-10-12 이전에 쓴 작업 기록은 쓴 그대로라 문장에 이름이 남아 있습니다.
- 화면 없이 셸에서 할 때(예: 소유자가 비밀번호를 잊었을 때):

  ```sh
  go run ./cmd/rove holder add --tenant rove --login minsu --name 김민수 --role member   # 계정이 없으면 만들고 비밀번호를 한 번 출력
  go run ./cmd/rove holder password --login admin   # 계정에 새 비밀번호를 한 번 출력, 로그인된 곳은 모두 끝남
  ```

- **초대**(D30): 워크스페이스의 소유자·관리자는 사람 화면의 **+ 초대**나 사람 상세의 **초대**로 이메일 주소를 초대합니다. 역할은 자기 역할까지 줄 수 있습니다. 받은 사람은 메일의 링크를 열고 로그인(계정이 없으면 가입)한 뒤 **받은 초대**(`/invitations`, 워크스페이스 전환기에도 있음)에서 수락하거나 거절합니다. roster가 그 주소를 확인된 것으로 갖고 있는 계정은 링크 없이도 초대함에서 봅니다. 기다리는 초대는 30일 뒤 만료되고 사람 화면의 **초대** 탭에서 취소할 수 있습니다.
- 메일은 `app.mail`(SMTP)로 보냅니다. 비워 두면 보내지 않고 메일 내용과 링크를 서버 로그에 남깁니다(`rove.yaml`의 예시 참고).
- **알림 메일**: 다음 알림은 앱과 함께 메일로도 갑니다. 승인 요청(승인하는 사람에게)과 그 결과(요청한 사람에게), 반납 연체(가진 사람과 관리하는 사람에게), 다가오는 정기 점검(관리하는 사람에게). 나머지 알림은 앱에서만 받습니다. 받는 주소는 roster가 확인한 계정의 주소이고, 내장 roster면 사람 기록의 이메일도 씁니다. 주소가 없으면 보내지 않습니다.
  - 각자 **내 정보**의 "알림을 메일로도 받기"에서 끕니다(`NotificationService.SetMail`, 터미널에서는 `rove api notification set-mail '{"off": true}'`). 꺼도 앱 알림은 그대로 옵니다.
  - 메일은 알림을 만든 작업이 끝난 뒤 1분마다 보냅니다. 되돌려진 작업의 알림은 보내지 않고, 보내지 못한 메일은 다음에 다시 시도하며 8번 실패하면 서버 로그에 남기고 포기합니다.
  - `app.mail`에 서버가 없으면 알림 메일은 쌓지도, 로그에 남기지도 않습니다(초대 메일만 로그에 남습니다).
- 로그인은 아이디나 이메일로 하고, 어느 워크스페이스인지는 묻지 않습니다. 이메일은 내장 roster면 사람 기록의 이메일이고, 외부 roster면 roster에서 확인된 주소입니다. 2단계 인증을 켠 사람은 비밀번호만으로 들어올 수 없고, SSO로 들어옵니다.
- 세션은 계정의 것이고, 호출마다 어느 워크스페이스인지를 `rove-workspace` 헤더로 밝힙니다(화면은 `/@<워크스페이스>` 주소에서 자동으로). 워크스페이스가 하나뿐인 계정은 밝히지 않아도 됩니다.
- 로그인한 사람이 roster에서 중지·삭제되거나 "모든 곳에서 로그아웃"되면 Rove의 세션도 끝납니다. 외부 roster면 roster가 말하는 즉시(roster의 동기화 스트림), 그렇지 않아도 30초 안에 끝납니다. roster에 물을 수 없으면 로그인과 세션이 거부됩니다(503).
- roster에서 삭제된 계정은 그때부터 속한 모든 워크스페이스에서 **떠나는 중**이 됩니다(`Holder.date_leaving`, 삭제된 시각). roster의 삭제에는 되살릴 수 있는 유예가 있고(`holder.forget_after`, 기본 30일), Rove도 그만큼 기다립니다(`app.accounts.forget_after`, roster와 같게 둡니다). 유예가 지나도 roster가 계정을 되살리지 않았으면 그 계정은 **모든 워크스페이스에서 떠납니다**. 워크스페이스마다 개인정보 삭제와 같은 일이 일어나고, 그 계정이 마지막 소유자였던 워크스페이스는 남은 사람 중 역할이 가장 높은 사람(관리자 > 매니저 > 구성원, 감사자는 제외), 같은 역할이면 먼저 들어온 사람이 소유자가 됩니다. 넘길 사람이 없으면 소유자 없이 남고 서버 로그에 남깁니다. 되살린 계정은 다음 확인(1시간마다, `app.accounts.every`) 때 원래대로 돌아옵니다.
- 바로 잊어 달라는 요청은 운영자가 처리합니다. `rove identity depart --member @<워크스페이스>/<로그인>`은 그 계정이 떠날 워크스페이스를 보여 주고, `--yes`를 붙이면 지금 모든 워크스페이스에서 떠나게 합니다. 내장 roster면 계정도 roster에서 파기합니다.
- roster의 동기화 스트림은 끊긴 사이의 일을 다시 보내지 않습니다. Rove가 듣고 있지 않을 때 삭제된 계정은 서버가 하루에 한 번(`app.accounts.reconcile`, 기본 24h, 0이나 음수면 끔) roster에 모든 계정을 다시 물어 찾아내고, 지금부터 떠나는 중으로 표시하고, 무엇을 표시했는지 서버 로그에 남깁니다. `rove identity reconcile`은 같은 일을 바로 하되 무엇을 할지 먼저 보여 주고, `--yes`를 붙이면 합니다. roster가 계정을 하나도 모른다고 답하면 디렉터리나 키가 잘못된 것으로 보고 아무것도 표시하지 않습니다.

**roster를 따로 운영할 때**는 `auth.roster.addr` 에 주소를, `auth.roster.key` 에 키를, `auth.roster.directory` 에 계정이 있는 roster 테넌트를 줍니다. SaaS의 Rove는 roster 운영자가 만든 배포 키(`rk_…`)를 쓰고, 계정은 모두 그 디렉터리 하나에 있습니다. 한 조직이 직접 운영하는 Rove는 그 조직의 키(`rt_…`)를 쓰고, 디렉터리는 그 키의 테넌트입니다. 이때 `rove init`, `rove holder`, 사람 화면의 로그인 발급·비밀번호 변경은 거부되고 roster에서 합니다.

```sh
# roster 운영자: 배포 키를 만들고(rk_… 출력), 디렉터리에 Rove를 설치
roster control key add --allow /roster.NominationService/List rove
roster tenant add @accounts '{"name": "Rove accounts"}'
roster app install --tenant accounts --role /roster.VouchService/Verify,/roster.HolderService/Get,/roster.TenantService/Get,/roster.MeService/Get,/roster.EmailService/List,/roster.EmailService/Get,/roster.SyncService/Watch rove

# Rove
ROVE_AUTH_ROSTER_ADDR=roster.example.com:443 ROVE_AUTH_ROSTER_DIRECTORY=accounts \
ROVE_AUTH_ROSTER_KEY=env:ROSTER_KEY ROSTER_KEY=rk_… go run ./cmd/rove serve
```

`EmailService`의 두 권한은 초대(D30)를 위한 것입니다. 초대장은 roster가 확인한 주소를 가진 계정의 초대함에 보이고, Rove는 그 주소를 이 권한으로 읽습니다. `SyncService/Watch`는 roster의 동기화 스트림입니다. roster가 계정을 중지·삭제하거나 모든 곳에서 로그아웃시키면 Rove가 그 자리에서 세션을 끝내고, 삭제된 계정을 떠나는 중으로 표시합니다. roster가 디렉터리를 닫으면(`roster tenant close`, roster#105) 그 디렉터리의 모든 계정의 세션이 끝나고, 닫혀 있는 동안은 아무도 로그인하지 못합니다. 아무것도 지우지 않으므로 디렉터리를 복구하면 다시 로그인할 수 있습니다.

`auth.roster.account_url` 에 roster의 계정 페이지(디렉터리의 front door, 예: `https://id.example.com/`)를 주면, 내 정보 화면에 "로그인 방법 관리"와 "계정 탈퇴" 링크가 생깁니다. 탈퇴 링크는 그 페이지의 `#leave` 로 가고, 계정 삭제는 roster에서 합니다(`MeService.Leave`, roster#106). 삭제는 디렉터리가 허용할 때만 되므로, SaaS의 디렉터리는 roster 운영자가 `TenantConfig.leave` 를 켭니다(관리 콘솔의 테넌트 화면, 또는 resources 파일의 `config.leave`). 삭제된 계정은 위의 스트림으로 Rove에 들어와 떠나는 중이 됩니다.

이 권한들이 없던 때 설치한 배포는 역할을 넓혀야 합니다. `app install`을 다시 실행해도 이미 있는 역할은 그대로 두므로(roster `docs/apps.md`), roster 운영자가 역할을 직접 고칩니다. 목록을 통째로 바꾸므로 전체를 줍니다.

```sh
roster role patch @accounts/rove-app '{"methods": ["/roster.VouchService/Verify", "/roster.HolderService/Get", "/roster.TenantService/Get", "/roster.MeService/Get", "/roster.EmailService/List", "/roster.EmailService/Get", "/roster.SyncService/Watch"], "date_updated_force": true}'
```

고치기 전까지는 초대 메일의 링크로 받은 초대장만 초대함에 보이고, 세션은 다음 호출에서(30초 안에) 끝나며, 삭제된 계정은 떠나는 중으로 표시되지 않습니다. 스트림을 거부당한 Rove는 서버 로그에 오류를 한 번 남기고 10분마다 다시 청하므로, 역할을 고친 뒤 다시 시작하지 않아도 됩니다. 조직의 키(`rt_…`)로 붙은 배포는 키가 허용한 메서드에도 들어 있어야 하므로 키를 새로 만듭니다.

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

### 터미널에서 쓰기 (CLI)

모든 기능은 API(RPC)이고, 같은 기능을 터미널에서도 씁니다. 터미널은 브라우저와 같은 주소로 접속하고, 로그인은 이미 로그인한 브라우저에서 승인해서 합니다(SSO만 쓰는 배포에서도 그대로 됩니다).

```sh
go run ./cmd/rove login --url http://localhost:8080   # 주소와 코드가 나오면, 로그인한 브라우저에서 열고 승인
go run ./cmd/rove whoami                              # 계정과 속한 워크스페이스
go run ./cmd/rove use acme                            # 기본 워크스페이스 (하나뿐이면 필요 없음, --workspace로 그때그때)
go run ./cmd/rove api asset ls -o json                # 엔터티마다 get·ls·watch·add·patch·erase
go run ./cmd/rove api asset move <자산> '{"to": {"id": "<공간>"}}'      # 그 밖의 작업도 엔터티 아래에
go run ./cmd/rove api call rove.AssetService.Search '{"q": "노트북"}'  # 아무 RPC나 이름으로
go run ./cmd/rove scan 7K3M-Q9TX                      # 라벨 코드·주소·태그가 가리키는 것 (내 다른 워크스페이스의 라벨도)
go run ./cmd/rove workspace export --out acme.zip acme   # 소유한 워크스페이스의 모든 데이터 (화면의 내려받기와 같음)
go run ./cmd/rove workspace new 연구실                 # 워크스페이스 만들기
go run ./cmd/rove logout
```

- 승인하면 그 계정의 세션이 터미널에 생깁니다. 브라우저와 같은 권한 표를 따르고, 유지 시간도 같으며, roster에서 계정이 중지·삭제되면 함께 끝납니다. 승인 화면은 요청한 터미널과 위치를 보여 줍니다. 내 터미널에서 방금 `rove login`을 실행했고 같은 코드가 보일 때만 승인하세요.
- 로그인 정보는 `~/.config/rove/client.json`(`ROVE_CLIENT_CONFIG`)에 주인만 읽을 수 있게 저장됩니다. 세션 키가 들어 있습니다.
- `rove api`는 서버의 설정을 읽지 않습니다. 서버 상자에서 쓰는 운영자 명령(`init`, `tenant`, `identity` 등)과는 다릅니다.

### UI 개발

```sh
go run ./cmd/rove serve          # :8080
cd ts && npm run dev             # :5173, API 호출은 Vite가 :8080으로 넘김
```

스캔한 라벨이 개발 서버로 열리게 하려면 `ROVE_APP_APP_URL=http://localhost:5173` 을 주고 서버를 띄웁니다.

오프라인으로 여는 것(서비스 워커)은 빌드한 UI(`npm run build`)를 서버가 줄 때만 동작합니다. `npm run dev` 에서는 등록하지 않습니다.

### PostgreSQL로 실행

배포 데이터베이스는 PostgreSQL입니다. SQLite에는 없는 안전장치(예약·위치가 겹치면 DB가 거부하는 EXCLUDE 제약 등)와 검색 인덱스가 `serve` 의 마이그레이션 때 함께 설치됩니다.

검색은 **pg_bigm** 확장이 있으면 두 글자부터("의자") 인덱스로 찾습니다. 공식 PostgreSQL 이미지에는 없으므로 [`deploy/postgres`](deploy/postgres/Dockerfile)의 이미지(공식 이미지 + pg_bigm)를 씁니다. pg_bigm이 없는 데이터베이스(Azure, 네이버 클라우드, NHN 클라우드 등)에서는 pg_trgm으로 세 글자부터 찾고, 그보다 짧으면 태그·이름의 앞부분으로 찾습니다. 서버 로그의 `search:` 줄이 어느 쪽인지 말해 줍니다. pg_bigm은 superuser만 설치할 수 있는 확장이라, Rove의 DB 계정이 superuser가 아니면 DB 관리자가 한 번 `CREATE EXTENSION pg_bigm` 을 실행합니다.

내장 roster도 같은 서버의 다른 데이터베이스를 씁니다.

```sh
docker build -t rove-postgres deploy/postgres
docker run -d --name rove-pg -e POSTGRES_USER=rove -e POSTGRES_PASSWORD=rove -p 5432:5432 rove-postgres
docker exec rove-pg createdb -U rove roster
export ROVE_DB_DRIVER=pgx ROVE_DB_DSN='postgres://rove:rove@localhost:5432/rove?sslmode=disable'
export ROVE_AUTH_ROSTER_DB_DRIVER=pgx ROVE_AUTH_ROSTER_DB_DSN='postgres://rove:rove@localhost:5432/roster?sslmode=disable'
go run ./cmd/rove init --demo
go run ./cmd/rove serve
```

#### 여러 대로 띄우려면

PostgreSQL이면 서버를 여러 대 띄울 수 있습니다. 상태는 데이터베이스에 있고, 다음만 맞추면 됩니다.

- `watch.broker: postgres`: 다른 서버에서 일어난 변경이 실시간 화면에 전해집니다(LISTEN/NOTIFY).
- `spin.lease: postgres`: 예약 만료·메일·탈퇴 처리·워크스페이스 파기 같은 백그라운드 작업을 한 대만 돌립니다(advisory lock, payday#54). 그 서버가 죽으면 다른 서버가 이어받습니다. 요청만 받는 서버는 `none`.
  - LISTEN과 세션 잠금은 트랜잭션 모드의 PgBouncer 같은 풀러를 거치면 안 됩니다. 풀러를 쓴다면 `watch.dsn`과 `spin.dsn`에 DB로 바로 가는 주소를 줍니다.
  - "정확히 한 번"은 아닙니다. 장애가 넘어가는 순간에는 두 서버가 잠깐 같은 작업을 할 수 있고, 작업은 그래도 괜찮게 짜여 있습니다.
- `app.signing_key`: 모든 서버에 같은 값을 줍니다. 비워 두면 서버마다 다른 키라, 다른 서버가 만든 다운로드 링크가 열리지 않습니다. 그래서 `spin.lease`가 `local`이 아니거나 `watch.broker`가 `memory`·`none`이 아닌데 키가 비어 있으면 `serve`가 시작하지 않습니다.
- 로그인 제한은 데이터베이스에서 세므로 서버가 몇 대든 한 번에 셉니다. 같은 주소에서 같은 아이디로 15분에 10번 틀리면 잠시 막고, 한 주소에서 터미널 로그인(`rove login`)은 10분에 20번까지입니다. 계정 잠금(연속 10번 틀리면 15분)은 roster가 합니다.
- 첨부 파일(`app.files`)은 아직 로컬 디렉터리뿐이라, 여러 서버가 같은 디렉터리를 공유해야 합니다(NFS 등).

설정할 수 있는 모든 값은 `go run ./cmd/rove config env` 로 볼 수 있습니다. 기본값은 [`rove.yaml`](rove.yaml)에 있습니다.

### 검색 엔진 (OpenSearch)

설정에 클러스터가 없으면 검색은 데이터베이스가 답합니다(위의 pg_bigm). `app.search.opensearch`로 클러스터를 주면 OpenSearch와 한국어 분석기 nori가 답합니다. nori는 "웹서버"를 웹 · 서버로, "의자를"을 의자로 나눠서 찾습니다(D31).

무엇을 입력했는지에 따라 이렇게 찾습니다.

- **낱말:** 붙여 쓴 합성어(웹서버)와 조사가 붙은 낱말(의자를)도 낱말로 찾습니다. 쳐 넣은 한 낱말은 그 조각을 모두 갖춘 것만 찾습니다. 그래서 없는 태그(E2E-ABC12)는 글자 몇 개가 겹치는 다른 자산을 끌어오지 않습니다.
- **두 낱말:** 둘 다 맞은 것을 먼저 보여 주고, 그다음은 드문 낱말이 맞은 것부터입니다. "GPU 서버"면 GPU서버, NVIDIA GPU, 웹서버 순입니다.
- **태그와 시리얼:** 통째로도, 일부로도 찾습니다.
- **이름:** 입력하는 중인 앞부분으로도, 한 글자 틀린 것으로도 찾습니다.

저장한 것은 그 응답이 오기 전에 색인에 들어갑니다. 저장하고 바로 검색해도 보입니다. 다른 사람이나 다른 프로세스가 바꾼 것은 1초쯤 늦을 수 있습니다.

아무것도 입력하지 않은 목록(필터만)은 그대로 데이터베이스가 답합니다. 클러스터가 답하지 않으면 검색은 "지금은 검색할 수 없습니다"로 답하고, 데이터베이스로 몰래 넘어가지 않습니다.

색인은 서버가 만듭니다. 처음 뜰 때 색인이 없거나, 색인의 모양이 바뀐 빌드가 뜨면 루프가 DB로 새로 만든 뒤 별칭을 옮깁니다. 그동안 검색은 옛 색인이 답하고, 옛 색인이 없으면 "지금은 검색할 수 없습니다"입니다.

운영 구성에서 켜는 법은 `deploy/compose/README.md`의 "검색을 켜려면"과 `deploy/k8s/README.md`의 9에 있습니다.

- **바뀐 것이 색인에 닿는 길:**
  - 자산, 유형, 모델, 사람이 바뀌면 같은 트랜잭션에 "다시 색인할 것"(`searchstale`)이 남습니다.
  - 배포에서 한 서버의 루프(`rove.search.index`)가 1초쯤 안에 그 문서를 DB의 지금 상태로 다시 만듭니다.
  - 개인정보 삭제로 바뀐 이름도, 지운 자산도, 파기한 워크스페이스도 이 길로 색인에서 사라집니다.
- **클러스터가 답하지 않으면** 바뀐 것은 큐에서 기다립니다. 로그에는 시작할 때, 이어지는 동안 1분에 한 번, 다시 될 때 남습니다.

- **이미지:** [`deploy/opensearch`](deploy/opensearch/Dockerfile)입니다. 공식 이미지에 nori를 넣었고, 쓰지 않는 플러그인은 뺐습니다. TLS와 인증을 맡는 security 플러그인은 남겼습니다.
- **설정:**

  ```yaml
  app:
    search:
      opensearch:
        addrs: [https://search-0:9200]   # 노드 여럿. 답하지 않는 노드는 건너뜁니다
        username: rove
        password: env:ROVE_SEARCH_PASSWORD   # file:/run/secrets/..., 또는 값 그대로
        ca_file: /etc/rove/opensearch-ca.pem
        index: rove   # 인덱스 이름의 앞부분. 별칭 rove-assets가 rove-assets-<시각>을 가리킵니다
  ```

  환경변수로는 `ROVE_APP_SEARCH_OPENSEARCH_ADDRS=https://search-0:9200,https://search-1:9200`처럼 씁니다. 한 클러스터를 여러 배포가 나눠 쓰면 `index`를 배포마다 다르게 줍니다.
- **Rove 계정의 role:** 클러스터 권한은 bulk 쓰기 하나이고, 인덱스 권한은 자기 이름(`<index>-*`)에만 줍니다. CI가 이 role로 테스트를 돌립니다.

  ```sh
  curl -u admin --cacert root-ca.pem -X PUT https://search-0:9200/_plugins/_security/api/roles/rove \
    -H 'Content-Type: application/json' \
    -d '{"cluster_permissions":["indices:data/write/bulk"],"index_permissions":[{"index_patterns":["rove-*"],"allowed_actions":["indices_all"]}]}'
  ```

- **명령:**

  ```sh
  rove search reindex          # 지금 다시 만들기: DB의 자산으로 새 인덱스를 다 채운 뒤, 별칭을 옮기고 옛 인덱스를 지움
  rove search status           # 별칭이 가리키는 인덱스, 문서 수와 DB의 자산 수
  rove search analyze 웹서버    # 그 글을 어떤 낱말로 나누는지: 색인할 때와 찾을 때
  rove search remove --yes     # 이 배포가 만든 인덱스와 별칭을 모두 지움
  ```

  - reindex 동안에도 별칭은 다 채워진 인덱스만 가리킵니다. 옛 인덱스에서 새 인덱스로 한 번에 넘어갑니다.
  - reindex는 한 번에 하나만 돌립니다. 둘째는 첫째가 쓰는 중인 인덱스를 지웁니다.
  - 인덱스 이름의 시각은 UTC입니다.
- **개발용 클러스터:** 보안 플러그인을 끄고 loopback에만 띄웁니다.

  ```sh
  docker build -t rove-opensearch deploy/opensearch
  docker run -d --name rove-os -p 127.0.0.1:9200:9200 -e discovery.type=single-node \
    -e DISABLE_SECURITY_PLUGIN=true -e DISABLE_INSTALL_DEMO_CONFIG=true \
    -e OPENSEARCH_JAVA_OPTS='-Xms512m -Xmx512m' rove-opensearch
  export ROVE_APP_SEARCH_OPENSEARCH_ADDRS=http://127.0.0.1:9200
  go run ./cmd/rove search reindex
  ```

### 운영에 띄우기 (운영자)

[`deploy/compose`](deploy/compose/README.md)가 한 대의 서버에 띄우는 운영 구성 전체입니다. PostgreSQL, 마이그레이션, `rove serve` 두 대, 운영자 진입점, 인증서를 받는 Caddy가 들어 있습니다. 운영자가 채울 것은 도메인과 비밀값(`.env`, `secrets/`)이고, 이미지는 저장소 루트의 `Dockerfile`로 짓습니다(`docker build -t rove .`).

운영에서는 서버가 테이블을 직접 바꾸지 않습니다(`db.migrate: false`). 새 빌드를 띄우기 전에 `rove migrate`로 테이블을 맞추고, 모양이 다르면 `serve`는 시작하지 않습니다.

### 휴대폰으로 스캔하려면

브라우저는 **HTTPS 이거나 localhost 일 때만** 카메라를 열어 줍니다. 그래서 PC의 `http://localhost:8080` 은 그 PC에서만 카메라 스캔이 됩니다.

- 같은 PC에서 USB/블루투스 바코드 스캐너를 쓰면 스캔 화면의 입력칸에 그대로 입력됩니다 (QR 주소와 태그 모두 읽습니다).
- 휴대폰에서 쓰려면 서버를 HTTPS로 열어야 합니다. 예를 들어 [Caddy](https://caddyserver.com)를 앞에 두고(`reverse_proxy localhost:8080`, 사내망이면 `tls internal`) `app.public_url` 을 그 주소에 맞춥니다. 라벨 QR은 `<public_url>/l/<코드>` 로 인쇄되므로(D28), 휴대폰 기본 카메라로 찍어도 바로 그 자산이 열립니다.
- 프록시를 앞에 두면 `app.trusted_proxies`에 프록시 주소를 적습니다(`[127.0.0.1]`이나 `[10.0.0.0/8]`처럼, 환경변수로는 `ROVE_APP_TRUSTED_PROXIES=10.0.0.0/8,127.0.0.1`). 적은 프록시가 보낸 `X-Forwarded-For`만 믿습니다. 비워 두면 모든 요청이 프록시에서 온 것으로 보입니다. 그러면 로그인 실패 제한이 프록시 뒤 모든 사람에게 하나로 묶여, 누군가 열 번 틀리면 그 이름으로는 아무도 로그인하지 못합니다. 프록시가 아닌 곳에서 온 `X-Forwarded-For`는 무시합니다. 호출자가 마음대로 쓸 수 있는 값이라서입니다.
- 라벨 코드는 8자(`7K3M-Q9TX`처럼 넷씩 끊어 라벨 아래에도 인쇄)이고 서비스 전체에서 하나뿐입니다. 카메라가 못 읽으면 스캔 화면에 그대로 입력하면 되고, 소문자나 `-` 없이 써도, `I`·`L`을 `1`로, `O`를 `0`으로 읽습니다. 다른 워크스페이스의 라벨을 스캔하면, 내가 그 워크스페이스의 구성원일 때 그곳으로 넘어가 열립니다. 구성원이 아니면 없는 라벨과 똑같이 "찾지 못함"입니다.
- 워크스페이스마다 자기 라벨 도메인을 쓰려면(Dedicated, D23) `app.labels.tenant_domains: true` 를 켭니다. 그러면 설정 › 라벨에서 도메인을 추가해야 라벨이 켜지고, `app.labels.suffix` 아래 기본 하위 도메인(`https://<워크스페이스>.<suffix>/l/<코드>`)이 새 워크스페이스에 생깁니다.

### 콘텐츠 보안 정책 (CSP)

모든 화면은 콘텐츠 보안 정책과 함께 나갑니다(`server/csp`). 페이지는 이 서버의 스크립트·스타일·이미지와 직접 그린 QR(`data:`), 이 서버의 API만 쓰고, 폼은 이 서버와 SSO issuer로만 보냅니다. 누가 페이지에 스크립트를 끼워 넣어도 브라우저가 실행하지 않게 하는 장치입니다.

지금은 `app.csp: report-only`(기본)라 막지는 않습니다. 브라우저가 정책을 어긴 것을 `/csp-report`로 보내면 서버 로그에 `csp: a page did what the policy does not allow`로 남깁니다. 로그는 1분에 30줄까지이고, 넘친 것은 다음 줄에 개수만 남습니다. 주소의 쿼리는 지웁니다(첨부 주소의 서명 등). 운영에서 이 로그가 조용한 것을 확인한 뒤 `enforce`로 바꾸면 막습니다. `off`는 둘 다 하지 않습니다. 다른 사이트가 화면을 틀에 넣는 것은 설정과 상관없이 늘 막습니다.

e2e는 어느 화면이든 정책을 어기면 실패합니다(`ts/e2e/rove.ts`).

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

### 운영자 진입점 (rove-admin)

운영자는 서버 상자의 셸 없이도 일할 수 있게 **운영자 진입점**(design 6장)을 따로 띄웁니다. 공개 바이너리(`rove`)에는 들어 있지 않은 별도 바이너리(`rove-admin`)이고, 운영자 네트워크(기본은 루프백, VPN 주소까지)에서만 닿는 포트로 듣습니다. 요청마다 운영자의 키가 필요하고, 서버 로그에 누가 무엇을 했는지 이름으로 남습니다.

```sh
# 서버 옆에서: 같은 설정(rove.yaml, ROVE_*)에 운영자와 키만 더함 (admin.addr 기본 127.0.0.1:8081)
export ALICE_KEY=$(openssl rand -hex 24)
ROVE_ADMIN_KEYS='{"alice": "env:ALICE_KEY"}' go run ./cmd/rove-admin serve

# 운영자의 컴퓨터에서: 진입점 주소와 자기 키 (--url, --key로 줘도 됨)
export ROVE_ADMIN_URL=http://127.0.0.1:8081 ROVE_ADMIN_KEY=env:ALICE_KEY
go run ./cmd/rove-admin workspace ls --q acme                     # 주소·이름의 일부로 찾기 (--closed: 닫힌 것만)
go run ./cmd/rove-admin workspace show acme                       # 구성원, 계약, hold
go run ./cmd/rove-admin workspace close --reason "요청" --yes acme # 닫기 (소유자는 복구 기간 안에 복구할 수 있음)
go run ./cmd/rove-admin workspace restore --yes acme
go run ./cmd/rove-admin workspace owner --member kim --yes acme    # 구성원을 소유자로 지정
go run ./cmd/rove-admin workspace alias --to acme-kr --yes acme    # 주소 바꾸기 (이전 주소는 보류되고 새 주소로 넘김)
go run ./cmd/rove-admin contract set --view 365 --keep 730 --yes acme
go run ./cmd/rove-admin hold place --why "사건 번호" --yes acme; go run ./cmd/rove-admin hold lift --yes <ID>
```

- 키는 32자 이상이어야 하고(`admin.keys.<이름>: env:…|file:…|키`), 키가 하나도 없으면 `serve`가 시작하지 않습니다. 플래그는 인자보다 앞에 씁니다.
- 리버스 프록시의 on-demand TLS 확인(`/internal/tls-ask`)도 이 진입점으로 옮겼습니다(키 없이 답함). 공개 서버에는 더 이상 없습니다.
- 셸의 운영자 명령(`rove tenant`, `rove contract`, `rove hold` 등)은 그대로 있습니다. 운영자 웹 화면은 나중입니다.

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

같은 키로 DB 기록의 행 하나하나에도 서명하고, 각 체크포인트는 바로 앞 체크포인트를 가리킵니다(payday#58). 그래서 마지막 체크포인트 뒤에 바뀐 행도 드러납니다. 작업은 매번 체크포인트로 끝납니다. 행이 최신 체크포인트와 맞지 않는 동안에는 `accept`할 때까지 체크포인트를 남기지 않고, 보관소를 건드리는 작업(보존 패스, 개인정보 삭제, 계정 탈퇴, 워크스페이스 파기)은 모두 오류로 끝납니다. 서버는 멈추지 않고 로그에 남깁니다. 그때 할 일은 [감사 기록 위변조 감지 대응](docs/runbook/trail-integrity.md)에 있습니다. DB를 백업에서 복원한 뒤에도 `verify`가 `behind`를 찾으며, 살펴본 뒤 `accept`를 한 번 실행하면 이어 갑니다(payday#70).

**키를 이미 쓰던 배포는 이 버전으로 올린 뒤 `accept`를 한 번 실행합니다.** 서명이 없는 행은 서명하기 전에 쓴 것인지, 누가 서명을 지운 것인지 구별할 수 없습니다. 그래서 키가 있으면 그런 행을 모두 찾아냅니다(`unsigned`). 살펴본 뒤 한 번 받아들이면 그다음부터는 깨끗합니다.

```sh
go run ./cmd/rove trail verify                                   # 그전에 쓴 행마다 `unsigned`
go run ./cmd/rove trail accept --why "행에 서명하기 전에 쓴 것"   # 한 번
go run ./cmd/rove trail verify                                   # 깨끗함
```

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
| 설정 | 유형과 속성, 모델, 예약 자원, 라벨(도메인은 `tenant_domains`일 때), 작업 기록, 감사 기록, 사용량 |

**오프라인.** 연결이 끊겨도 앱이 열리고, 이 브라우저에서 마지막으로 본 화면을 그대로 보여 줍니다(위에 '오프라인 — 마지막으로 본 내용'). 오프라인에서는 읽기만 하고 저장은 이유와 함께 거절하며, 실사 스캔만 모아 두었다가 연결되면 보냅니다. 본 내용은 계정과 워크스페이스별로 따로 보관하고(IndexedDB, `ts/src/offline.ts`), 로그아웃하거나 로그아웃된 것을 알게 되면 모두 지웁니다. 앱 자체는 서비스 워커(`ts/sw.js`)가 보관합니다.

**실시간.** 자산 상세, 예약 캘린더, 진행 중인 실사는 다른 사람이 바꾼 것이 새로고침 없이 바로 보입니다. 화면은 자기가 그리는 것을 대표하는 행(자산, 예약 자원의 `schedule_version`, 실사의 `findings_version`)을 Watch하다가 바뀌면 다시 읽습니다. 스트림이 끊기면 1초에서 30초까지 늘려 가며 다시 열고, Watch가 없는 배포(`watch.broker: none`)에서는 조용히 예전처럼 동작합니다.

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
PDTEST_POSTGRES='postgres://rove:rove@localhost:5432/rove?sslmode=disable' go test ./...   # PostgreSQL (deploy/postgres 이미지면 pg_bigm까지)
PDTEST_OPENSEARCH=http://127.0.0.1:9200 go test ./...           # 검색 색인까지 (deploy/opensearch; TLS면 https://user:pass@... 와 PDTEST_OPENSEARCH_CA)
cd ts && npm run check                                           # 타입 검사
```

도메인 테스트는 실제 호출과 같은 경로(게이트, 테넌트 벽, 도메인 계층)를 지나고, `cmd` 테스트는 gRPC 체인(세션 쿠키, 역할 표, 배치) 전체를 지납니다.

### 브라우저 테스트 (Playwright)

`ts/e2e`는 주요 흐름을 빌드된 화면에서 사람처럼 따라갑니다: 로그인(비밀번호, 그리고 roster의 발급자를 거치는 SSO), 자산 등록·검색·이동, 가져오기(csv, xlsx), 라벨 인쇄와 코드 스캔, 카메라로 라벨 스캔, 지급·인수 확인·반납(부분 반납 포함, 자산 화면에서 시작하는 지급), 구성원이 라벨을 스캔해 하는 인수 확인, 예약 승인, 실사 스캔, 고장 신고, 구매와 입고, 재고 사용·입고, 워크스페이스 만들기와 전환, 닫기·내려받기·복구, 초대 보내기와 수락·거절, 약관 동의, 터미널 로그인 승인.

서버는 테스트가 띄웁니다. `ts/e2e/serve.sh`가 이 체크아웃의 rove를 빌드해 새 SQLite(아래처럼 주면 PostgreSQL)에 `init --demo`를 하고 `127.0.0.1:18510`에서, 약관 동의를 켠 두 번째 서버는 `18520`에서, SSO를 켠 세 번째 서버는 `18530`에서 띄웁니다. SSO의 발급자는 Go 테스트가 쓰는 `internal/oidctest`이고, `ts/e2e/issuer.sh`가 `internal/oidctest/cmd/oidctest`로 `18540`에서 띄웁니다. 설정은 그 스크립트들의 환경변수와 인자가 전부라서 `data/`, `rove.yaml`, 셸의 `ROVE_*`는 쓰지 않습니다. 서버의 DB와 로그는 `ts/test-results/rove-<포트>/`에 남고, 다음 실행이 지웁니다. 메일 서버를 정하지 않은 서버는 보낼 메일을 로그에 쓰므로, 초대 테스트는 그 로그에서 초대 링크를 읽습니다.

```sh
cd ts
npm ci && npm run build                      # 테스트는 ts/dist를 씁니다
npx playwright install --with-deps chromium  # 처음 한 번
npx playwright test                          # 또는 npm run e2e
npx playwright show-trace test-results/<실패한 테스트>/trace.zip
```

브라우저를 설치하지 않고 Playwright 이미지에서 돌릴 수도 있습니다. 이미지에는 Go가 없으니 미리 빌드한 rove와 발급자를 `E2E_ROVE`, `E2E_ISSUER`로 넘기고, 태그는 `ts/package.json`의 `@playwright/test` 버전과 맞춥니다.

```sh
CGO_ENABLED=0 GOOS=linux go build -o /tmp/rove ./cmd/rove
CGO_ENABLED=0 GOOS=linux go build -o /tmp/oidctest ./internal/oidctest/cmd/oidctest
docker run --rm --user "$(id -u):$(id -g)" -e HOME=/tmp \
  -v "$PWD:$PWD" -v /tmp/rove:/tmp/rove -v /tmp/oidctest:/tmp/oidctest \
  -e E2E_ROVE=/tmp/rove -e E2E_ISSUER=/tmp/oidctest -w "$PWD/ts" \
  mcr.microsoft.com/playwright:v1.63.0-noble npx playwright test
```

포트가 겹치면 `E2E_PORT=19510`처럼 옮깁니다(그 포트와 +1, +10, +11, +20, +21, +30을 씁니다).

PostgreSQL에서 돌리려면 `E2E_POSTGRES`에 데이터베이스를 만들 수 있는 계정으로 그 서버의 주소를 줍니다. 서버마다 Rove와 roster의 데이터베이스를 하나씩(`e2e_rove_<포트>`, `e2e_roster_<포트>`) 그 서버에 두고, 실행할 때마다 지우고 새로 만듭니다(`ts/e2e/postgres.mjs`). CI는 SQLite와, `go` 잡과 같은 pg_bigm이 든 PostgreSQL(`deploy/postgres`) 둘 다에서 돌립니다.

```sh
docker build -t rove-postgres deploy/postgres
docker run -d --rm --name rove-e2e-pg -p 127.0.0.1:55432:5432 \
  -e POSTGRES_USER=rove -e POSTGRES_PASSWORD=rove -e POSTGRES_DB=rove rove-postgres
E2E_POSTGRES='postgres://rove:rove@127.0.0.1:55432/rove?sslmode=disable' npx playwright test
docker stop rove-e2e-pg
```

Playwright 이미지로 돌릴 때는 앞의 Playwright `docker run`에 `--network host`와 `-e E2E_POSTGRES=...`를 더해, 컨테이너 안에서도 그 주소가 닿게 합니다.

검색을 OpenSearch로 돌리려면 `E2E_OPENSEARCH`에 클러스터 주소를 줍니다. TLS면 `https://사용자:비밀번호@…`로 주고, CA는 `E2E_OPENSEARCH_CA`에 줍니다. 서버마다 자기 인덱스(`e2e-<포트>-…`)를 쓰고, 뜨기 전에 지운 뒤 자기 DB로 다시 만듭니다. CI의 `e2e (sqlite, opensearch)`가 이렇게 돕니다(`scripts/opensearch.sh`). 개발용 클러스터는 위의 "검색 엔진"에 있습니다.

```sh
E2E_OPENSEARCH=http://127.0.0.1:9200 npx playwright test
```
