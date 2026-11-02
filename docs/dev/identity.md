# 계정과 로그인 (roster)

**계정**은 [roster](https://github.com/lesomnus/roster)의 것이고, **워크스페이스**는 Loft의 것입니다(design 9.10, D27). 계정과 비밀번호, 로그인할 수 있는지는 roster가 정하고, 계정 하나가 여러 워크스페이스에 속할 수 있습니다. 워크스페이스마다의 역할은 Loft의 사람 화면에서 정합니다.

- roster의 테넌트는 계정이 들어 있는 **디렉터리**입니다. 배포마다 하나입니다(`auth.roster.directory`).
- Loft의 테넌트는 **워크스페이스**입니다. 계정이 워크스페이스에 속한 것이 구성원 자격(`Holder`)이고, 자기 ID와 roster 계정(`account`), 역할을 갖습니다.

화면에서 하는 일은 [관리자·소유자 안내서](../guide/admin.md), 서버 상자에서 하는 일은 [운영자 안내서](../guide/operator.md#계정-roster)에 있습니다.

## 내장 roster

기본은 roster를 Loft 프로세스 안에서 함께 띄우는 **내장** 모드라 따로 설치할 것이 없습니다. roster의 DB는 Loft의 SQLite 파일 옆 `data/roster.db` 이고(PostgreSQL이면 `auth.roster.db`), 계정은 그 안의 디렉터리 `loft` 에 있습니다. Loft는 그 roster를 `bufconn` 으로 부릅니다.

- `loft init`, `loft holder`, 사람 화면의 로그인 발급과 비밀번호 재설정은 이 roster에 계정을 만들고 비밀번호를 정합니다.
- 사람 화면의 로그인 발급은 내장 roster에 계정을 만들고 그 워크스페이스의 구성원으로 넣습니다. 비밀번호 재설정은 그 계정이 이 워크스페이스에만 있을 때 할 수 있습니다(다른 워크스페이스의 사람을 이 워크스페이스의 관리자가 막을 수는 없으니까요).

**운영처럼 보이는 배포에서 내장 roster를 쓰면 `loft serve` 가 시작할 때 경고합니다.** 거부하지는 않습니다. 운영처럼 보인다는 것은 DB가 PostgreSQL(`db.driver: pgx`)이거나, `app.public_url` 이 이 기계(`localhost`, `*.localhost`, 루프백 주소)가 아닌 경우입니다. 경고에는 둘 중 무엇 때문인지가 함께 나옵니다.

- 내장 roster는 서버 한 대의 배포를 간단하게 하려는 것입니다(design 9.10, CE).
- 그 프로세스만 roster에 닿고, 사람과 비밀번호가 Loft의 데이터 옆에 있으며, 다른 앱은 그 계정을 쓸 수 없습니다.
- 서버 한 대를 넘는 배포(SaaS, Dedicated, 여러 대)는 roster를 따로 운영합니다.
- 서버 한 대의 CE를 PostgreSQL과 자기 도메인으로 운영한다면 이 경고는 그대로 두어도 됩니다.

## roster를 따로 운영할 때

`auth.roster.addr` 에 주소를, `auth.roster.key` 에 키를, `auth.roster.directory` 에 계정이 있는 roster 테넌트를 줍니다.

- SaaS의 Loft는 roster 운영자가 만든 배포 키(`rk_…`)를 쓰고, 계정은 모두 그 디렉터리 하나에 있습니다.
- 한 조직이 직접 운영하는 Loft는 그 조직의 키(`rt_…`)를 쓰고, 디렉터리는 그 키의 테넌트입니다.
- 이때 `loft init`, `loft holder`, 사람 화면의 로그인 발급·비밀번호 변경은 거부되고 roster에서 합니다.
- 내장 roster에서 옮기는 순서와 Kubernetes에서 키를 붙이는 곳은 [`deploy/k8s`](../../deploy/k8s/README.md)의 13에 있습니다.
- roster의 박스에서 `tenant`, `role` 같은 명령을 할 때는 그 명령을 하는 운영자를 `--as <운영자>` 로 밝힙니다(control plane이 있는 roster, roster#221). roster의 접속기록이 그 사람을 남기고, 밝히지 않으면 거부됩니다. `app install` 과 `control key add` 에는 필요 없습니다.

```sh
# roster 운영자: 배포 키를 만들고(rk_… 출력), 디렉터리에 Loft를 설치
roster control key add --allow /roster.NominationService/List loft
roster --as <운영자> tenant add @accounts '{"name": "Loft accounts"}'
roster app install --tenant accounts --role /roster.VouchService/Verify,/roster.HolderService/Get,/roster.TenantService/Get,/roster.MeService/Get,/roster.EmailService/List,/roster.EmailService/Get,/roster.SyncService/Watch,/roster.ConsentService/Agree,/roster.ConsentService/Settled,/roster.ConsentService/List loft

# Loft
LOFT_AUTH_ROSTER_ADDR=roster.example.com:443 LOFT_AUTH_ROSTER_DIRECTORY=accounts \
LOFT_AUTH_ROSTER_KEY=env:ROSTER_KEY ROSTER_KEY=rk_… loft serve
```

### Loft가 roster에 묻는 것

위 `--role` 의 목록이 Loft가 roster에 묻는 메서드 전부입니다(`identity.AgentMethods`).

- `VouchService/Verify`: 비밀번호 로그인. `HolderService/Get`, `TenantService/Get`, `MeService/Get`: 계정과 디렉터리를 읽습니다.
- `EmailService` 의 두 권한은 초대(D30)를 위한 것입니다. 초대장은 roster가 확인한 주소를 가진 계정의 초대함에 보이고, Loft는 그 주소를 이 권한으로 읽습니다.
- `SyncService/Watch` 는 roster의 동기화 스트림입니다. roster가 계정을 중지·삭제하거나 모든 곳에서 로그아웃시키면 Loft가 그 자리에서 세션을 끝내고, 삭제된 계정을 떠나는 중으로 표시합니다. roster가 디렉터리를 닫으면(`roster tenant close`, roster#105) 그 디렉터리의 모든 계정의 세션이 끝나고, 닫혀 있는 동안은 아무도 로그인하지 못합니다. 아무것도 지우지 않으므로 디렉터리를 복구하면 다시 로그인할 수 있습니다.
- `ConsentService` 의 세 권한은 약관 동의(`app.consent`, D37)를 위한 것입니다. Loft는 계정이 무엇을 빚졌는지 묻고(`Settled`), 동의를 기록하고(`Agree`), 동의한 문서를 읽습니다(`List`). Loft가 묻는 문서는 모두 필수라서 철회(`Withdraw`)는 쓰지 않습니다.

### 계정 페이지

`auth.roster.account_url` 에 roster의 계정 페이지(디렉터리의 front door, 예: `https://id.example.com/`)를 주면, 내 정보 화면에 "로그인 방법 관리"와 "계정 탈퇴" 링크가 생깁니다. 탈퇴 링크는 그 페이지의 `#leave` 로 가고, 계정 삭제는 roster에서 합니다(`MeService.Leave`, roster#106). 삭제는 디렉터리가 허용할 때만 되므로, SaaS의 디렉터리는 roster 운영자가 `TenantConfig.leave` 를 켭니다(관리 콘솔의 테넌트 화면, 또는 resources 파일의 `config.leave`). 삭제된 계정은 위의 스트림으로 Loft에 들어와 떠나는 중이 됩니다.

### 역할 넓히기 (`roster role patch`)

이 권한들이 없던 때 설치한 배포, 그리고 내장 roster에서 옮긴 뒤에 Loft가 더 묻게 된 배포는 역할을 넓혀야 합니다. `app install` 을 다시 실행해도 이미 있는 역할은 그대로 두므로(roster `docs/apps.md`), roster 운영자가 역할을 직접 고칩니다. 목록을 통째로 바꾸므로 전체를 줍니다.

역할의 이름은 Loft가 그 디렉터리에 들어온 길을 따릅니다. `roster app install` 로 설치한 배포는 `loft-app` 이고, 내장 roster에서 옮긴 배포는 내장 roster가 만든 `loft` 입니다(디렉터리를 따로 적지 않았다면 디렉터리도 `loft` 라 `@loft/loft`).

```sh
# roster app install로 설치한 배포 (위의 예: 디렉터리 accounts)
roster --as <운영자> role patch @accounts/loft-app '{"methods": ["/roster.VouchService/Verify", "/roster.HolderService/Get", "/roster.TenantService/Get", "/roster.MeService/Get", "/roster.EmailService/List", "/roster.EmailService/Get", "/roster.SyncService/Watch", "/roster.ConsentService/Agree", "/roster.ConsentService/Settled", "/roster.ConsentService/List"], "date_updated_force": true}'
# 내장 roster에서 옮긴 배포 (디렉터리 loft)
roster --as <운영자> role patch @loft/loft '{"methods": ["/roster.VouchService/Verify", "/roster.HolderService/Get", "/roster.TenantService/Get", "/roster.MeService/Get", "/roster.EmailService/List", "/roster.EmailService/Get", "/roster.SyncService/Watch", "/roster.ConsentService/Agree", "/roster.ConsentService/Settled", "/roster.ConsentService/List"], "date_updated_force": true}'
```

고치기 전까지는 이렇습니다.

- 초대 메일의 링크로 받은 초대장만 초대함에 보입니다.
- 세션은 다음 호출에서(30초 안에) 끝나며, 삭제된 계정은 떠나는 중으로 표시되지 않습니다.
- 약관 동의를 묻는 배포는 계정이 동의했는지 알 수 없으므로 모든 계정의 호출이 거부됩니다(503, 서버 로그에 `roster role patch` 가 적힙니다).

스트림을 거부당한 Loft는 서버 로그에 오류를 한 번 남기고 10분마다 다시 청하고, 동의 원장은 다음 호출에서 다시 물으므로, 역할을 고친 뒤 다시 시작하지 않아도 됩니다. 조직의 키(`rt_…`)로 붙은 배포는 키가 허용한 메서드에도 들어 있어야 하므로 키를 새로 만듭니다.

## 로그인과 세션

- 로그인은 아이디나 이메일로 하고, 어느 워크스페이스인지는 묻지 않습니다. 이메일은 내장 roster면 사람 기록의 이메일이고, 외부 roster면 roster에서 확인된 주소입니다. 2단계 인증을 켠 사람은 비밀번호만으로 들어올 수 없고, SSO로 들어옵니다.
- 브라우저는 `POST /session` 으로 로그인합니다(`server/session`). 비밀번호는 roster의 `VouchService.Verify` 로 가고, 세션은 **계정**의 것입니다.
- 호출마다 어느 워크스페이스인지를 `loft-workspace` 헤더로 밝힙니다(화면은 `/@<워크스페이스>` 주소에서 저절로). 워크스페이스가 하나뿐인 계정은 밝히지 않아도 됩니다.
- 로그인한 사람이 roster에서 중지·삭제되거나 "모든 곳에서 로그아웃"되면 Loft의 세션도 끝납니다. 외부 roster면 roster가 말하는 즉시(roster의 동기화 스트림), 그렇지 않아도 30초 안에 끝납니다. roster에 물을 수 없으면 로그인과 세션이 거부됩니다(503).
- 처음 로그인한 계정이 어느 워크스페이스에도 없으면 자기 워크스페이스가 하나 생깁니다(`tenancy.Workspaces.Arrive`). 그 뒤로 만드는 워크스페이스는 `POST /account/workspaces` 이고, `GET /account` 가 계정이 속한 것을 답합니다. 이 둘이 RPC가 아니라 HTTP인 것은, 모든 RPC가 어느 워크스페이스의 구성원 자격으로 처리되는데 이것들은 그 전에 묻기 때문입니다.
- 터미널의 로그인(`loft login`)은 브라우저에서 승인하는 같은 계정의 세션입니다([터미널과 비서](../guide/terminal.md)). 로그인 실패와 터미널 로그인의 제한은 [보안](security.md#로그인-제한)에 있습니다.

## SSO

**SSO로 로그인할 때**(SaaS, D28)는 roster의 issuer(Hydra와 Login App)를 거칩니다. 로그인 화면에 Google·Microsoft·GitHub 버튼이 생기고, 누르면 roster를 잠깐 지나 그 IdP에서 로그인한 뒤 돌아옵니다. 돌아온 계정은 비밀번호로 들어온 계정과 같고, 처음이면 개인 워크스페이스가 생깁니다. 로그아웃은 issuer의 세션까지 끝냅니다.

```yaml
auth:
  sso:
    issuer: https://id.loft.example      # roster 앞의 Hydra
    client_id: loft
    client_secret: env:LOFT_SSO_SECRET
    providers:                           # 버튼: roster 디렉터리의 연결(Connection) 이름
      - { connection: google, label: Google, mark: google }
      - { connection: microsoft, label: Microsoft, mark: microsoft }
      - { connection: github, label: GitHub, mark: github }
  sso_only: true                         # 비밀번호 로그인을 끈다
```

Loft는 roster `docs/relying-party.md` 의 `itself` 모양을 따르는 relying party입니다(`server/sso`). `GET /sso/login?with=<연결>` 은 브라우저를 그 연결을 이름 붙인 Hydra로 보내고, `GET /sso/callback` 은 코드를 바꿔(state는 쿠키에, nonce, PKCE, 비밀은 헤더에) 같은 계정 세션을 만들고, ID 토큰을 그 옆에 둡니다. `POST /sso/logout` 이 그것을 `id_token_hint` 로 돌려줍니다. `GET /sso` 는 로그인 화면이 그리는 버튼입니다. `auth.sso_only` 면 `POST /session` 은 403입니다.

roster 쪽에 필요한 것:

- **Hydra 클라이언트**: `token_endpoint_auth_method: client_secret_basic`, `redirect_uris: ["<app_url>/sso/callback"]`, `post_logout_redirect_uris: ["<app_url>/"]`, `scope: openid`.
- **디렉터리에 앱 호스트의 `Host` 행**: Login App은 돌아갈 주소의 호스트로 디렉터리를 찾습니다(`roster host add` 에 `{"tenant": {"alias": "accounts"}, "name": "loft.example"}`).
- **디렉터리의 연결** `google`·`microsoft`·`github`: roster `docs/operating.md` 의 *GitHub*, *Any Microsoft account*. 버튼이 이름 붙인 연결로 Login App이 바로 보내는 것은 roster#103부터이고, 디렉터리에 없는 이름이면 roster의 로그인 화면이 나옵니다.
- **누구나 가입**하게 하려면 Login App의 `login.enrol: enrolling`.

테스트는 `internal/oidctest` 의 issuer로 로그인합니다([테스트](testing.md)).

## 약관 동의

`app.consent.terms` 와 `app.consent.privacy` 에 버전과 문서 주소를 주면, 계정은 처음 들어올 때(그리고 버전이 바뀔 때마다) 동의 화면을 봅니다(design 9.11).

- 동의하기 전에는 모든 호출이 거부되고, 터미널에는 `loft consent`(무엇에 동의해야 하는지와 주소)와 `loft consent --agree` 가 있습니다.
- 개인 워크스페이스는 동의한 뒤에 생깁니다. 동의하지 않아도 자기 워크스페이스의 데이터는 내려받을 수 있습니다.
- 문서 내용은 운영자가 따로 게시합니다. 버전을 비워 두면(기본값) 아무것도 묻지 않고, roster에도 묻지 않습니다.
- 동의 기록은 roster의 동의 원장(`ConsentService`)에 있습니다(D37). Loft의 설정에는 어느 문서의 어느 버전이 시행 중인지만 있고, 계정이 어느 문서의 어느 버전에 언제, 화면과 터미널 중 어디서 동의했는지는 roster가 고치지 않는 행으로 남깁니다(내 정보 › 동의한 문서). 동의는 roster의 감사 기록에도 남습니다.
- 호출마다 roster에 동의했는지 묻습니다. 동의했다는 답은 30초 동안 믿고, 동의하지 않았다는 답은 매번 다시 묻습니다. roster에 물을 수 없으면 호출이 거부됩니다(503).
- 계정이 roster에서 삭제되어도 동의 기록은 증거로 남고, roster의 `consent.keep` 이 지나면 roster가 지웁니다. 내장 roster면 그 기간이 `auth.roster.consent_keep`(예: `26280h`)입니다. 비워 두면(기본값) 지우지 않습니다. 기간은 법률 검토 뒤에 정합니다(loft#35).
- 이 판 전에 Loft가 직접 갖고 있던 동의 기록은 마이그레이션이 지우고, 계정은 다음 화면에서 한 번 더 동의합니다. 예전 동의는 Loft의 감사 기록(`loft.consent.agree`)에 누가 언제 했는지만 남고, 배포 전체의 보존 기간을 따릅니다(따로 두려면 `audit.by.d44`). `app.consent.keep` 이나 `audit.by.consent` 를 적어 둔 설정은 서버가 시작하지 않으므로 지웁니다.

## 초대

워크스페이스의 소유자·관리자가 이메일 주소를 자기 역할까지의 역할로 초대합니다(D30). 화면의 흐름은 [관리자·소유자 안내서](../guide/admin.md#사람-들이기)에 있습니다.

- 초대는 roster가 아니라 Loft에 있습니다. 초대장은 roster가 그 주소를 확인된 것으로 갖고 있는 계정의 초대함에 보이고, 확인되지 않은 계정은 메일의 1회용 링크로 받습니다(`InvitationService.Claim`).
- **초대는 계정으로 처리하는 유일한 호출입니다.** 초대받은 계정은 아직 그 워크스페이스의 구성원이 아니므로, `InvitationService` 의 `Inbox`, `Claim`, `Accept`, `Deny` 는 구성원 자격이 아니라 계정으로 불립니다. 정책은 그런 호출자에게 그 메서드만 줍니다.
- 답은 그 워크스페이스에서 배포가 한 일로 씁니다. 기다리는 초대는 30일 뒤 만료됩니다.
- 메일은 `app.mail`(SMTP)로 보냅니다. 비워 두면 보내지 않고 메일 내용과 링크를 서버 로그에 남깁니다(`loft.yaml` 의 예시 참고). 초대 메일의 링크는 언제나 서비스 주소입니다.

## 계정이 떠날 때

- roster에서 삭제된 계정은 그때부터 속한 모든 워크스페이스에서 **떠나는 중**이 됩니다(`Holder.date_leaving`, 삭제된 시각).
- roster의 삭제에는 되살릴 수 있는 유예가 있고(`holder.forget_after`, 기본 30일), Loft도 그만큼 기다립니다(`app.accounts.forget_after`, roster와 같게 둡니다).
- 유예가 지나도 roster가 계정을 되살리지 않았으면 그 계정은 **모든 워크스페이스에서 떠납니다**. 워크스페이스마다 개인정보 삭제와 같은 일이 일어나고, 그 계정이 마지막 소유자였던 워크스페이스는 남은 사람 중 역할이 가장 높은 사람(관리자 > 매니저 > 구성원, 감사자는 제외), 같은 역할이면 먼저 들어온 사람이 소유자가 됩니다. 넘길 사람이 없으면 소유자 없이 남고 서버 로그에 남깁니다.
- 되살린 계정은 다음 확인(1시간마다, `app.accounts.every`) 때 원래대로 돌아옵니다.
- roster의 `restore` 와 `forget` 은 스트림에 실리지 않습니다. 그래서 표시에 따라 무엇을 하기 전에는 언제나 roster에 다시 묻습니다(`Lifecycle.Sweep`).
- roster의 동기화 스트림은 끊긴 사이의 일을 다시 보내지 않습니다. Loft가 듣고 있지 않을 때 삭제된 계정은 서버가 하루에 한 번(`app.accounts.reconcile`, 기본 24h, 0이나 음수면 끔) roster에 모든 계정을 다시 물어 찾아내고, 지금부터 떠나는 중으로 표시하고, 무엇을 표시했는지 서버 로그에 남깁니다(`Lifecycle.Reconcile`). roster가 계정을 하나도 모른다고 답하면 디렉터리나 키가 잘못된 것으로 보고 아무것도 표시하지 않습니다.
- 바로 잊어 달라는 요청과 지금 다시 맞추는 일은 운영자가 `loft identity depart`, `loft identity reconcile` 로 합니다([운영자 안내서](../guide/operator.md#계정을-지워-달라는-요청)).
- 워크스페이스의 구성원 자격만 끝내는 것(로그인 중지, 개인정보 삭제)은 계정을 건드리지 않습니다.

## roster 이전의 배포

roster 이전에 만든 `data/` 는 로그인이 Loft의 DB에만 있어서, 업그레이드한 뒤에는 그대로 로그인할 수 없습니다. 한 번 옮깁니다.

```sh
loft identity migrate                       # 사람마다 로그인과 새 비밀번호를 한 번 출력
loft identity migrate --password demo1234   # 체험용 배포라면: 모두 같은 비밀번호로
```

- 로그인마다 같은 ID로 내장 roster에 계정이 생기므로 이력에 남은 행위자는 그대로입니다.
- 두 워크스페이스에 같은 아이디가 있었다면 하나는 `kim-beta` 처럼 워크스페이스 이름이 붙습니다(출력에 나옵니다).
- 예전 비밀번호는 옮기지 않고 새로 발급하며, 예전 세션은 끝나고, 비밀번호를 담던 `credential` 테이블은 지웁니다.
- 다시 돌려도 이미 옮긴 사람은 건드리지 않습니다("nobody to bring in: every holder here is an account's").
- roster를 따로 운영하면 대신 roster 운영자가 실행할 명령(같은 ID로 계정 만들기)을 출력하고, 운영자가 만든 뒤 다시 돌리면 연결합니다.
- 복구한 워크스페이스에서 이 roster에 계정이 없는 구성원에게도 이것이 계정을 줍니다([워크스페이스 복구](../guide/operator.md#워크스페이스-복구)).
