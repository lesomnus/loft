# 사람과 로그인 (roster)

**사람**은 [roster](https://github.com/lesomnus/roster)의 것이고, 배포 하나가 roster **테넌트 하나**를 섬깁니다(design 9.10·9.11, D85). 그 테넌트가 곧 워크스페이스입니다. 사람과 비밀번호, 로그인할 수 있는지는 roster가 정하고, 역할은 Loft의 사람 화면에서 정합니다.

- 배포가 섬기는 테넌트는 `auth.roster.tenant` 입니다. 내장 roster는 비우면 `loft` 이고, 외부 roster는 그 테넌트의 키로 붙으므로 키의 테넌트입니다.
- Loft의 워크스페이스(`Tenant`)는 그 테넌트의 ID와 주소(alias)를 갖습니다. `loft init` 이 세웁니다.
- 구성원(`Holder`)은 그 테넌트의 holder이고 ID가 같습니다. 처음 로그인할 때 구성원(member)으로 생기고, 역할은 Loft의 것입니다.
- 구성원을 넣고 빼는 일과 비밀번호는 roster의 화면에서 합니다(D87). Loft의 사람 화면은 사람 기록과 역할을 다루고, 초대도 로그인 발급도 없습니다.

화면에서 하는 일은 [관리자·소유자 안내서](../guide/admin.md), 운영자가 운영자 진입점에서 하는 일은 [운영자 안내서](../guide/operator.md#계정-roster)에 있습니다.

## 내장 roster

roster를 Loft 프로세스 안에서 함께 띄우는 **내장** 모드는 따로 설치할 것이 없어 시험과 개발, 써 보기에 씁니다(loft#327의 결정 5). roster의 DB는 Loft의 SQLite 파일 옆 `data/roster.db` 이고(PostgreSQL이면 `auth.roster.db`), 사람은 그 안의 테넌트(`auth.roster.tenant`, 기본 `loft`)에 있습니다. Loft는 그 roster를 `bufconn` 으로 부릅니다.

- `loft init` 은 테넌트와 첫 사람(소유자, 아이디 기본 `admin`, 이름 기본 "관리자")을 만들고 비밀번호를 한 번 보여 줍니다. `--password` 로 정할 수도 있습니다.
- 이 roster에는 사람을 넣는 화면이 없으므로, 운영자가 `loft holder add|password` 로 사람을 만들어 구성원으로 넣고 비밀번호를 정합니다. `loft init --demo` 의 사람도 같은 길로 만듭니다.

**운영처럼 보이는 배포에서 내장 roster를 쓰면 `loft serve` 가 시작할 때 경고합니다.** 거부하지는 않습니다. 운영처럼 보인다는 것은 DB가 PostgreSQL(`db.driver: pgx`)이거나, `app.public_url` 이 이 기계(`localhost`, `*.localhost`, 루프백 주소)가 아닌 경우입니다. 경고에는 둘 중 무엇 때문인지가 함께 나옵니다.

- 그 프로세스만 roster에 닿고, 사람과 비밀번호가 Loft의 데이터 옆에 있으며, 다른 앱은 그 사람들을 쓸 수 없습니다.
- roster의 화면(사람을 넣고 빼는 곳)이 없습니다. 사람이 쓰는 배포(self-hosted)는 loft와 roster를 따로 띄웁니다(아래, D91).

## roster를 따로 운영할 때

self-hosted의 순서입니다(D91). roster 운영자가 테넌트를 만들고, 사람 화면의 호스트가 그 테넌트의 것이라고 적고, Loft를 설치하고, 그 테넌트의 키를 만듭니다. Loft는 그 키로 붙어 `loft init` 을 합니다. 한 대에서 그대로 따라 하는 구성이 [`deploy/compose`](../../deploy/compose/README.md#처음-띄우기)이고, CI의 `bundle` 작업이 이 순서를 처음부터 로그인까지 돌립니다.

```sh
# roster 운영자 (roster의 상자에서)
roster init                                                   # roster 운영자 admin. 한 번만
roster --as admin tenant add @acme '{"name": "ACME"}'         # 테넌트와 그 관리자 admin
roster vouch reset @acme/admin                                # 그 관리자의 비밀번호, 한 번 출력
roster --as admin host add '{"tenant": {"alias": "acme"}, "name": "id.example.com"}'   # 사람 화면의 호스트
m=/roster.VouchService/Verify,/roster.HolderService/Get,/roster.TenantService/Get,/roster.MeService/Get
m=$m,/roster.EmailService/List,/roster.EmailService/Get,/roster.SyncService/Watch
m=$m,/roster.HolderService/Reaches,/roster.RoleService/Add,/roster.RoleService/Get,/roster.RoleService/Widen
m=$m,'/loft.*/*'
roster app install --tenant acme --role "$m" --administer '/loft.*/*' loft
roster key add --tenant acme --holder loft --name loft --allow "$m"   # rt_… 출력

# Loft: 그 키로 붙고, 그 테넌트를 워크스페이스로 세운다
export LOFT_AUTH_ROSTER_ADDR=roster.example.com:443 LOFT_AUTH_ROSTER_KEY=env:ROSTER_KEY ROSTER_KEY=rt_…
export LOFT_AUTH_ROSTER_TENANT=acme LOFT_AUTH_ROSTER_CONSOLE_URL=https://id.example.com/
loft init
loft serve
```

- 목록 `m` 은 Loft가 roster에 묻는 메서드(`identity.AgentMethods`), 그리고 Loft가 테넌트에 써 넣는 기본 역할(#334)을 쓰고 넓히는 데 드는 메서드입니다. `/loft.*/*` 를 쥐어야 Loft의 메서드로 된 역할을 쓸 수 있고, `--administer` 가 있어야 테넌트의 관리자가 그 역할을 사람에게 줄 수 있습니다(roster `docs/apps.md` 의 *The roles an app ships*). 키도 허용한 메서드로 좁혀지므로 같은 목록을 줍니다.
- 사람 화면은 roster의 설정에서 켭니다: `sign_in.enabled: true`, `user_console.dir: /usr/share/roster/user`. 데이터 플레인의 HTTP 리스너(`server.http`)가 그 화면을 내고, 앞의 프록시가 화면만의 호스트를 그리로 보냅니다. Loft와 다른 호스트입니다. 세션 쿠키(`__Host-`)가 호스트에만 붙고, roster는 그 이름으로 어느 테넌트인지 압니다(roster `docs/operating.md` 의 *The user console*).

- 키는 그 테넌트의 키(`rt_…`)입니다(roster `docs/apps.md` 의 C 모양). 키 하나가 테넌트 하나에만 닿습니다. `auth.roster.tenant` 는 비워 두면 키의 테넌트이고, 적으면 그것과 맞아야 합니다.
- `loft init` 은 그 테넌트를 같은 ID와 주소로 워크스페이스로 세우고, `--login` 의 사람(기본은 roster가 테넌트를 만들 때 넣는 `admin`)을 소유자로 둡니다. 그 사람은 테넌트의 관리자가 roster에서 미리 비밀번호나 로그인 방법을 줍니다. `--password` 와 `--demo` 는 받지 않습니다.
- 이때 `loft holder add|password` 는 거부되고, 사람을 넣고 빼는 일과 비밀번호는 roster의 화면에서 합니다(아래 *roster의 화면*).
- 내장 roster에서 옮기는 순서와 Kubernetes에서 키를 붙이는 곳은 [`deploy/k8s`](../../deploy/k8s/README.md)의 12에 있습니다.
- roster의 박스에서 `tenant`, `role` 같은 명령을 할 때는 그 명령을 하는 운영자를 `--as <운영자>` 로 밝힙니다(control plane이 있는 roster, roster#221). roster의 접속기록이 그 사람을 남기고, 밝히지 않으면 거부됩니다. `app install` 과 `key add` 에는 필요 없습니다.

### Loft가 roster에 묻는 것

위 `--role` 의 목록이 Loft가 roster에 묻는 메서드 전부입니다(`identity.AgentMethods`).

- `VouchService/Verify`: 비밀번호 로그인. `HolderService/Get`, `TenantService/Get`, `MeService/Get`: 사람과 테넌트, 키가 누구의 것인지를 읽습니다.
- `EmailService` 의 두 권한은 roster가 확인한 주소를 읽습니다. 처음 로그인한 사람의 사람 기록을 그 주소로 찾습니다.
- `SyncService/Watch` 는 roster의 동기화 스트림입니다. roster가 사람을 중지·삭제하거나 모든 곳에서 로그아웃시키면 Loft가 그 자리에서 세션을 끝내고, 삭제된 사람을 떠나는 중으로 표시합니다. roster가 테넌트를 닫으면(`roster tenant close`, roster#105) 모든 사람의 세션이 끝나고, 닫혀 있는 동안은 아무도 로그인하지 못합니다. 아무것도 지우지 않으므로 테넌트를 복구하면 다시 로그인할 수 있습니다.

### 계정 페이지

`auth.roster.account_url` 에 roster의 계정 페이지(그 테넌트의 front door, 예: `https://id.example.com/`)를 주면, 내 정보 화면에 "로그인 방법 관리"와 "계정 탈퇴" 링크가 생깁니다. 탈퇴 링크는 그 페이지의 `#leave` 로 가고, 삭제는 roster에서 합니다(`MeService.Leave`, roster#106). 삭제는 테넌트가 허용할 때만 되므로, roster 운영자가 `TenantConfig.leave` 를 켭니다(관리 콘솔의 테넌트 화면, 또는 resources 파일의 `config.leave`). 삭제된 사람은 위의 스트림으로 Loft에 들어와 떠나는 중이 됩니다.

### roster의 화면

`auth.roster.console_url` 에 그 테넌트의 사람이 여는 roster의 화면(user console, roster `docs/operating.md` 의 *The user console*)을 주면, Loft의 사람 화면 위의 안내 "구성원과 로그인은 roster에서 관리합니다"에 그리로 가는 링크가 생깁니다. 테넌트의 관리자가 거기서 사람을 넣고 빼고 로그인 방법을 줍니다(D87). 비워 두면 안내만 보입니다. 내장 roster에는 그 화면이 없으므로, 내장 roster에 이것을 적으면 시작하지 않습니다(`account_url` 도 같습니다).

### 역할 바꾸기 (`roster role patch`)

`app install` 을 다시 실행해도 이미 있는 역할은 그대로 두므로(roster `docs/apps.md`), 역할은 roster 운영자가 `roster role patch` 로 바꿉니다. 목록을 통째로 바꾸므로 위의 메서드 전체를 줍니다.

역할의 이름은 Loft가 그 테넌트에 들어온 길을 따릅니다. `roster app install` 로 설치한 배포는 `loft-app` 이고, 내장 roster에서 옮긴 배포는 내장 roster가 만든 `loft` 입니다(테넌트를 따로 적지 않았다면 테넌트도 `loft` 라 `@loft/loft`).

```sh
# roster app install로 설치한 배포 (위의 예: 테넌트 acme)
roster --as <운영자> role patch @acme/loft-app '{"methods": ["/roster.VouchService/Verify", "/roster.HolderService/Get", "/roster.TenantService/Get", "/roster.MeService/Get", "/roster.EmailService/List", "/roster.EmailService/Get", "/roster.SyncService/Watch"], "date_updated_force": true}'
# 내장 roster에서 옮긴 배포 (테넌트 loft)
roster --as <운영자> role patch @loft/loft '{"methods": ["/roster.VouchService/Verify", "/roster.HolderService/Get", "/roster.TenantService/Get", "/roster.MeService/Get", "/roster.EmailService/List", "/roster.EmailService/Get", "/roster.SyncService/Watch"], "date_updated_force": true}'
```

역할에 이 메서드가 없으면 이렇습니다.

- 처음 로그인한 사람의 사람 기록을 roster가 확인한 주소로 찾지 못하고 새로 만듭니다.
- 세션은 다음 호출에서(30초 안에) 끝나며, 삭제된 사람은 떠나는 중으로 표시되지 않습니다.

스트림을 거부당한 Loft는 서버 로그에 오류를 한 번 남기고 10분마다 다시 청하므로, 역할을 고친 뒤 다시 시작하지 않아도 됩니다. 키가 허용한 메서드에도 들어 있어야 하므로 키를 새로 만듭니다.

## 로그인과 세션

- 로그인은 아이디나 이메일로 합니다. 이메일은 roster가 그 사람의 것으로 확인한 주소입니다. 내장 roster의 사람에게는 확인된 주소가 없으므로 아이디로 합니다. 2단계 인증을 켠 사람은 비밀번호만으로 들어올 수 없고, SSO로 들어옵니다.
- 브라우저는 `POST /session` 으로 로그인합니다(`server/session`). 비밀번호는 roster의 `VouchService.Verify` 로 가고, 세션은 그 **구성원**의 것입니다. 고를 워크스페이스는 없습니다. 요청에도 주소에도 워크스페이스가 없고, 화면은 앱의 뿌리(`/`)입니다.
- **처음 로그인하면 구성원이 됩니다**(`tenancy.Members`). roster의 holder ID로 Loft의 구성원 자격을 만들고, 역할은 구성원(member)입니다. 사람 기록은 roster가 확인한 주소로 찾습니다. 그 주소의 사람 기록 가운데 로그인이 없는 것이 꼭 하나면 그것을 잇고, 없거나 여럿이면 새로 만듭니다. 확인되지 않은 주소로는 잇지 않습니다(누구나 아무 주소나 적을 수 있으니까요). 아이디는 roster의 것이고, 여기서 이미 누가 쓰면 `kim-2` 처럼 번호를 붙입니다.
- **여기서 끝낸 구성원 자격은 다시 들입니다**(D88). 개인정보를 삭제했거나 roster에서 지워진 뒤 유예가 지나 떠난 구성원도, roster가 그 holder를 아직 갖고 있어 로그인이 되면 같은 구성원 자격으로 돌아옵니다. 구성원이 누구인지는 roster가 말하기 때문입니다. 돌아온 자격은 roster가 부르는 이름과 아이디, 역할은 구성원이고, 사람 기록은 처음 로그인할 때처럼 찾거나 새로 만듭니다. 잊은 사람 기록은 잊은 채로 둡니다. 그 전의 세션은 모두 끝나고, trail에는 `loft.holder.readmit` 로 남습니다. 운영자의 `loft holder add` 도 같은 일을 하되, roster가 그 사람을 멈춰 두었으면 거절합니다. 들어오지 못하게 하려면 roster에서 멈추거나 지웁니다.
- **사람 기록은 손으로도 잇습니다**(D88). 관리자는 사람 기록을 열어 **구성원과 잇기**로 구성원을 고릅니다(`PartyService.LinkMember`). 다른 기록과 이어져 있던 구성원은 이 기록으로 옮겨 오고, 원래 기록은 이력을 지닌 채 로그인 없는 기록이 됩니다. **구성원 연결 끊기**(`UnlinkMember`)는 기록에서 구성원을 떼고, 그 구성원은 다른 기록과 이을 때까지 사람 기록이 없습니다. 로그인해도 기록을 다시 찾지 않습니다(같은 주소로 같은 기록을 다시 찾을 것이기 때문입니다). 사람 화면이 그런 구성원을 따로 보여 줍니다. 개인정보를 삭제한 기록에는 잇지 않습니다. 둘 다 작업 기록(`party.link`, `party.unlink`)에 남습니다.
- `loft init` 전이라 워크스페이스가 없으면 로그인은 503입니다.
- 로그인한 사람이 roster에서 중지·삭제되거나 "모든 곳에서 로그아웃"되면 Loft의 세션도 끝납니다. 외부 roster면 roster가 말하는 즉시(roster의 동기화 스트림), 그렇지 않아도 30초 안에 끝납니다. roster에 물을 수 없으면 로그인과 세션이 거부됩니다(503).
- 화면이 가장 먼저 묻는 것은 `GET /session` 입니다(`session.Who`). 세션이 누구인지(구성원의 ID, 아이디, 이름), `--allow` 로 좁힌 메서드, 결제를 다루는 배포인지, 워크스페이스(ID, 주소, 이름, 시간대, 닫혔으면 닫힌 때와 지워질 때)를 답합니다. 닫힌 워크스페이스는 RPC를 받지 않으므로 그 안내는 이것으로 그립니다.
- 터미널의 로그인(`loft auth login`)은 브라우저에서 승인하는 같은 구성원의 세션입니다([터미널과 비서](../guide/terminal.md)). 로그인 실패와 터미널 로그인의 제한은 [보안](security.md#로그인-제한)에 있습니다.

## SSO

**SSO로 로그인할 때**(D28)는 roster의 issuer(Hydra와 Login App)를 거칩니다. 로그인 화면에 Google·Microsoft·GitHub 같은 버튼이 생기고, 누르면 roster를 잠깐 지나 그 IdP에서 로그인한 뒤 돌아옵니다. 돌아온 사람은 비밀번호로 들어온 사람과 같은 구성원이고, 처음이면 구성원이 됩니다. 로그아웃은 issuer의 세션까지 끝냅니다.

```yaml
auth:
  sso:
    issuer: https://id.loft.example      # roster 앞의 Hydra
    client_id: loft
    client_secret: env:LOFT_SSO_SECRET
    providers:                           # 버튼: 그 테넌트의 연결(Connection) 이름
      - { connection: google, label: Google, mark: google }
      - { connection: microsoft, label: Microsoft, mark: microsoft }
      - { connection: github, label: GitHub, mark: github }
  sso_only: true                         # 비밀번호 로그인을 끈다
```

Loft는 roster `docs/relying-party.md` 의 `itself` 모양을 따르는 relying party입니다(`server/sso`). `GET /sso/login?with=<연결>` 은 브라우저를 그 연결을 이름 붙인 Hydra로 보내고, `GET /sso/callback` 은 코드를 바꿔(state는 쿠키에, nonce, PKCE, 비밀은 헤더에) `sub` 가 말하는 구성원의 세션을 만들고, ID 토큰을 그 옆에 둡니다. `POST /sso/logout` 이 그것을 `id_token_hint` 로 돌려줍니다. `GET /sso` 는 로그인 화면이 그리는 버튼입니다. `auth.sso_only` 면 `POST /session` 은 403입니다.

roster 쪽에 필요한 것:

- **Hydra 클라이언트**: `token_endpoint_auth_method: client_secret_basic`, `redirect_uris: ["<app_url>/sso/callback"]`, `post_logout_redirect_uris: ["<app_url>/"]`, `scope: openid`.
- **테넌트에 앱 호스트의 `Host` 행**: Login App은 돌아갈 주소의 호스트로 테넌트를 찾습니다(`roster host add` 에 `{"tenant": {"alias": "acme"}, "name": "loft.example"}`).
- **테넌트의 연결** `google`·`microsoft`·`github`: roster `docs/operating.md` 의 *GitHub*, *Any Microsoft account*. 버튼이 이름 붙인 연결로 Login App이 바로 보내는 것은 roster#103부터이고, 테넌트에 없는 이름이면 roster의 로그인 화면이 나옵니다.

테스트는 `internal/oidctest` 의 issuer로 로그인합니다([테스트](testing.md)).

## 구성원 들이기

구성원은 테넌트의 관리자가 roster의 화면에서 넣고, 넣은 사람은 처음 로그인할 때 구성원이 됩니다(위의 *처음 로그인하면 구성원이 됩니다*, D87). 화면의 흐름은 [관리자·소유자 안내서](../guide/admin.md#사람-들이기)에 있습니다.

- Loft에는 초대도, 로그인 발급도, 비밀번호를 바꾸는 화면도 없습니다. 초대(D30)는 D87이 걷어냈습니다. 누구를 넣을지는 roster가 알고, 넣은 사람은 이미 그 테넌트의 사람이어서 들어오라고 알릴 것이 남지 않기 때문입니다.
- 내장 roster에는 화면이 없으므로 운영자가 `loft holder add` 로 넣습니다(위의 *내장 roster*).

## 사람이 떠날 때

- roster에서 삭제된 사람은 그때부터 **떠나는 중**이 됩니다(`Holder.date_leaving`, 삭제된 시각).
- roster의 삭제에는 되살릴 수 있는 유예가 있고(`holder.forget_after`, 기본 30일), Loft도 그만큼 기다립니다(`app.members.forget_after`, roster와 같게 둡니다).
- 유예가 지나도 roster가 되살리지 않았으면 그 사람은 **떠납니다**. 개인정보 삭제와 같은 일이 일어나고, 마지막 소유자였으면 남은 사람 중 역할이 가장 높은 사람(관리자 > 매니저 > 구성원, 감사자는 제외), 같은 역할이면 먼저 들어온 사람이 소유자가 됩니다. 넘길 사람이 없으면 소유자 없이 남고 서버 로그에 남깁니다.
- 되살린 사람은 다음 확인(1시간마다, `app.members.every`) 때 원래대로 돌아옵니다.
- roster의 동기화 스트림은 끊긴 사이의 일을 다시 보내지 않습니다. Loft가 듣고 있지 않을 때 삭제된 사람은 서버가 하루에 한 번(`app.members.reconcile`, 기본 24h, 0이나 음수면 끔) roster에 모든 구성원을 다시 물어 찾아내고, 지금부터 떠나는 중으로 표시하고, 무엇을 표시했는지 서버 로그에 남깁니다(`Lifecycle.Reconcile`). roster가 하나도 모른다고 답하면 테넌트나 키가 잘못된 것으로 보고 아무것도 표시하지 않습니다. 구성원은 roster의 holder와 같은 ID라는 것 말고 따로 적어 두는 것이 없어서(D86), 복구한 뒤 roster에 만들지 않은 구성원도 roster가 모르는 사람으로 표시됩니다. `loft identity link` 로 만들면 다음 대조에서 돌아옵니다.
- 바로 잊어 달라는 요청과 지금 다시 맞추는 일은 운영자가 `loft identity depart`, `loft identity reconcile` 로 합니다([운영자 안내서](../guide/operator.md#계정을-지워-달라는-요청)).
- 개인정보 삭제로 구성원 자격을 끝내는 것은 roster의 사람을 건드리지 않습니다. 워크스페이스를 파기해도 roster의 사람은 그 테넌트에 남습니다.

## roster에 없는 구성원

워크스페이스를 내보내기에서 복구하면, 이 배포의 roster에 없는 구성원(다른 배포에서 왔거나 그 뒤 roster에서 잊힌 구성원)도 그대로 돌아오고 복구의 출력에 이름이 나옵니다([워크스페이스 복구](../guide/operator.md#워크스페이스-복구)). 그 구성원은 roster에 사람이 생기기 전에는 로그인할 수 없습니다. 운영자가 만듭니다.

```sh
loft identity link                          # 구성원마다 로그인과 새 비밀번호를 한 번 출력
loft identity link --password demo1234      # 체험용 배포라면: 모두 같은 비밀번호로
```

- 구성원마다 같은 ID로 내장 roster의 테넌트에 사람이 생기므로 이력에 남은 행위자는 그대로입니다. 테넌트가 아직 없으면(복구만 한 배포) 워크스페이스의 ID로, 이 배포가 섬기는 주소(`auth.roster.tenant`)에 먼저 만들고, 워크스페이스의 주소도 그것이 됩니다(D86).
- 이 배포가 섬기는 워크스페이스의 구성원만 만듭니다.
- 다시 돌려도 roster에 이미 있는 구성원은 건드리지 않습니다("nobody to make").
- roster가 삭제한 사람(잊힌 사람도, `loft identity depart` 로 떠난 사람도)은 그 ID로 다시 만들 수 없습니다. roster의 forget은 ID를 남깁니다. `link` 는 그 사람을 "erased at roster"라고 말하고 나머지를 만듭니다. 그 사람은 다음 대조에서 떠나는 중이 됩니다.
- roster를 따로 운영하면 대신 roster 운영자가 실행할 명령(같은 ID로 사람 만들기)을 출력합니다. 운영자가 만들면 그 사람은 그대로 로그인할 수 있습니다.
