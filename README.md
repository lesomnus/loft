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

- **약관 동의**(design 9.11): `app.consent.terms`와 `app.consent.privacy`에 버전과 문서 주소를 주면, 계정은 처음 들어올 때(그리고 버전이 바뀔 때마다) 동의 화면을 봅니다. 동의하기 전에는 모든 호출이 거부되고, 터미널에는 `rove consent`(무엇에 동의해야 하는지와 주소)와 `rove consent --agree`가 있습니다. 개인 워크스페이스는 동의한 뒤에 생깁니다. 동의하지 않아도 자기 워크스페이스의 데이터는 내려받을 수 있습니다. 문서 내용은 운영자가 따로 게시합니다. 버전을 비워 두면(기본값) 아무것도 묻지 않고, roster에도 묻지 않습니다.
  - 동의 기록은 roster의 동의 원장(`ConsentService`)에 있습니다(D37). Rove의 설정에는 어느 문서의 어느 버전이 시행 중인지만 있고, 계정이 어느 문서의 어느 버전에 언제, 화면과 터미널 중 어디서 동의했는지는 roster가 고치지 않는 행으로 남깁니다(내 정보 › 동의한 문서). 동의는 roster의 감사 기록에도 남습니다.
  - 호출마다 roster에 동의했는지 묻습니다. 동의했다는 답은 30초 동안 믿고, 동의하지 않았다는 답은 매번 다시 묻습니다. roster에 물을 수 없으면 호출이 거부됩니다(503).
  - 계정이 roster에서 삭제되어도 동의 기록은 증거로 남고, roster의 `consent.keep`이 지나면 roster가 지웁니다. 내장 roster면 그 기간이 `auth.roster.consent_keep`(예: `26280h`)입니다. 비워 두면(기본값) 지우지 않습니다. 기간은 법률 검토 뒤에 정합니다(rove#35).
  - 이 판 전에 Rove가 직접 갖고 있던 동의 기록은 마이그레이션이 지우고, 계정은 다음 화면에서 한 번 더 동의합니다. 예전 동의는 Rove의 감사 기록(`rove.consent.agree`)에 누가 언제 했는지만 남고, 배포 전체의 보존 기간을 따릅니다(따로 두려면 `audit.by.d44`). `app.consent.keep`이나 `audit.by.consent`를 적어 둔 설정은 서버가 시작하지 않으므로 지웁니다.
- 처음 로그인한 계정이 어느 워크스페이스에도 없으면 자기 워크스페이스가 하나 생깁니다(가입). 워크스페이스는 계정마다 3개까지 더 만들 수 있고(`app.workspaces.owned`), 그 이상은 결제가 필요합니다(닫은 워크스페이스는 세지 않습니다). 이름은 직접 정하고, 주소(`/@…`)는 영문 소문자와 숫자 7자로 무작위로 정해집니다. 헷갈리는 i·l·o·0·1은 쓰지 않고 첫 글자는 영문입니다. 7자 주소가 붐비면 저절로 8자가 됩니다(`app.workspaces.alias_length`).
- 주소는 사용자가 바꿀 수 없습니다. 운영자가 `rove tenant alias --tenant <지금 주소> --to <새 주소> --yes`로 바꾸면, 이전 주소는 1년(`app.workspaces.alias_hold`) 동안 아무도 쓰지 못하게 보류되고 그동안 새 주소로 넘겨 줍니다. 예약어(`www`, `api`, `admin`, `l`, `sso`, `login` 등, `app.workspaces.reserved`로 더함)는 쓸 수 없습니다.
- **워크스페이스 닫기**(D29): 소유자는 설정 › 워크스페이스에서 이름을 입력해 닫습니다. 닫으면 소유자 말고는 아무도 들어오지 못하고, 소유자는 복구 기간 안에 복구하거나 모든 데이터를 zip으로 내려받을 수 있습니다. 복구 기간은 그 워크스페이스가 정하고(설정 › 워크스페이스 › 기록 보관), 정하지 않으면 30일(`app.workspaces.restore_within`)입니다. 닫기 화면이 그 기간을 알려 줍니다. 그 뒤에는 모든 데이터와 파일이 지워지고(법적 보존이 걸려 있으면 풀릴 때까지 기다림), 주소는 1년(`app.workspaces.alias_hold`) 동안 보류됩니다. 감사 기록은 계약이 정한 대로 떠납니다. 기본은 **은퇴**(retire)라서, 배포의 바닥값(`audit.min`, `audit.by.<종류>.min`)이 걸린 기록(접속기록 등)은 그 바닥값이 지날 때까지 남고 나머지는 함께 지워집니다. 계약이 `--purge-floors`를 말하면 감사 기록도 모두 지워집니다(아래 '조직 탈퇴'). 계정 탈퇴로 구성원이 아무도 남지 않은 워크스페이스는 저절로 닫힙니다. 운영자는 `rove tenant close|restore --tenant <주소> --yes`로 대신 닫고 엽니다.
- **지역과 예약**(rove#144): 소유자는 설정 › 워크스페이스 › 지역과 예약에서 세 가지를 정합니다. 비워 두면 배포의 기본값(`app.locale`, `app.no_show_after`)을 따릅니다.
  - **시간대:** 그 워크스페이스의 날짜가 이 시간대를 따릅니다. 화면에 보이는 날짜와 화면에서 입력하는 시각(브라우저가 어느 시간대에 있든), 자산 대장 CSV의 날짜, 알림 문장의 시각, 하루 단위 집계의 날짜 경계, 대량 등록이 읽는 날짜, 새 예약 공간의 기본 시간대가 모두 그렇습니다. `app.locale.timezone`을 비우면 Asia/Seoul입니다.
  - **통화:** 새 구매와 새 작업의 금액이 이 통화로 기록됩니다. 이미 기록된 금액은 기록할 때의 통화 그대로이고, 환산하지 않습니다. 비용이 있는 작업의 통화를 바꾸려면 그 통화로 비용도 함께 적어야 합니다. 금액은 그 통화의 최소 단위의 정수입니다(KRW는 원, USD는 센트). 그래서 화면에서 12.50달러를 넣으면 1250으로 저장됩니다. `app.locale.currency`를 비우면 KRW입니다.
  - **노쇼 해제:** 회의실 예약이 시작되고 몇 분 안에 체크인하지 않으면 예약을 풉니다(1~1440분). **체크인을 받지 않음**으로 두면 풀지 않습니다.
  - 터미널에서는 `rove api tenant configure acme '{"timezone":"America/New_York","currency":"USD"}'`입니다. 바꾸면 작업 기록(`workspace.configure`)이 남습니다.
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
roster app install --tenant accounts --role /roster.VouchService/Verify,/roster.HolderService/Get,/roster.TenantService/Get,/roster.MeService/Get,/roster.EmailService/List,/roster.EmailService/Get,/roster.SyncService/Watch,/roster.ConsentService/Agree,/roster.ConsentService/Settled,/roster.ConsentService/List rove

# Rove
ROVE_AUTH_ROSTER_ADDR=roster.example.com:443 ROVE_AUTH_ROSTER_DIRECTORY=accounts \
ROVE_AUTH_ROSTER_KEY=env:ROSTER_KEY ROSTER_KEY=rk_… go run ./cmd/rove serve
```

**운영처럼 보이는 배포에서 내장 roster를 쓰면 `rove serve`가 시작할 때 경고합니다.** 거부하지는 않습니다. 운영처럼 보인다는 것은 DB가 PostgreSQL(`db.driver: pgx`)이거나, `app.public_url`이 이 기계(`localhost`, `*.localhost`, 루프백 주소)가 아닌 경우입니다. 경고에는 둘 중 무엇 때문인지가 함께 나옵니다.

- 내장 roster는 서버 한 대의 배포를 간단하게 하려는 것입니다(design 9.10, CE).
- 그 프로세스만 roster에 닿고, 사람과 비밀번호가 Rove의 데이터 옆에 있으며, 다른 앱은 그 계정을 쓸 수 없습니다.
- 서버 한 대를 넘는 배포(SaaS, Dedicated, 여러 대)는 roster를 따로 운영합니다.
- 서버 한 대의 CE를 PostgreSQL과 자기 도메인으로 운영한다면 이 경고는 그대로 두어도 됩니다.

`EmailService`의 두 권한은 초대(D30)를 위한 것입니다. 초대장은 roster가 확인한 주소를 가진 계정의 초대함에 보이고, Rove는 그 주소를 이 권한으로 읽습니다. `SyncService/Watch`는 roster의 동기화 스트림입니다. roster가 계정을 중지·삭제하거나 모든 곳에서 로그아웃시키면 Rove가 그 자리에서 세션을 끝내고, 삭제된 계정을 떠나는 중으로 표시합니다. roster가 디렉터리를 닫으면(`roster tenant close`, roster#105) 그 디렉터리의 모든 계정의 세션이 끝나고, 닫혀 있는 동안은 아무도 로그인하지 못합니다. 아무것도 지우지 않으므로 디렉터리를 복구하면 다시 로그인할 수 있습니다. `ConsentService`의 세 권한은 약관 동의(`app.consent`, D37)를 위한 것입니다. Rove는 계정이 무엇을 빚졌는지 묻고(`Settled`), 동의를 기록하고(`Agree`), 동의한 문서를 읽습니다(`List`). Rove가 묻는 문서는 모두 필수라서 철회(`Withdraw`)는 쓰지 않습니다.

`auth.roster.account_url` 에 roster의 계정 페이지(디렉터리의 front door, 예: `https://id.example.com/`)를 주면, 내 정보 화면에 "로그인 방법 관리"와 "계정 탈퇴" 링크가 생깁니다. 탈퇴 링크는 그 페이지의 `#leave` 로 가고, 계정 삭제는 roster에서 합니다(`MeService.Leave`, roster#106). 삭제는 디렉터리가 허용할 때만 되므로, SaaS의 디렉터리는 roster 운영자가 `TenantConfig.leave` 를 켭니다(관리 콘솔의 테넌트 화면, 또는 resources 파일의 `config.leave`). 삭제된 계정은 위의 스트림으로 Rove에 들어와 떠나는 중이 됩니다.

이 권한들이 없던 때 설치한 배포는 역할을 넓혀야 합니다. `app install`을 다시 실행해도 이미 있는 역할은 그대로 두므로(roster `docs/apps.md`), roster 운영자가 역할을 직접 고칩니다. 목록을 통째로 바꾸므로 전체를 줍니다.

```sh
roster role patch @accounts/rove-app '{"methods": ["/roster.VouchService/Verify", "/roster.HolderService/Get", "/roster.TenantService/Get", "/roster.MeService/Get", "/roster.EmailService/List", "/roster.EmailService/Get", "/roster.SyncService/Watch", "/roster.ConsentService/Agree", "/roster.ConsentService/Settled", "/roster.ConsentService/List"], "date_updated_force": true}'
```

고치기 전까지는 초대 메일의 링크로 받은 초대장만 초대함에 보이고, 세션은 다음 호출에서(30초 안에) 끝나며, 삭제된 계정은 떠나는 중으로 표시되지 않습니다. 약관 동의를 묻는 배포는 계정이 동의했는지 알 수 없으므로 모든 계정의 호출이 거부됩니다(503, 서버 로그에 `roster role patch`가 적힙니다). 스트림을 거부당한 Rove는 서버 로그에 오류를 한 번 남기고 10분마다 다시 청하고, 동의 원장은 다음 호출에서 다시 물으므로, 역할을 고친 뒤 다시 시작하지 않아도 됩니다. 조직의 키(`rt_…`)로 붙은 배포는 키가 허용한 메서드에도 들어 있어야 하므로 키를 새로 만듭니다.

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
go run ./cmd/rove register --form 양식.csv             # 대량 등록 양식
go run ./cmd/rove register 입고.csv                    # 무엇이 등록될지와 문제를 줄마다 (아무것도 바꾸지 않음)
go run ./cmd/rove register --yes --reason "10월 입고" 입고.csv   # 실제로 등록
go run ./cmd/rove workspace export --out acme.zip acme   # 소유한 워크스페이스의 모든 데이터 (화면의 내려받기와 같음)
go run ./cmd/rove workspace new 연구실                 # 워크스페이스 만들기
go run ./cmd/rove logout
```

- 승인하면 그 계정의 세션이 터미널에 생깁니다. 브라우저와 같은 권한 표를 따르고, 유지 시간도 같으며, roster에서 계정이 중지·삭제되면 함께 끝납니다. 승인 화면은 요청한 터미널과 위치를 보여 줍니다. 내 터미널에서 방금 `rove login`을 실행했고 같은 코드가 보일 때만 승인하세요.
- 로그인 정보는 `~/.config/rove/client.json`(`ROVE_CLIENT_CONFIG`)에 주인만 읽을 수 있게 저장됩니다. 세션 키가 들어 있습니다.
- **좁혀서 로그인하기(`--allow`):** 터미널이 부를 수 있는 작업만 적어서 로그인할 수 있습니다(D33). 비서에게 맡기는 터미널이 그 정도만 하게 하려는 것입니다.

  ```sh
  rove login --allow read                                  # 읽기만: 조회, 검색, 이력
  rove login --allow read --allow '/rove.ReservationService/*'   # 읽기와 예약
  rove login --allow '/rove.AssetService/Move'             # 메서드 하나
  ```

  - 패턴은 `/패키지.서비스/메서드`이고, 어느 부분이든 `*`로 쓸 수 있습니다(`/rove.*/Get`).
  - 승인 화면에 그 목록이 나오고, 세션에 그대로 적힙니다. 서버가 모든 호출에서 지키므로, 세션 키를 가진 누구도 목록 밖은 하지 못합니다.
  - 좁힌 세션은 RPC 옆의 HTTP 중 계정·동의 내역·라벨 조회 같은 읽기만 됩니다. 다른 터미널 승인, 데이터 내보내기, 워크스페이스 만들기, 약관 동의는 거부됩니다.
  - `rove whoami`가 그 목록을 보여 줍니다. 넓히려면 `--allow` 없이 다시 로그인합니다.
- `rove api`는 서버의 설정을 읽지 않습니다. 서버 상자에서 쓰는 운영자 명령(`init`, `tenant`, `identity` 등)과는 다릅니다.

### 비서에게 맡기기 (MCP)

`rove mcp`는 MCP 서버입니다(D32). Claude Code나 Claude Desktop 같은 MCP 클라이언트가 이 명령을 띄우면, 비서가 터미널에 로그인한 계정으로 워크스페이스를 읽고, 그 사람이 승인한 것을 바꿉니다. 먼저 `rove login`을 하고, `rove`가 PATH에 있어야 합니다(`go install ./cmd/rove`).

```sh
claude mcp add --scope user rove -- rove mcp                  # Claude Code
claude mcp add --scope user rove-lab -- rove mcp --workspace lab   # 다른 워크스페이스는 따로
```

Claude Desktop은 설정 파일(`claude_desktop_config.json`)의 `mcpServers`에 넣습니다.

```json
{
  "mcpServers": {
    "rove": { "command": "rove", "args": ["mcp"] }
  }
}
```

- **읽는 도구:**
  - 자산 찾기(`search_assets`), 자산 하나(`get_asset`), 이력(`asset_history`), 어느 시점의 모습(`asset_at`)
  - 최근에 일어난 일(`recent_events`), 지급과 대여(`list_custody`), 예약(`reservations`)
  - 사람과 팀 찾기(`find_people`), 모델 찾기(`find_models`)
  - 재고(`list_stock`), 작업 지시(`list_work_orders`), 실사와 그 결과(`list_counts`), 구매(`list_purchases`)
  - 누구로 읽는지(`whoami`)
- **쓰기는 미리보기와 적용, 두 단계입니다(D33).**
  - 미리보기 도구는 아무것도 바꾸지 않습니다.
    - `plan_asset`: 등록, 이동, 속성 변경, 담당 지정, 연결, 정정, 등록 취소
    - `plan_custody`: 지급·대여, 반납, 기한 연장, 인수 확인
    - `plan_reservation`: 예약, 확정, 승인, 반려, 취소, 체크인, 완료
    - `plan_stock`: 재고 등록, 입고, 사용, 조정, 이동, 자산으로 전환
    - `plan_count`: 실사 시작, 스캔, 대조, 처리, 마감
    - `plan_work_order`: 수리·점검 접수, 변경, 완료, 취소
    - `plan_purchase`: 구매 등록, 입고
    - `plan_model`: 모델 등록, 변경
    - `plan_label`: 라벨 만들기, 연결, 해제
  - 이 도구들은 바뀔 것을 이름으로 적은 문장("이동: NB-1 개발용 노트북, 본관 › 3층 창고 → 본관 › 4층 회의실 (놓임)")과 토큰을 돌려줍니다.
  - 사람이 그 문장을 보고 "예"라고 하면, 비서가 `apply`에 토큰과 그 문장을 그대로 넘겨 실행합니다. 문장이 `apply`의 인자라서, 클라이언트가 실행 전에 묻는 승인 창에도 그 문장이 보입니다.
  - 토큰은 10분 동안, 한 번만 쓰입니다. 같은 작업을 다시 보내도 서버는 한 번으로 처리합니다.
  - 이력에는 그 사람이 한 것으로 남습니다.
- **비서가 할 수 있는 것을 좁히려면** 그 터미널을 `rove login --allow`로 로그인합니다. 예를 들어 `--allow read`면 읽기만 하고, 미리보기 단계에서 거부됩니다.
- 브라우저와 같은 권한 표와 벽을 따르므로, 비서는 그 사람이 볼 수 있는 것만 봅니다. 서버는 MCP를 모르고, 새로 여는 것도 없습니다.
- 로그인은 부를 때마다 다시 읽습니다. `rove login`과 `rove use`는 비서를 다시 띄우지 않아도 다음 호출부터 적용됩니다.
- 도구의 답(자산과 사람의 이름 등)은 비서를 제공하는 쪽으로 갑니다. 그래서 사람의 이메일과 전화번호는 답에 넣지 않습니다.

### UI 개발

```sh
go run ./cmd/rove serve          # :8080
cd ts && npm run dev             # :5173, API 호출은 Vite가 :8080으로 넘김
```

스캔한 라벨이 개발 서버로 열리게 하려면 `ROVE_APP_APP_URL=http://localhost:5173` 을 주고 서버를 띄웁니다.

오프라인으로 여는 것(서비스 워커)은 빌드한 UI(`npm run build`)를 서버가 줄 때만 동작합니다. `npm run dev` 에서는 등록하지 않습니다.

### PostgreSQL로 실행

배포 데이터베이스는 PostgreSQL이고, 공식 이미지를 그대로 씁니다. SQLite에는 없는 안전장치(예약·위치가 겹치면 DB가 거부하는 EXCLUDE 제약 등)가 `serve` 의 마이그레이션 때 함께 설치됩니다. 필요한 확장은 `btree_gist` 하나입니다.

검색은 아래의 검색 엔진(OpenSearch)이 답합니다. 클러스터를 설정하지 않으면 데이터베이스가 인덱스 없이 그 워크스페이스의 행을 모두 읽어서 답합니다. 개발용과 작은 배포에는 그것으로 충분합니다.

내장 roster도 같은 서버의 다른 데이터베이스를 씁니다.

```sh
docker run -d --name rove-pg -e POSTGRES_USER=rove -e POSTGRES_PASSWORD=rove -p 5432:5432 postgres:18
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
- `app.signing_key`: 모든 서버에 같은 값을 줍니다. 다운로드 링크를 서명하고, 세션의 키도 이 키에서 만든 키로 보관합니다(아래 "세션의 키"). 비워 두면 서버마다 다른 키라, 다른 서버가 만든 다운로드 링크가 열리지 않고 다른 서버의 세션도 알아보지 못합니다. 그래서 `spin.lease`가 `local`이 아니거나 `watch.broker`가 `memory`·`none`이 아닌데 키가 비어 있으면 `serve`가 시작하지 않습니다.
- 로그인 제한은 데이터베이스에서 세므로 서버가 몇 대든 한 번에 셉니다. 같은 주소에서 같은 아이디로 15분에 10번 틀리면 잠시 막고, 한 주소에서 터미널 로그인(`rove login`)은 10분에 20번까지입니다. 계정 잠금(연속 10번 틀리면 15분)은 roster가 합니다.
- 첨부 파일(`app.files`)은 아직 로컬 디렉터리뿐이라, 여러 서버가 같은 디렉터리를 공유해야 합니다(NFS 등).

설정할 수 있는 모든 값은 `go run ./cmd/rove config env` 로 볼 수 있습니다. 기본값은 [`rove.yaml`](rove.yaml)에 있습니다.

### 검색 엔진 (OpenSearch)

설정에 클러스터가 없으면 검색은 데이터베이스가 답합니다(인덱스 없이 행을 모두 읽습니다). `app.search.opensearch`로 클러스터를 주면 OpenSearch와 한국어 분석기 nori가 답합니다. nori는 "웹서버"를 웹 · 서버로, "의자를"을 의자로 나눠서 찾습니다(D31).

자산(`AssetService.Search`)과 사람·팀·거래처(`PartyService.Search`), 모델(`ItemModelService.Search`)을 이렇게 찾습니다. 색인은 셋이 따로이고, 별칭도 `<index>-assets`, `<index>-parties`, `<index>-models`로 따로입니다.

무엇을 입력했는지에 따라 이렇게 찾습니다.

- **낱말:** 붙여 쓴 합성어(웹서버)와 조사가 붙은 낱말(의자를)도 낱말로 찾습니다. 쳐 넣은 한 낱말은 그 조각을 모두 갖춘 것만 찾습니다. 그래서 없는 태그(E2E-ABC12)는 글자 몇 개가 겹치는 다른 자산을 끌어오지 않습니다.
- **두 낱말:** 둘 다 맞은 것을 먼저 보여 주고, 그다음은 드문 낱말이 맞은 것부터입니다. "GPU 서버"면 GPU서버, NVIDIA GPU, 웹서버 순입니다.
- **태그와 시리얼:** 통째로도, 일부로도 찾습니다.
- **이름:** 입력하는 중인 앞부분으로도, 한 글자 틀린 것으로도 찾습니다.
- **사람:** 이름의 두 글자 이상 일부로도 찾습니다("민수"로 김민수). 이름 전체가 맞은 사람이 먼저입니다. 팀 이름으로 찾으면 팀이 먼저 나오고, 그 팀의 사람이 뒤에 나옵니다. 이메일과 전화번호는 색인에 넣지 않습니다.
- **모델:** 이름, 제조사, 코드, 유형 이름으로 찾습니다.

저장한 것은 그 응답이 오기 전에 색인에 들어갑니다. 저장하고 바로 검색해도 보입니다. 다른 사람이나 다른 프로세스가 바꾼 것은 1초쯤 늦을 수 있습니다.

아무것도 입력하지 않은 목록(필터만)은 그대로 데이터베이스가 답합니다. 클러스터가 답하지 않으면 검색은 "지금은 검색할 수 없습니다"로 답하고, 데이터베이스로 몰래 넘어가지 않습니다.

색인은 서버가 만듭니다. 처음 뜰 때 색인이 없거나, 색인의 모양이 바뀐 빌드가 뜨면 루프가 DB로 새로 만든 뒤 별칭을 옮깁니다. 그동안 검색은 옛 색인이 답하고, 옛 색인이 없으면 "지금은 검색할 수 없습니다"입니다.

운영 구성에서 켜는 법은 `deploy/compose/README.md`의 "검색을 켜려면"과 `deploy/k8s/README.md`의 9에 있습니다.

- **바뀐 것이 색인에 닿는 길:**
  - 자산, 유형, 모델, 사람이 바뀌면 같은 트랜잭션에 "다시 색인할 것"(`searchstale`)이 남습니다.
  - 배포에서 한 서버의 루프(`rove.search.index`)가 1초쯤 안에 그 문서를 DB의 지금 상태로 다시 만듭니다. 이름을 빌려 오는 문서도 함께입니다: 모델 이름이 바뀌면 그 모델의 자산, 유형 이름이 바뀌면 그 유형의 모델, 팀 이름이 바뀌면 그 팀의 사람입니다.
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
        index: rove   # 인덱스 이름의 앞부분. 별칭 rove-assets가 rove-assets-<시각>을 가리킵니다 (parties, models도 같음)
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
  rove search reindex          # 지금 다시 만들기: 색인마다 DB로 새 인덱스를 다 채운 뒤, 별칭을 옮기고 옛 인덱스를 지움
  rove search status           # 색인마다 별칭이 가리키는 인덱스, 문서 수와 DB의 행 수
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

**작업 기록과 자산의 사실을 payday의 과거로 옮기는 버전(payday#151)은 되돌릴 수 없습니다.** 그 `rove migrate`는 Rove 자신의 Fact 표를 payday의 모양으로 한 번 옮기고(`the assets' facts moved into payday's past: N rewritten, N put on the trail`), 그 전 빌드는 새 표를 읽지 못합니다.

- 올리기 전에 DB를 백업합니다(`pg_dump`, [백업과 복구](docs/runbook/restore.md)). 이 버전을 넘는 `rollout undo`는 하지 않습니다. 되돌리려면 백업에서 되살립니다.
- 옮긴 행을 감사 기록에 남기므로 마이그레이션에 다이제스트 키가 필요합니다(`audit.digest.key`). Kubernetes의 `migrate` 컨테이너는 `digest.key`만 마운트합니다. 옮길 사실이 없는 DB는 키를 읽지 않습니다.
- 과거가 쓸 수 없는 사실(자산의 필드가 아닌 키, ID로 읽히지 않는 유형·모델, UTF-8이 아닌 글)이 있으면 종류별 건수와 함께 멈추고 아무것도 옮기지 않습니다. 옆에 둔 표(`rove_fact_aside`)를 고친 뒤 다시 실행하면 이어서 옮깁니다.
- 올린 뒤 `rove trail rows`는 그 전에 쓴 작업 기록을 `N rows from before seals`로 셉니다. 작업 기록의 사유(`reason`)가 이제 개인정보로 선언되어 봉인되기 때문입니다. 살펴본 뒤 `rows --accept --of <다이제스트> --why "사유가 개인정보가 되기 전의 작업 기록 (payday#151)"`을 한 번 실행합니다.
- 새 작업 기록은 문장을 갖지 않고 종류의 메시지를 갖습니다. 문장은 읽을 때 만들고, 그 전 기록은 쓴 문장 그대로 읽힙니다. 유형과 모델의 사실은 소문자 정규 ID로 읽힙니다.

**운영자 명령이 말하는 날짜는 UTC입니다.** `rove`의 운영자 명령(`tenant`, `contract`, `retention`, `trail`, `access` 등)과 `rove-admin`이 말하는 날짜와 시각은 셸의 시간대와 상관없이 UTC입니다. `--effective 2027-01-01`처럼 주는 날짜도 UTC의 그날입니다. 워크스페이스의 사람이 앱에서 읽는 날짜는 그 워크스페이스의 시간대를 따릅니다(#144).

Kubernetes에 띄우는 참조 배포는 [`deploy/k8s`](deploy/k8s/README.md)입니다. 무엇을 백업하고 어떤 순서로 되살리는지는 [백업과 복구](docs/runbook/restore.md)에 있습니다.

**세션의 키.** 브라우저의 세션, 터미널의 세션(`rove login`), 워크스페이스 도메인에 넘긴 세션과 그 코드는 DB에 키가 아니라 키의 HMAC으로 남습니다. 정확히는 키의 SHA-256에 대한 HMAC이고, HMAC의 키는 `app.signing_key`에서 이 용도만의 이름으로 만듭니다(HKDF). DB에 쓸 수 있는 사람이 자기가 아는 키로 세션 행을 넣어도 아무도 로그인되지 않습니다. 세션은 감사 기록에 남지 않으므로, 이것이 그 표를 들어오는 문이 되지 않게 막습니다.

- **`app.signing_key`를 바꾸면 모두 로그아웃됩니다.** 브라우저도 터미널도 다시 로그인합니다. 다운로드 링크도 새로 받습니다.
- 키를 비워 둔 체크아웃은 시작할 때마다 키를 새로 만듭니다. 그래서 다시 시작하면 다시 로그인합니다. 로그인을 이어 가려면 체크아웃에도 키를 하나 줍니다(`ROVE_APP_SIGNING_KEY`).
- **업그레이드:** 이 방식으로 바뀐 버전으로 올리면 지금 있는 세션은 맞지 않습니다. 모두 한 번 다시 로그인하고, 터미널도 `rove login`을 다시 합니다. 그 밖에 할 일은 없습니다.

### 휴대폰으로 스캔하려면

브라우저는 **HTTPS 이거나 localhost 일 때만** 카메라를 열어 줍니다. 그래서 PC의 `http://localhost:8080` 은 그 PC에서만 카메라 스캔이 됩니다.

- 같은 PC에서 USB/블루투스 바코드 스캐너를 쓰면 스캔 화면의 입력칸에 그대로 입력됩니다 (QR 주소와 태그 모두 읽습니다).
- 휴대폰에서 쓰려면 서버를 HTTPS로 열어야 합니다. 예를 들어 [Caddy](https://caddyserver.com)를 앞에 두고(`reverse_proxy localhost:8080`, 사내망이면 `tls internal`) `app.public_url` 을 그 주소에 맞춥니다. 라벨 QR은 `<public_url>/l/<코드>` 로 인쇄되므로(D28), 휴대폰 기본 카메라로 찍어도 바로 그 자산이 열립니다.
- 프록시를 앞에 두면 `app.trusted_proxies`에 프록시 주소를 적습니다(`[127.0.0.1]`이나 `[10.0.0.0/8]`처럼, 환경변수로는 `ROVE_APP_TRUSTED_PROXIES=10.0.0.0/8,127.0.0.1`). 적은 프록시가 보낸 `X-Forwarded-For`만 믿습니다. 비워 두면 모든 요청이 프록시에서 온 것으로 보입니다. 그러면 로그인 실패 제한이 프록시 뒤 모든 사람에게 하나로 묶여, 누군가 열 번 틀리면 그 이름으로는 아무도 로그인하지 못합니다. 프록시가 아닌 곳에서 온 `X-Forwarded-For`는 무시합니다. 호출자가 마음대로 쓸 수 있는 값이라서입니다.
- 라벨 코드는 8자(`7K3M-Q9TX`처럼 넷씩 끊어 라벨 아래에도 인쇄)이고 서비스 전체에서 하나뿐입니다. 카메라가 못 읽으면 스캔 화면에 그대로 입력하면 되고, 소문자나 `-` 없이 써도, `I`·`L`을 `1`로, `O`를 `0`으로 읽습니다. 다른 워크스페이스의 라벨을 스캔하면, 내가 그 워크스페이스의 구성원일 때 그곳으로 넘어가 열립니다. 구성원이 아니면 없는 라벨과 똑같이 "찾지 못함"입니다.
- **워크스페이스의 도메인**(rove#155): 어느 워크스페이스든 설정 › 도메인과 라벨에서 자기 도메인을 둘 수 있습니다. 도메인이 없어도 라벨은 언제나 켜져 있고, 그때는 서비스 주소로 인쇄됩니다.
  - **종류.** 무엇에 쓰이는지는 고르는 것이 아니라 종류가 정합니다.
    - **자체 도메인**(`assets.acme.co.kr`)은 앱 도메인(`purpose: app`)입니다. 쓰기로 하면 그 워크스페이스의 앱 전체가 그 도메인에 있고, 라벨도 그 도메인으로 인쇄됩니다.
    - **기본 주소**는 `app.labels.suffix` 아래 하위 도메인(`<이름>.<suffix>`)입니다. DNS 작업이 필요 없고 라벨만 씁니다. 새 워크스페이스에 저절로 생기지 않고, 원하는 워크스페이스가 추가합니다.
  - **고객(워크스페이스 소유자)이 할 일:** 도메인을 더하고, 설정 화면에 나오는 레코드 둘을 DNS에 만듭니다. 몇 분마다 저절로 확인하고, 확인되면 쓸 수 있습니다.
    - CNAME `<도메인>` → 화면에 나오는 이름(`app.labels.target`). CNAME을 둘 수 없는 apex라면 DNS 업체의 ALIAS나 CNAME 펼치기로 같은 주소를 가리키게 합니다.
    - TXT `_rove-challenge.<도메인>` = 화면에 나오는 토큰.
    - **확인된 뒤에도 두 레코드를 지우지 않습니다.** 쓰는 동안 한 시간마다 다시 확인하고, 여섯 번 연속(다섯 시간 넘게) 맞지 않으면 사용을 멈춥니다(`DNS 확인 실패`). 그러면 로그인과 링크는 서비스 주소로 돌아가고, 소유자와 관리자에게 알림과 메일이 갑니다. 레코드를 되돌리면 일주일 동안 저절로 다시 확인하고, 설정에서 바로 확인할 수도 있습니다. 답이 없는 DNS(시간 초과, DNS 업체 장애)는 세지 않습니다.
  - **그 도메인에서 일하는 사람:**
    - 로그인은 서비스 주소가 합니다. 로그인하지 않은 채 도메인을 열면 서비스 주소로 넘어가 로그인(이미 했으면 그대로)한 뒤, 1분 동안 한 번만 쓰이는 코드로 그 도메인 전용 세션을 받아 돌아옵니다.
    - 그 워크스페이스는 그 도메인에서 엽니다. 서비스 주소에서 그 워크스페이스의 화면(`/@주소/...`)을 열면 그 도메인의 같은 화면으로 넘어가고, 워크스페이스 전환에서 고르면 바로 그 도메인으로 갑니다. 호출, 로그인, 계정(`/account`), 터미널, 라벨(`/l/...`)은 서비스 주소에서도 그대로 답합니다. 활성이 아닌 도메인(이전 주소, DNS 확인에 실패한 도메인)으로는 보내지 않습니다.
    - 그 세션은 그 워크스페이스만 다룹니다. 워크스페이스 전환에는 그 워크스페이스 하나와, 계정의 다른 워크스페이스를 고르는 서비스 주소의 목록(`다른 워크스페이스`)으로 가는 링크가 나옵니다. 다른 워크스페이스를 부르는 헤더나 `/@주소`, 파일은 거부합니다. 워크스페이스 만들기, 받은 초대, 약관 동의, 터미널 로그인은 서비스 주소에서 합니다.
    - 도메인에서 로그아웃하면 서비스 주소의 로그인도 함께 끝납니다. 서비스 주소에서 로그아웃해도 도메인의 세션이 끝납니다.
    - 알림 메일의 링크, 새로 인쇄하는 라벨, 첨부의 내려받기 주소가 그 도메인으로 만들어집니다. 초대 메일의 링크는 언제나 서비스 주소입니다. 초대받은 계정은 아직 그 워크스페이스의 구성원이 아니고, 초대함은 서비스 주소에 있기 때문입니다.
  - **도메인을 바꾸면** 이전 도메인은 '이전 주소'로 남아 그 주소로 인쇄한 라벨이 계속 열립니다. 이전 앱 도메인은 `app.workspaces.domain_redirect`(기본 90일) 동안 페이지를 새 도메인으로 보냅니다. 서비스 주소로 인쇄한 라벨도 그대로 열립니다.
  - **운영자가 켜는 법:** 워크스페이스 도메인의 인증서는 프런트가 요청 시점에 받습니다. compose는 `compose.domains.yaml`(`deploy/compose/README.md`의 "워크스페이스 도메인을 받으려면"), Kubernetes는 컴포넌트 `deploy/k8s/domains`(`deploy/k8s/README.md`의 11)로 켭니다.
    - 프런트는 인증서를 받기 전에 운영자 진입점(`/internal/tls-ask`, `admin.ask`)에 묻습니다. 확인된 도메인에만, 정해진 횟수 안에서 받습니다.
    - `app.labels.target`에 고객의 CNAME이 가리킬 이름을 넣습니다. 프런트를 가리키는 DNS 이름입니다.
    - 서비스 주소의 인증서는 지금 그대로입니다.
  - **호스트마다 받는 것:**
    - 활성 앱 도메인: 그 워크스페이스의 앱.
    - 서비스 주소(`app.public_url`·`app.app_url`의 호스트): 모든 워크스페이스를 받습니다. 활성 앱 도메인이 있는 워크스페이스의 화면만 그 도메인으로 보냅니다.
    - 다른 워크스페이스 도메인(라벨 도메인, 이전 주소, DNS 확인에 실패한 도메인): 그 주소로 인쇄한 라벨만 엽니다.
    - 그 밖의 호스트: `/healthz`만 답합니다.
    - `app.public_url`이 없는 체크아웃에서는 워크스페이스 도메인이 아닌 모든 호스트가 서비스 주소입니다.
  - 호스트는 소문자와 ASCII(IDNA)로 저장합니다. 이 서비스의 주소와 그 아래 이름, IP 주소, `localhost` 아래 이름은 받지 않고, 한 호스트는 한 워크스페이스만 씁니다.
  - 예전의 `app.labels.tenant_domains`는 없어졌습니다. 설정에 `true`로 남아 있으면 무시하고 시작할 때 경고합니다.

### 콘텐츠 보안 정책 (CSP)

모든 화면은 콘텐츠 보안 정책과 함께 나갑니다(`server/csp`). 페이지는 이 서버의 스크립트·스타일·이미지와 직접 그린 QR(`data:`), 이 서버의 API만 씁니다. 폼은 이 서버, 서비스 주소, SSO issuer로만 보냅니다. 워크스페이스 도메인에서 로그아웃하는 폼은 서비스 주소에서 끝나고, 브라우저는 폼이 넘겨 가는 곳도 정책으로 따지기 때문입니다. 누가 페이지에 스크립트를 끼워 넣어도 브라우저가 실행하지 않게 하는 장치입니다.

지금은 `app.csp: report-only`(기본)라 막지는 않습니다. 브라우저가 정책을 어긴 것을 `/csp-report`로 보내면 서버 로그에 `csp: a page did what the policy does not allow`로 남깁니다. 로그는 1분에 30줄까지이고, 넘친 것은 다음 줄에 개수만 남습니다. 주소의 쿼리는 지웁니다(첨부 주소의 서명 등). 운영에서 이 로그가 조용한 것을 확인한 뒤 `enforce`로 바꾸면 막습니다. `off`는 둘 다 하지 않습니다. 다른 사이트가 화면을 틀에 넣는 것은 설정과 상관없이 늘 막습니다.

e2e는 어느 화면이든 정책을 어기면 실패합니다(`ts/e2e/rove.ts`).

### 조직별 이력 보존

워크스페이스가 이력을 얼마나 보관할지는 **워크스페이스가 정합니다**(design 8, D36). 소유자가 설정 › 워크스페이스 › 기록 보관에서 정합니다. 접속기록을 얼마나 보관할지와 닫은 뒤 얼마나 복구할 수 있는지도 같은 자리에서 정합니다(rove#144). 보관하는 기록은 모두 보입니다. 조회 기간을 따로 두지 않습니다.

- **이력**: 비우면 영원히 보관합니다.
- **접속기록**: 비우면 배포의 `audit.by.access`를 따릅니다. 배포의 바닥값(`audit.by.access.min`)보다 짧게는 정할 수 없습니다.
- **닫은 뒤 복구 기간**: 비우면 배포의 `app.workspaces.restore_within`을 따릅니다.
- **줄인 기간은 유예가 지난 뒤에 적용됩니다**(`app.retention.grace`, 기본 30일). 그때까지는 그 전 기간을 지키므로, 그 안에 되돌리면 아무것도 지워지지 않습니다. 저장하기 전에 화면이 언제부터 무엇이 몇 건 지워지는지 보여 줍니다(`TenantService.Preview`).

운영자의 몫은 **계약**과 법적 보존(hold)입니다. 셸에서만 쓰고, 조직의 사람은 읽기만 합니다. 계약은 파기할 때 감사 기록을 어떻게 할지만 정합니다(아래 '조직 탈퇴').

```sh
# 파기할 때 감사 기록을 모두 지움: 배포의 바닥값이 걸린 것까지
go run ./cmd/rove contract set --tenant rove --name abroad --purge-floors --effective 2027-01-01
go run ./cmd/rove contract show --tenant rove   # 계약들, 워크스페이스가 정한 기간이 지금 어떻게 되는지, 걸린 hold

go run ./cmd/rove hold place --tenant rove --why "사건 2026-1"   # 이력을 아무것도 지우지 않음
go run ./cmd/rove hold lift <hold-id>
```

**지우는 일은 기본으로 꺼져 있습니다.** 고지·내보내기·유예·백업 정책이 정해지기 전에는 파괴적인 동작을 켜지 않는다는 원칙(design 1장) 때문입니다. 꺼져 있어도 기간은 읽힙니다. 설정 화면과 `plan`이 무엇이 지워질지 말해 줍니다.

```sh
go run ./cmd/rove retention plan                 # 지금 지우면 무엇이 몇 건 사라지는지 (아무것도 바꾸지 않음)
go run ./cmd/rove retention plan --tenant rove   # 한 조직만
go run ./cmd/rove retention run                  # 실제로 지움 — app.retention.apply 가 켜져 있을 때만
```

`plan` 은 삭제를 트랜잭션 안에서 실제로 해 보고 되돌리므로, 말하는 숫자가 `run` 이 지우는 숫자와 같습니다. 설정 화면의 미리 보기는 같은 조건으로 세기만 하므로 행을 잠그지 않습니다. `app.retention.apply: true` 이면 `serve` 가 `app.retention.every`(기본 1시간)마다 같은 일을 하고, 감사 기록의 이력 종류도 같은 기간으로 지웁니다. 지운 뒤에는 무엇을 몇 건, 어느 날짜 이전 것을 지웠는지만 적은 기록(`retention.expired` 작업 기록)이 남습니다. 지워지는 것은 보관 기간이 시작되기 전에 **끝난** 이력입니다: 끝났거나 정정된 위치·담당·관계 기록, 그 뒤에 바뀐 속성 값, 아무 기록도 가리키지 않는 작업 기록, 끝난 예약·다 돌려받은 지급·닫힌 실사·끝난 작업, 재고 이동. 기간이 시작될 때의 상태, 아직 끝나지 않은 것, 등록(취득) 기록과 폐기·불용·분실 상태는 남습니다. hold가 걸린 조직은 아무것도 지우지 않습니다.

이 버전으로 올리면 마이그레이션이 계약에 있던 보관·접속기록·복구 기간을 워크스페이스로 한 번 옮깁니다. 효력 중인 계약의 값을 옮기고, 그 전 계약의 값은 유예가 지킬 그 전 값으로 옮깁니다. 앞으로 효력이 생길 계약의 그 값은 옮기지 않습니다. 옮긴 뒤 한 번 해야 할 일은 `docs/runbook/trail-integrity.md`에 있습니다.

### 운영자 진입점 (rove-admin)

운영자는 서버 상자의 셸 없이도 일할 수 있게 **운영자 진입점**(design 6장)을 따로 띄웁니다. 공개 바이너리(`rove`)에는 들어 있지 않은 별도 바이너리(`rove-admin`)이고, 운영자 네트워크(기본은 루프백, VPN 주소까지)에서만 닿는 포트로 듣습니다. 요청마다 운영자의 키가 필요하고, 서버 로그에 누가 무엇을 했는지 이름으로 남습니다.

```sh
# 서버 옆에서: 같은 설정(rove.yaml, ROVE_*)에 운영자와 키만 더함 (admin.addr 기본 127.0.0.1:8081)
export ALICE_KEY=$(openssl rand -hex 24)
ROVE_ADMIN_KEYS='{"alice": "env:ALICE_KEY"}' go run ./cmd/rove-admin serve

# 운영자의 컴퓨터에서: 진입점 주소와 자기 키 (--url, --key로 줘도 됨)
export ROVE_ADMIN_URL=http://127.0.0.1:8081 ROVE_ADMIN_KEY=env:ALICE_KEY
go run ./cmd/rove-admin workspace ls --q acme                     # 주소·이름의 일부로 찾기 (--closed: 닫힌 것만)
go run ./cmd/rove-admin workspace show acme                       # 구성원, 워크스페이스가 정한 보관 기간, 계약, hold
go run ./cmd/rove-admin workspace close --reason "요청" --yes acme # 닫기 (소유자는 복구 기간 안에 복구할 수 있음)
go run ./cmd/rove-admin workspace restore --yes acme
go run ./cmd/rove-admin workspace owner --member kim --yes acme    # 구성원을 소유자로 지정
go run ./cmd/rove-admin workspace alias --to acme-kr --yes acme    # 주소 바꾸기 (이전 주소는 보류되고 새 주소로 넘김)
go run ./cmd/rove-admin contract set --name abroad --purge-floors --yes acme   # 셸의 `rove contract set`과 같은 항목
go run ./cmd/rove-admin hold place --why "사건 번호" --yes acme; go run ./cmd/rove-admin hold lift --yes <ID>
```

- 키는 32자 이상이어야 하고(`admin.keys.<이름>: env:…|file:…|키`), 키가 하나도 없으면 `serve`가 시작하지 않습니다. 플래그는 인자보다 앞에 씁니다.
- 리버스 프록시의 on-demand TLS 확인(`/internal/tls-ask`)도 이 진입점으로 옮겼습니다(키 없이 답함). 공개 서버에는 더 이상 없습니다.
- 셸의 운영자 명령(`rove tenant`, `rove contract`, `rove hold` 등)은 그대로 있습니다. 운영자 웹 화면은 나중입니다.

### 조직 탈퇴 (운영자)

설계 8.2의 순서대로 **내보내기 → 유예 → 파기**입니다. 순서는 운영자가 지킵니다.

```sh
go run ./cmd/rove tenant export --tenant rove --out rove.zip   # 모든 행(종류마다 JSON 줄), 감사 기록, 첨부 파일
go run ./cmd/rove tenant retire --tenant rove                  # 무엇이 몇 건 사라지고 무엇이 언제까지 남는지만 (아무것도 바꾸지 않음)
go run ./cmd/rove tenant retire --tenant rove --yes            # 실제로 지움: 모든 행과 파일, 바닥값이 걸린 것을 뺀 감사 기록, 마지막으로 조직
go run ./cmd/rove tenant purge --tenant rove --yes             # 감사 기록까지 모두 지움: 바닥값이 걸린 것도
```

**은퇴(retire)와 전체 삭제(purge) 가운데 무엇을 쓰나(payday#115).** 둘 다 조직의 행, 파일, 내장 roster에서 그 워크스페이스에만 있던 계정을 지우고 주소를 보류합니다. 다른 것은 감사 기록입니다.

- **바닥값**(`audit.min`, `audit.by.<종류>.min`)은 배포가 법 때문에 지켜야 하는 최소 보관 기간입니다. 예를 들어 개인정보 보호법은 처리자에게 접속기록을 누구의 정보였든 1년 보관하게 하므로, `deploy/`의 설정은 `audit.by.access.min: {profile: pipa}`입니다.
- **retire**는 바닥값이 아직 걸린 감사 기록을 DB와 보관소에 그대로 두고, 나머지는 지금 지웁니다. 남긴 기록은 보존 패스가 바닥값이 지나는 대로 지우고, 마지막 바닥값이 지나면 남은 것과 그 조직의 키까지 지웁니다. 출력의 `kept until …`이 무엇을 언제까지 남기는지입니다.
- **purge**는 바닥값이 걸린 것까지 모두 지금 지웁니다. 그 조직이 바닥값의 법이 아닌 다른 법이나 계약 아래에 있어 모두 지워야 할 때 씁니다.
- **기본은 retire입니다.** 닫힌 워크스페이스를 파기하는 sweep은 파기하는 순간 효력이 있는 계약을 따릅니다. 계약이 `--purge-floors`를 말하지 않으면(계약이 없을 때도) retire입니다. 줄어드는 보관 기간처럼, purge로 바꾸는 계약은 앞 계약이 retire였다면 배포의 유예(`app.retention.grace`)가 지난 뒤에 적용됩니다. 손으로 하는 `retire`와 `purge`는 이름대로 하고, 계약이 다른 쪽을 말하면 미리 보기에서 알려 줍니다.
- **retire는 보관소가 있어야 합니다.** 남기는 기록과 그 조직이 떠났다는 서명된 기록을 보관소에 두기 때문입니다. 바닥값이 있는데 `audit.archive`가 없으면 아무것도 지우기 전에 거부합니다. 그런 배포에서 sweep은 그 워크스페이스를 닫힌 채로 두고 경고합니다(법적 보존이 걸린 것처럼). 보관소를 두면 다음 sweep이 retire하고, 바닥값을 포기해도 되면 `rove tenant purge --yes`나 `--purge-floors` 계약으로 지웁니다. 바닥값이 없는 배포(`rove.yaml`의 기본)에서 retire는 남길 것이 없어 purge와 같습니다.
- retire가 지운 행은 지웠다는 기록(`rove.workspace.retire`)으로 감사 기록에 남습니다. 바닥값이 걸린 종류의 것만 남고 나머지는 함께 지워지므로, `trail rows`가 그 행들을 `missing`으로 찾지 않습니다.
- retire한 워크스페이스에는 나중에 법적 보존을 걸 수 없습니다. hold는 지금 있는 워크스페이스에 거는 것이고, 워크스페이스의 hold 기록도 함께 지워지기 때문입니다(rove#153). 파기 뒤에 보존 요청이 오면 남은 기록을 암호화된 파일로 꺼내 별도 매체에 보관합니다: [보존 요청 대응](docs/runbook/preserve.md). `rove tenant retired`가 무엇이 언제까지 남는지 보여 줍니다.

내보내기의 `manifest.json`에는 형식(`rove-export/1`)과 모든 파일의 SHA-256이 적히고, 감사 기록의 키(`audit.checkpoints.key`)로 서명한 `signature.json`이 옆에 붙습니다(design D34). 복구는 서명된 내보내기만 받으므로, 복구할 수 있는 내보내기를 내려면 키를 설정해야 합니다. 키가 없는 배포의 내보내기에는 서명이 없습니다. 조직의 데이터를 돌려주는 것은 같지만 복구에는 쓰이지 않습니다.

```sh
go run ./cmd/rove tenant verify --file rove.zip   # 이 배포(또는 audit.checkpoints.trust에 적은 키)가 쓴 그대로인지
```

내보내기에는 세션과 운영자의 hold는 들어가지 않고, 무엇을 왜 뺐는지가 `manifest.json`에 적힙니다. 로그인과 비밀번호는 roster의 것이라 Rove의 내보내기에 없습니다. hold가 걸린 조직은 지우지 않습니다. 사람 한 명의 개인정보 삭제는 사람 화면의 '개인정보 삭제'(가명화)이며, 감사 기록의 사본도 DB와 보관소에서 함께 비웁니다(hold가 걸린 것은 남김). DB 쪽은 삭제와 같은 트랜잭션에서, 보관소 쪽은 그 트랜잭션이 커밋한 뒤에 비우고, 그때 비우지 못한 것은 감사 기록의 다음 패스가 비웁니다. hold가 남긴 것은 hold가 풀린 뒤의 패스가 비웁니다. 아직 비우지 못한 것은 `rove trail owed`가 보여 줍니다(payday#115). 사람의 이름·연락처·사번과 초대 주소, 알림의 글은 감사 기록에 애초에 값으로 남지 않으므로(payday#92), 비우는 것은 그 밖의 것(자유 텍스트, 그 전에 쓴 행)입니다.

내장 roster면 `purge --yes` 가 그 워크스페이스에만 있던 계정을 roster에서도 먼저 파기합니다. 다른 워크스페이스에도 있는 계정은 그 사람의 것이라 남깁니다. roster를 따로 운영하면 계정은 roster의 것이고 Rove는 건드리지 않습니다.

### 워크스페이스 복구 (운영자)

서명된 내보내기로 워크스페이스를 되살립니다. 파기한 배포에도, 다른 배포에도 넣을 수 있습니다(design D34). purge처럼 `--yes` 없이는 무엇을 되돌릴지만 말합니다. 다만 바닥값이 있는 배포에서 **retire한 워크스페이스는 그 배포에 같은 ID로 되살리지 않고 거부합니다**(다른 배포에는 됩니다). 그 배포의 감사 기록이 그 조직을 떠난 조직으로 기억하고 있어서, 되살린 워크스페이스가 쓰는 감사 기록을 보존 패스가 지우고 마지막에는 키까지 지우기 때문입니다. purge한 워크스페이스는 그런 기록을 남기지 않으므로 그대로 되살아납니다. 어느 쪽으로 파기했는지는 주소 보류의 이유(`destroyed`, `retired`)에 남습니다. payday가 떠난 조직을 되돌리는 방법을 갖추면 이 거부는 없어집니다.

```sh
go run ./cmd/rove tenant import --file rove.zip          # 무엇을 몇 건 되돌릴지만 (아무것도 바꾸지 않음)
go run ./cmd/rove tenant import --file rove.zip --yes    # 실제로 복구
```

- `--out -`는 내보내기를 stdout으로 쓰고, `--file -`는 stdin에서 읽습니다(`verify`도 같습니다). 셸도 `tar`도 없는 컨테이너에서 파일을 꺼내고 넣는 방법이고, Kubernetes에서의 순서는 `deploy/k8s/README.md`에 있습니다.
- 서명과 모든 파일의 해시를 먼저 검증합니다. 믿는 키는 이 배포의 키와 `audit.checkpoints.trust`에 적은 키입니다. 다른 배포에서 온 내보내기는 그 배포의 공개 키를 `trust`에 넣어야 받습니다.
- 행은 원래 ID와 시각 그대로 들어갑니다. 그래서 붙여 둔 라벨과 링크가 계속 열립니다. 같은 워크스페이스가 이 배포에 있으면 거부합니다. 오래된 내보내기로 지금 데이터를 되돌릴 수 없습니다. 전부 한 트랜잭션이라, 한 행이라도 들어가지 못하면(다른 워크스페이스가 그 사이 같은 라벨 코드를 쓰는 등) 아무것도 남지 않습니다.
- 주소는 내보내기의 것을 씁니다. 그 워크스페이스가 떠날 때 걸린 보류는 저절로 풉니다. 다른 워크스페이스의 보류가 걸려 있으면 `--reuse`로 풀거나 `--alias`로 다른 주소에 넣습니다. 다른 워크스페이스가 그 주소를 쓰고 있으면 새 주소를 만들어 넣습니다(`--alias`로 정할 수 있음).
- 닫힌 채로 내보낸 워크스페이스는 열린 상태로 돌아옵니다. 닫힌 채 두면 다음 파기 주기에 다시 파기되기 때문입니다.
- 이 빌드가 지운 테이블의 행(예: 사용량 계량이 Rove 밖으로 나가며 지운 `usagesnapshot`, design 7장)은 되살리지 않고, 몇 건을 두고 왔는지 출력과 복구 이벤트(`left=`)에 남깁니다. 이 빌드에 없는 그 밖의 테이블의 행이 있으면 거부합니다.
- 이 roster에 계정이 없는 구성원(다른 배포에서 왔거나, 파기 때 계정도 지워진 구성원)은 계정 없이 복구됩니다. `rove identity migrate`가 그들에게 계정을 줍니다(내장 roster는 새 비밀번호를 한 번 보여 주고, roster를 따로 운영하면 무엇을 만들지 출력합니다).
- 감사 기록은 이 배포의 체인에 넣지 않고, 따로 보관하지도 않습니다. 내보내기 파일이 그 기록이므로 보관해 두세요. 원래 서명으로 계속 검증할 수 있습니다(`tenant verify`). 배포 안에 서명된 사본을 두면, 나중에 그 안의 사람을 개인정보 삭제해도 서명을 깨지 않고는 그 사본에서 지울 수 없습니다.
- 워크스페이스의 이력에 "워크스페이스 복구 (운영자)" 이벤트가 남습니다. 이벤트에는 그 내보내기의 manifest 해시와 서명 키 지문이 적혀서, 어느 파일에서 복구했는지 맞춰 볼 수 있습니다(`tenant verify`도 둘을 출력합니다). 검색 색인은 복구한 자산·모델·사람을 다시 만듭니다.

### 감사 기록에 남는 내용 (운영자)

감사 기록 한 행은 누가, 무엇을, 어느 행에, 언제 했는지에 더해 기본으로 그 쓰기가 쓴 내용(patch)을 남깁니다. 등록(Add)은 만든 행을 빈 행에서 시작하는 patch로 남깁니다. 행 전체(쓰기 뒤의 행)는 남기지 않습니다(payday#92).

```yaml
audit:
  content: patch        # fields | patch | value. 비우면 patch
  by:
    asset:
      content: value    # 이 종류만 행 전체도 남김
```

`fields`는 바꾼 필드의 번호만 남깁니다. 사람의 이름·연락처·사번, 초대 주소, 알림의 글처럼 personal로 선언한 필드는 어느 수준에서도 값으로 남지 않고 바꿨다는 것만 남습니다. 이 설정으로 다시 켤 수도 없습니다.

### 접속기록 (운영자)

개인정보를 읽거나 쓰거나 내려받은 호출마다 접속기록이 한 행씩 남습니다(rove#136, payday#111).
- **남는 것:** 누가, 언제, 어디서(`app.trusted_proxies`를 지난 접속지 주소), 어떤 기능으로, 누구의 정보를, 읽기·쓰기·내려받기 중 무엇을 했는지, 그리고 결과(거부 포함).
- **남기는 때:** 쓰기는 그 쓰기와 같은 트랜잭션에서 남깁니다. 내려받기는 파일을 건네기 전에 남기고, 남기지 못하면 거부합니다. 읽기는 1초씩 모아 남기므로, 프로세스가 갑자기 죽으면 마지막 1초 분이 빠질 수 있습니다.
- **내려받는 이유:** 개인정보가 담긴 내려받기(자산 대장 CSV, 워크스페이스 내보내기 zip)는 이유를 적어야 합니다. 화면에서는 내려받기 전에 묻고, 터미널에서는 `--reason`을 씁니다. 대량 등록 양식은 아무의 정보도 담지 않아 묻지 않습니다.

```sh
go run ./cmd/rove api --reason "10월 자산 실사" asset export
go run ./cmd/rove workspace export --reason "폐업 전 보관" acme
```

- **점검(워크스페이스의 관리자·감사자):** 설정 › 접속기록에서 한 달씩 봅니다. 처음에는 지난달을 엽니다.
  - 구성원(누가), 정보주체(누구의 정보), 종류(조회·수정·내려받기)로 좁혀 볼 수 있습니다.
  - 내려받기는 그 이유가 함께 보입니다.
  - 점검을 마치면 **점검 완료로 기록**합니다. 누가, 언제, 어느 기간을, 그때 몇 건이었는지와 점검 결과가 남고, 작업 기록(`access.review`)에도 남습니다. 점검 기록은 고치거나 지울 수 없습니다.
  - 터미널에서는 `rove api access review`와 `rove api access-review add|ls`입니다.
- **운영자:** `rove access ls`는 배포 전체의 접속기록을, `rove access reviews`는 점검 기록을 보여 줍니다. 이 명령은 이름 없이 ID만 보여 주고, 아무것도 바꾸지 않습니다. 정보주체가 자기 정보의 처리 내역을 물으면 `--subject`로 뽑습니다.

```sh
go run ./cmd/rove access ls --tenant acme --kind download --from 2026-10-01 --to 2026-11-01
go run ./cmd/rove access ls --subject <사람 기록의 ID>
go run ./cmd/rove access reviews --tenant acme
```

- **보존:** 감사 기록의 보존 장치를 그대로 씁니다. `deploy/`의 설정은 `audit.by.access: {profile: pipa-sensitive}`(DB에 90일, 보관소까지 2년)입니다. 법무 검토가 다른 기간을 정하면 그 값을 적습니다.
  - **조직마다:** 1년·2년을 가르는 기준(정보주체 수, 고유식별정보·민감정보)은 조직마다 다르므로, 워크스페이스가 자기 기간을 정합니다(설정 › 워크스페이스 › 기록 보관, rove#144, D36). 정하지 않으면 배포의 기간입니다.
  - **바닥값:** 배포의 바닥값(`audit.by.access.min`, `deploy/`는 `{profile: pipa}`로 1년)보다 짧게는 정할 수 없습니다. 기간을 줄이면 이력처럼 유예(`app.retention.grace`)가 지난 뒤에, 그리고 `app.retention.apply`가 켜져 있을 때만 적용됩니다. hold가 걸린 조직은 아무것도 지우지 않습니다.
- **내보내기:** 워크스페이스 내보내기에 그 워크스페이스의 접속기록이 함께 담깁니다(`trail/access.jsonl`).

### 감사 기록 보관소 확인 (운영자)

`audit.archive` 를 두면 DB에서 나간 감사 기록이 보관소에 쌓이고, 보관소에 무엇이 있어야 하는지는 DB가 따로 기록합니다(payday의 manifest). 감사 기록의 보존 패스는 시작할 때마다 둘을 가볍게 맞춰 보고, 끝날 때 체크포인트를 남깁니다.

```sh
go run ./cmd/rove trail verify          # 보관소를 DB의 기록·최신 체크포인트와 비교 (아무것도 바꾸지 않음)
go run ./cmd/rove trail verify --full   # 모든 청크를 다 읽어 내용이 바뀌지 않았는지까지
go run ./cmd/rove trail accept --of sha256:… --full --why "백업에서 복원한 청크를 확인함"   # 살펴본 뒤, 보관소가 맞다고 DB의 기록을 맞춤
go run ./cmd/rove trail rows            # 지금 DB의 행을 감사 기록이 남긴 지문과 비교 (아무것도 바꾸지 않음)
go run ./cmd/rove trail rows --accept --of sha256:… --why "10-20 이전 작업이 바꾼 것을 확인함"   # 살펴본 뒤, 찾은 행을 그대로 받아들임
go run ./cmd/rove trail owed            # 개인정보 삭제 중 보관소 쪽에서 아직 끝내지 못한 것: 몇 건, 언제부터, 어느 워크스페이스의 hold 때문에
```

**받아들임은 살펴본 것에 대한 것입니다(payday#115).** `verify`와 `rows`는 찾은 것의 다이제스트(`digest sha256:…`)를 출력하고, `verify`는 최근의 받아들임 10개도 보여 줍니다(어느 체크포인트에서, 언제, 누가, 왜, 무엇을 몇 건). `accept --of`와 `rows --accept --of`는 그 다이제스트를 받아 다시 검사하고(`verify --full`이었으면 `--full`), 그 사이 찾은 것이 달라졌으면 거부합니다. 살펴본 뒤에 생긴 것을 함께 받아들이지 않기 위해서입니다. 그러면 다시 `verify`하고 새로 찾은 것을 살펴봅니다. 누가 받아들였는지는 `--by`이고, 주지 않으면 셸의 사용자와 호스트입니다. `accept`의 누가·왜·무엇은 그 받아들임이 끝에 남기는 서명된 체크포인트와 영수증(`accept`)에 남습니다. `rows --accept`의 이유는 로그에 남습니다(감사 기록에는 행마다 받아들였다는 기록만 남음).

`verify` 는 무언가 찾으면 실패로 끝나므로 일정에 걸어 둘 수 있습니다. 체크포인트는 삭제를 거부하는 디스크(`audit.checkpoints.dir`)에 따로 둡니다. 조직 내보내기의 감사 기록도 DB가 기록해 둔 청크만, 내용이 그대로인 것만 읽습니다.

**체크포인트의 증인(payday#115).** 최신 체크포인트는 DB와 체크포인트 저장소만 알기 때문에, 둘을 모두 쓸 수 있는 사람은 최신 것을 지우고 번호를 되감을 수 있습니다. `audit.checkpoints.witnesses`에 적은 증인이 체크포인트마다 둘 바깥에서 들어 둡니다. `log:`는 로그와 게이지(`payday.trail.checkpoint`)로, 경보를 걸어야 증인입니다. `store:<디렉터리>`는 보관소와 다른 디스크에 들은 것을 둡니다. `verify`는 답하는 증인마다 마지막으로 들은 것(`heard by …`)을 보여 주고, 증인이 들은 것과 사슬이 맞지 않으면 찾습니다. 무엇을 하는지는 [런북](docs/runbook/trail-integrity.md#체크포인트의-증인)에 있습니다.

**보관소에는 서명 키가 반드시 있어야 합니다(payday#115).** `audit.archive`를 두고 `audit.checkpoints.key`가 없으면 `serve`와 `trail` 명령이 시작하지 않고, 키를 만드는 방법을 알려 줍니다. 아무도 서명하지 않은 보관소의 기록은 아무것도 증명하지 못하고, 모두가 아는 개발용 키는 그 키가 배포에 닿는 날 누구의 위조에든 서명해 주기 때문에 기본 키는 없습니다.

- 키는 파일 경로로 주거나 `ROVE_AUDIT_CHECKPOINTS_KEY` 에 PEM 문서 자체를 넣습니다.
- 배포의 키는 `openssl genpkey -algorithm ed25519 -out checkpoints.pem`으로 따로 만들어 DB·보관소의 자격 증명과 떨어진 곳에 비밀로 마운트합니다. `deploy/compose`와 `deploy/k8s`가 그렇게 합니다.
- 써 보는 배포에서는 `rove init`이 보관소 옆에 이 배포만의 키(`<archive>-checkpoints.pem`)를 만들고 어디에 썼는지 알려 줍니다. `serve` 전에 그 경로를 `audit.checkpoints.key`에 적습니다. `.gitignore`가 이 파일을 커밋하지 않게 막습니다. 서명하는 키는 커밋하지 않습니다.

같은 키로 DB 기록의 행 하나하나에도 서명하고, 각 체크포인트는 바로 앞 체크포인트를 가리킵니다(payday#58). 그래서 마지막 체크포인트 뒤에 바뀐 행도 드러납니다. 작업은 매번 체크포인트로 끝납니다. 행이 최신 체크포인트와 맞지 않는 동안에는 `accept`할 때까지 체크포인트를 남기지 않고, 보관소를 건드리는 작업(보존 패스, 개인정보 삭제, 계정 탈퇴, 워크스페이스 파기)은 모두 오류로 끝납니다. 서버는 멈추지 않고 로그에 남깁니다. 그때 할 일은 [감사 기록 위변조 감지 대응](docs/runbook/trail-integrity.md)에 있습니다. DB를 백업에서 복원한 뒤에도 `verify`가 `behind`를 찾으며, 살펴본 뒤 `accept`를 한 번 실행하면 이어 갑니다(payday#70).

**키를 이미 쓰던 배포는 이 버전으로 올린 뒤 `accept`를 한 번 실행합니다.** 서명이 없는 행은 서명하기 전에 쓴 것인지, 누가 서명을 지운 것인지 구별할 수 없습니다. 그래서 키가 있으면 그런 행을 모두 찾아냅니다(`unsigned`). 살펴본 뒤 한 번 받아들이면 그다음부터는 깨끗합니다.

```sh
go run ./cmd/rove trail verify                                                 # 그전에 쓴 행마다 `unsigned`, 그리고 다이제스트
go run ./cmd/rove trail accept --of sha256:… --why "행에 서명하기 전에 쓴 것"   # 한 번
go run ./cmd/rove trail verify                                                 # 깨끗함
```

**키 없이 보관소를 쓰던 배포**도 키를 둔 뒤 `accept`를 한 번 실행합니다. 키를 두면 그동안의 행과 체크포인트가 모두 `unsigned`입니다. 그것들을 근거로는 아무것도 파기하지 않고, 그 뒤로는 체크포인트도 남기지 않습니다. 각 작업은 할 일을 한 뒤 오류로 그렇다고 알립니다(`no trusted key signed the checkpoints the store keeps`). 살펴본 뒤 `accept`를 실행하면 지금의 행에 그대로 서명합니다. 그리고 DB가 마지막으로 번호를 매긴 체크포인트를 앞의 것으로 삼아 키 이전의 기록을 이어 받습니다. 행에 서명하고 나면 그 행이 가리키는 청크도 비교하게 되는데, 거기서 새로 찾은 것(서명 없던 행이 있다고 한 청크가 없음 등)은 살펴본 것이 아니라서 함께 받아들이지 않습니다. 그다음 `verify`가 찾고, 따로 한 번 더 받아들입니다. 그래서 `accept`가 두 번 필요할 수 있습니다. 이전 키를 믿지 않은 채 키를 바꾼 배포도 같은 말을 듣습니다. 그때는 `accept` 대신 이전 키의 공개 키를 `audit.checkpoints.trust`에 넣습니다.

**행의 지문(payday#102, #108).** `audit.digest.key`를 두면 감사 기록이 쓰기마다 그 쓰기가 남긴 행의 지문(HMAC)을 함께 남깁니다.
- `trail rows`는 지금 DB의 행마다 지문을 다시 내어 그 행에 대한 마지막 감사 기록과 비교합니다. 지운(soft erase) 행도 비교합니다. 감사 기록이 보관소로 옮겨간 행은 보관소에서 찾습니다. 아래 가운데 무엇이든 찾으면 행마다 적고 실패로 끝나므로 일정에 걸어 둘 수 있습니다(payday#115).
  - `differs`: 앱을 거치지 않고 DB에서 직접 바뀐 행
  - `forged`: 지문이 그 감사 기록 줄의 것이 아닌 행. 다른 줄의 지문을 옮겨 적으면 이렇게 보입니다.
  - `no-digest`: 지문을 남기기 시작한 뒤에 쓴, 지문 없는 감사 기록
  - `not-on-trail`: 감사 기록 어디에도 없는 행. DB에 직접 넣은 행이 이렇게 보입니다.
  - `key-not-held`: 이 검사에 주지 않은 키로 만든 지문. `unheld-key` 줄이 그 키의 지문(fingerprint)을 알려 줍니다.
  - `missing`: 감사 기록에는 있다는데 DB에 없는 행. 서버를 거치지 않고 지운 행이 이렇게 보입니다.
- 감사 기록에서 뺀 종류는 매번 `not checked:` 줄로 무엇을 읽지 않았는지 알려 줍니다.
- 다른 키로 검사하거나 키 없이 쓰던 배포를 검사하면, 통과하지 않고 모든 행을 찾아냅니다. 키를 둔 때(`audit.digest.since`)와 버린 키(`audit.digest.retired`)를 적으면 그 행들은 찾지 않고 셉니다.
- Rove가 서버를 거치지 않고 일부러 쓰거나 지우는 행은 그 일을 한 행위로 감사 기록에 남깁니다. 그래서 찾을 것이 아닙니다.
  - 쓰는 것: 워크스페이스 복구(`tenant import`)가 되살린 행과 그때 되찾은 주소 보류, 알림 메일 발송, 운영자가 앞당겨 푼 주소 보류, 개인정보 삭제가 바꾼 구성원의 이름, 마이그레이션이 payday의 과거로 옮긴 자산의 사실(`rove.migrate.facts`)
  - 지우는 것(`pd.RecordErased`): 보존 기간이 지난 이력, 터미널 로그인
  - 워크스페이스 파기: purge는 그 워크스페이스의 감사 기록도 함께 파기하므로 남길 것이 없습니다. retire는 지운 행마다 지웠다는 기록(`rove.workspace.retire`)을 남기고, 바닥값이 걸린 종류의 것만 그 감사 기록과 함께 남습니다.
- 서버가 쓰지 않는 종류는 스키마에 둘 중 하나를 적습니다. 적지 않으면 생성(`pd gen`)이 거부합니다.
  - 쓸 때마다 행위로 기록: 터미널 로그인, 별칭 보류. 비교합니다. 동의는 roster의 것이라 roster의 감사 기록에 있습니다(D37).
  - 감사 기록에서 뺌(이유를 적음): 세션, 로그인 실패, 검색 대기열, 트리 잠금. 비교하지 않습니다.
- 마이그레이션이 바꾼 행처럼 살펴보고 맞다고 판단한 것은 `--accept --of <다이제스트> --why`로 받아들입니다. 그 사이 찾은 것이 달라졌으면 거부합니다. 찾은 행마다 받아들였다는 기록이 감사 기록에 남고(없어진 행은 없어진 것으로), 다이제스트와 이유는 로그에 남습니다. `forged`와 `key-not-held`는 받아들이지 않습니다. 그 줄을 살펴보거나 키를 줍니다.
- 키는 `openssl rand -base64 32`로 만들어 비밀로 마운트하거나, `ROVE_AUDIT_DIGEST_KEY`에 `base64:`와 키를 넣습니다. DB와 DB 백업에는 넣지 않습니다.
- 바꾼 키의 예전 것은 `audit.digest.previous`에 둡니다.
- 지문은 개인정보 필드를 뺀 모든 필드를 덮습니다. 비밀 필드도 덮습니다. 개인정보 필드는 행 옆의 DB에만 두는 **봉인**(`rowseal`)이 덮습니다(payday#115). 봉인은 내보내기에 담지 않고, 개인정보 삭제가 "잊었음"이라는 봉인으로 바꿉니다. 잊은 사람의 행은 `N forgotten`으로 셉니다.
- **이 버전으로 올린 뒤** 봉인하기 전에 쓴 행(사람·구성원·초대·알림)은 `N rows from before seals`로 셉니다. 찾을 것은 아니지만 그 개인정보 필드는 아무것도 보증하지 않으므로, 살펴본 뒤 `rows --accept --of <다이제스트> --why`를 한 번 실행해 봉인합니다. 다른 찾은 것이 없어도 받아들입니다([런북](docs/runbook/trail-integrity.md#행의-봉인과-올린-뒤-한-번의-받아들임)).
- 키를 두기 전에 쓴 행은 `audit.digest.since`에 키를 둔 때를 적으면 비교하지 않고 따로 셉니다. 받아들인 뒤에는 지웁니다.

## 무엇이 있나

| 화면 | 할 수 있는 일 |
| --- | --- |
| 대시보드 | 현황 숫자, 내 자산·예약, 승인 대기, 연체, 재고 부족, 알림 |
| 자산 | 검색·필터, 등록, **대량 등록**(rove 양식의 CSV), CSV 내보내기, 라벨 인쇄 |
| 자산 상세 | 이동(지난 일은 그 시점으로), 정보·상태 변경, 소유·관리 담당, 키트·그룹 연결, 지급, 고장 신고, 첨부, 라벨, **이력과 정정**, 플러그인(랙 배치도, 보증) |
| 공간 | 공간 트리, **과거 어느 시점의 모습**, 두 시점 사이 변화 |
| 예약 | 주간 캘린더, 반복 예약, 임시 홀드, 승인, 체크인, 노쇼 자동 해제, 장비 수령 |
| 지급·대여 | 지급/대여, 인수 확인, 부분 반납, 기한 연장, 연체 알림 |
| 재고 | 입고·사용·조정·이동, 자산으로 전환, 부족 알림, 내역 |
| 실사 | 범위 지정, 카메라/스캐너로 스캔(오프라인이면 모아 두었다 전송), 대조, 위치 반영·분실 처리 |
| 작업 | 수리·점검·정비, 예약 막기, 정기 점검 반복, 구성원의 고장 신고 |
| 구매 | 주문, 입고 시 자산이나 재고로 등록 |
| 사람 | 조직도, 역할, 로그인 중지, 개인정보 삭제, 로그인 발급·비밀번호 재설정(내장 roster일 때) |
| 설정 | 유형과 속성, 모델, 예약 자원, 도메인과 라벨, 작업 기록, 감사 기록 |

**오프라인.** 연결이 끊겨도 앱이 열리고, 이 브라우저에서 마지막으로 본 화면을 그대로 보여 줍니다(위에 '오프라인 — 마지막으로 본 내용'). 오프라인에서는 읽기만 하고 저장은 이유와 함께 거절하며, 실사 스캔만 모아 두었다가 연결되면 보냅니다. 앱 자체는 서비스 워커(`ts/sw.js`)가 보관합니다.

- **누구의 것으로 보관하나**: 본 내용(IndexedDB)과 보내지 못한 스캔은 계정과 워크스페이스별로 보관합니다(`ts/src/offline.ts`). 화면은 모든 호출에 자기가 누구의 것인지(`payday-identity`, `/account`의 `identity`)를 실어 보냅니다. 같은 브라우저의 다른 탭에서 다른 사람이 로그인하면, 남겨진 화면의 호출은 그 사람으로 처리되지 않고 거절됩니다. 그러면 화면은 지금 누가 로그인했는지 다시 묻고 그 사람으로 다시 시작합니다.
- **로그아웃하면**: 이 브라우저에 보관한 것을 모두 지우고, 같은 주소의 다른 탭도 함께 로그인 화면으로 돌아갑니다.
- **세션이 저절로 끝나면**(유휴, 쿠키를 지움 등): 로그아웃이 아니므로 보내지 못한 스캔은 남겨 둡니다. 같은 사람이 다시 로그인하면 그때 보냅니다. 본 내용과 마지막 로그인 정보는 지웁니다(다시 로그인하면 다시 읽습니다).
- **다른 사람이 로그인하면**: 앞 사람의 것은 본 내용도, 보내지 못한 스캔도 남기지 않습니다. 앞 사람의 스캔이 다음 사람으로 보내지는 일은 없습니다.

**대량 등록.** 새 자산 여러 개를 rove 양식의 CSV로 한 번에 등록합니다(design 9.13). 자산 화면의 '대량 등록'에서 양식을 내려받아 한 줄에 자산 하나씩 적고 CSV로 저장해 올리면, 무엇이 등록될지(정해 줄 태그, 새로 만들 유형·모델)와 문제를 줄마다 먼저 보여 주고, 문제가 없을 때 등록합니다. 터미널에서는 `rove register`입니다.

- 새 자산만 만듭니다. 이미 있는 태그는 고치지 않고 문제로 알립니다.
- 열은 이름(필수), 종류, 유형, 모델, 제조사, 시리얼, 태그(비우면 정해 줌), 위치태그, 취득일, 기준일(입고일), 설명, 상태, 컨디션, `속성.키`입니다. 열마다 적는 것은 '대량 등록' 화면의 '양식'에 있습니다.
- 엑셀 파일(.xlsx, .xls)은 받지 않습니다. 엑셀에서 적었으면 저장할 때 파일 형식을 'CSV UTF-8(쉼표로 분리)'로 고릅니다. 한글 엑셀의 'CSV(쉼표로 분리)'(CP949)도 읽습니다. 서버는 모든 워크스페이스가 함께 쓰는 프로세스라서, 누가 올린 엑셀 파일을 열지 않습니다. 내보내기도 CSV뿐이라 rove에는 엑셀 파일을 다루는 코드가 없습니다(design 9.13).
- 시리얼·태그·속성이 숫자뿐이면 그 열의 셀 서식을 텍스트로 바꾼 뒤 적습니다. 엑셀이 긴 숫자를 3.57951E+14처럼 줄여 저장했으면 그 줄을 문제로 알리고 등록하지 않습니다.
- 위치는 이미 있는 공간의 태그로 적습니다. 공간을 새로 만들지 않습니다. 사용자(지급)는 받지 않으니, 등록한 뒤 지급합니다.
- 없는 이름의 모델은 새로 만듭니다. 없는 유형은 관리자가 등록할 때만 만들고, 매니저에게는 문제로 알립니다.
- 등록은 작업 하나입니다. 이력에 "대량 등록: N개"(누가, 무슨 사유로)가 남고, 자산마다 그 아래 "등록"이 남습니다. 한 번에 5,000행까지 받습니다.

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
PDTEST_POSTGRES='postgres://rove:rove@localhost:5432/rove?sslmode=disable' go test ./...   # PostgreSQL (공식 postgres:18 이미지)
PDTEST_OPENSEARCH=http://127.0.0.1:9200 go test ./...           # 검색 색인까지 (deploy/opensearch; TLS면 https://user:pass@... 와 PDTEST_OPENSEARCH_CA)
cd ts && npm run check                                           # 타입 검사
```

도메인 테스트는 실제 호출과 같은 경로(게이트, 테넌트 벽, 도메인 계층)를 지나고, `cmd` 테스트는 gRPC 체인(세션 쿠키, 역할 표, 배치) 전체를 지납니다.

### 브라우저 테스트 (Playwright)

`ts/e2e`는 주요 흐름을 빌드된 화면에서 사람처럼 따라갑니다: 로그인(비밀번호, 그리고 roster의 발급자를 거치는 SSO), 자산 등록·검색·이동, CSV 내보내기, 라벨 인쇄와 코드 스캔, 카메라로 라벨 스캔, 지급·인수 확인·반납(부분 반납 포함, 자산 화면에서 시작하는 지급), 구성원이 라벨을 스캔해 하는 인수 확인, 예약 승인, 실사 스캔, 고장 신고, 구매와 입고, 재고 사용·입고, 워크스페이스 만들기와 전환, 닫기·내려받기·복구, 초대 보내기와 수락·거절, 약관 동의, 터미널 로그인 승인.

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

PostgreSQL에서 돌리려면 `E2E_POSTGRES`에 데이터베이스를 만들 수 있는 계정으로 그 서버의 주소를 줍니다. 서버마다 Rove와 roster의 데이터베이스를 하나씩(`e2e_rove_<포트>`, `e2e_roster_<포트>`) 그 서버에 두고, 실행할 때마다 지우고 새로 만듭니다(`ts/e2e/postgres.mjs`). CI는 SQLite와, `go` 잡과 같은 PostgreSQL(공식 `postgres:18`) 둘 다에서 돌립니다.

```sh
docker run -d --rm --name rove-e2e-pg -p 127.0.0.1:55432:5432 \
  -e POSTGRES_USER=rove -e POSTGRES_PASSWORD=rove -e POSTGRES_DB=rove postgres:18
E2E_POSTGRES='postgres://rove:rove@127.0.0.1:55432/rove?sslmode=disable' npx playwright test
docker stop rove-e2e-pg
```

Playwright 이미지로 돌릴 때는 앞의 Playwright `docker run`에 `--network host`와 `-e E2E_POSTGRES=...`를 더해, 컨테이너 안에서도 그 주소가 닿게 합니다.

검색을 OpenSearch로 돌리려면 `E2E_OPENSEARCH`에 클러스터 주소를 줍니다. TLS면 `https://사용자:비밀번호@…`로 주고, CA는 `E2E_OPENSEARCH_CA`에 줍니다. 서버마다 자기 인덱스(`e2e-<포트>-…`)를 쓰고, 뜨기 전에 지운 뒤 자기 DB로 다시 만듭니다. CI의 `e2e (sqlite, opensearch)`가 이렇게 돕니다(`scripts/opensearch.sh`). 개발용 클러스터는 위의 "검색 엔진"에 있습니다.

```sh
E2E_OPENSEARCH=http://127.0.0.1:9200 npx playwright test
```
