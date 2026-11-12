# 배포 운영자 안내서

배포 운영자는 Loft를 띄우고 지키는 사람입니다. 워크스페이스의 구성원이 아니라 워크스페이스 밖에서 일하고, 워크스페이스의 요청(주소를 바꿔 달라, 이 사람을 잊어 달라, 데이터를 돌려 달라)을 받아 처리합니다.

운영자가 쓰는 것은 셋입니다(design 9.15, 9.16, D74, D75).

- **서버 상자의 `loft`**: 배포의 생애에 관한 것만 남았습니다. `init`(워크스페이스), `migrate`(서버가 뜨기 전에 DB를 이 빌드의 모양으로), `serve`, `config env`, `version` 입니다. 물을 서버가 없을 때 돌기 때문에, 서버와 같은 설정(`loft.yaml`, `LOFT_*`)으로 DB에 바로 붙습니다.
- **`loft-admin serve`**: 운영자 진입점입니다. `loft-admin` 에는 이것과 `config`, `version` 만 있습니다.
- **운영자의 연결로 부르는 `loft`**: 그 밖의 운영자의 일(`tenant`, `tenant-contract`, `legal-hold`, `holder`, `identity`, `trail`, `event`, `access`, `search`, `retention`)은 모두 진입점에 묻는 명령이고, 사람의 명령과 같은 `loft <스키마 이름> <동사>` 입니다. 운영자 네트워크에서 자기 키로 부르고, 한 일은 작업 기록과 접속기록에 그 운영자의 것으로 남습니다. 운영자만 부르는 명령은 도움말에 `(operator)` 라고 적혀 있습니다.

몇 가지는 늘 같습니다.

- 무언가를 바꾸는 명령은 `--yes` 없이는 무엇을 할지만 말하고 아무것도 바꾸지 않습니다.
- **플래그는 인자보다 앞에 씁니다.** 루트의 플래그(`--url`, `--key`, `--reason`)는 명령 이름보다 앞에 씁니다. `loft --reason "…" tenant export acme` 이고, `tenant export acme --reason "…"` 는 명령 해석기(xli)가 `flag must come before arguments` 로 거부합니다.
- 명령은 스키마의 이름을 씁니다. `tenant` 는 `workspace`, `ws` 로도 씁니다. 워크스페이스는 주소로 씁니다(`acme` 나 `@acme`).
- **운영자 명령이 말하는 날짜는 UTC입니다.** 운영자의 명령이 말하는 날짜와 시각은 셸의 시간대와 상관없이 UTC입니다. `--effective 2027-01-01` 처럼 주는 날짜도 UTC의 그날입니다. 워크스페이스의 사람이 앱에서 읽는 날짜는 그 워크스페이스의 시간대를 따릅니다(loft#144).
- 서버 상자의 명령은 터미널의 연결(`LOFT_CLIENT_CONFIG`)을 읽지 않고, 터미널의 명령은 서버의 설정을 읽지 않습니다([터미널과 비서](terminal.md)). 진입점에 묻는 명령은 운영자의 연결이 정하는 진입점의 주소와 자기 키만 씁니다.
- 이 문서의 예는 체험용 워크스페이스(주소 `loft`, [체험용 워크스페이스](README.md#따라-하기-체험용-워크스페이스))를 씁니다. 컨테이너 안이면 `loft --config /etc/loft/loft.yaml init …` 처럼 설정 파일을 줍니다(`deploy/compose`, `deploy/k8s` 의 예).

## 처음 띄우기

- [`deploy/compose`](../../deploy/compose/README.md)는 한 대의 서버에 띄우는 운영 구성 전체입니다. PostgreSQL, 마이그레이션, `loft serve` 두 대, 운영자 진입점, 인증서를 받는 Caddy가 들어 있습니다. 운영자가 채울 것은 도메인과 비밀값(`.env`, `secrets/`)이고, 이미지는 저장소 루트의 `Dockerfile` 로 짓습니다(`docker build -t loft .`).
- Kubernetes에 띄우는 참조 배포는 [`deploy/k8s`](../../deploy/k8s/README.md)입니다. 운영의 기준이고, main의 빌드는 CI가 `ghcr.io/lesomnus/loft` 로 올립니다.
- 관측(로그, 추적, 지표, 대시보드)은 [`deploy/observe`](../../deploy/observe/README.md)와 [관측 런북](../runbook/observe.md)에 있습니다.
- 운영 전에 정할 것: `app.public_url`(라벨 QR이 담으므로 **라벨을 인쇄하기 전에 정하고 바꾸지 않습니다**), `app.signing_key`, `app.trusted_proxies`, `app.mail`, 감사 기록의 보존 기간(`audit.by`), 감사 기록의 서명 키와 지문의 키. 무엇인지는 두 `deploy/` README의 표와 [보안](../dev/security.md), [기록과 보존](../dev/retention.md)에 있습니다.

배포는 roster 테넌트 하나를 섬기고, 그 테넌트가 곧 워크스페이스입니다(design 9.10, D85). 워크스페이스와 그 소유자는 서버 상자의 `init` 이 세웁니다.

```sh
# 외부 roster: roster 운영자가 테넌트를 만들고 Loft를 설치하고 키를 준 뒤([roster를 따로 운영할 때](../dev/identity.md#roster를-따로-운영할-때))
loft init --name "ACME" --login owner --person "홍길동" --email owner@example.com
# 내장 roster(시험과 써 보기): 테넌트와 소유자까지 만들고 체험용 데이터도
loft init --name "우리 회사" --password admin1234 --demo
```

- 외부 roster면 `init` 은 키의 테넌트를 같은 ID와 주소로 워크스페이스로 세우고, `--login` 의 사람(기본은 roster가 테넌트를 만들 때 넣는 `admin`)을 소유자로 둡니다. 그 사람은 roster에 있어야 하고, 로그인 방법은 테넌트의 관리자가 roster에서 줍니다. `--password` 와 `--demo` 는 거부합니다.
- 내장 roster면 `auth.roster.tenant`(기본 `loft`)의 테넌트와 소유자(기본 아이디 `admin`, 이름 "관리자")를 만들고, `--password` 를 빼면 비밀번호를 만들어서 한 번 보여 줍니다.
- `init` 은 워크스페이스가 이미 있으면 실패합니다. 배포는 워크스페이스 하나를 섬기고, 잘못된 배포에서 조용히 아무것도 하지 않는 것보다 낫기 때문입니다.
- 그 테넌트의 다른 사람은 처음 로그인할 때 구성원이 됩니다. 역할은 소유자·관리자가 사람 화면에서 줍니다.
- 써 보는 배포에서 보관소(`audit.archive`)를 두고 서명 키를 두지 않았으면, `init` 이 보관소 옆에 이 배포만의 키를 만들고 어디에 썼는지 알려 줍니다. `serve` 전에 그 경로를 `audit.checkpoints.key` 에 적습니다.

## 새 버전 올리기

1. 지금 올라가 있는 커밋과 새 커밋 사이에 `deploy/` 가 어떻게 바뀌었는지 봅니다(`git diff <지금>..<새> -- deploy/k8s`). 새로 필요한 비밀이 있으면 **먼저** 넣습니다.
2. [판을 올릴 때 한 번 할 일](../runbook/upgrade.md)에서 넘는 판이 있는지 봅니다. 되돌릴 수 없는 판이면 DB를 백업합니다.
3. 마이그레이션을 돌립니다. 운영에서는 서버가 테이블을 직접 바꾸지 않습니다(`db.migrate: false`). 새 빌드를 띄우기 전에 `loft migrate` 로 테이블을 맞추고, 모양이 다르면 `serve` 는 시작하지 않습니다. compose와 Kubernetes는 이것을 서버보다 먼저 도는 `migrate` 로 합니다.
4. 서버를 새 빌드로 바꾸고 `healthz`, 로그인, 바꾼 기능을 확인합니다.

```sh
loft migrate          # "the database is in this build's shape"
loft version          # 이 바이너리가 무엇인지
loft config env --set # 설정이 읽는 환경변수 가운데 정해진 것
```

자세한 순서와 되돌리는 법은 `deploy/compose/README.md` 의 "새 버전으로 올리기"와 `deploy/k8s/README.md` 의 "새 버전 올리기", "되돌리기"에 있습니다.

## 계정: roster

사람과 비밀번호는 roster의 것입니다([사람과 로그인](../dev/identity.md)). 구성원을 넣고 빼는 일은 roster의 화면에서 하고, Loft에 초대는 없습니다(D87).

- **내장 roster**(`auth.roster.addr` 를 비우면): Loft 프로세스 안에서 함께 뜨고, DB는 Loft의 것 옆에 하나 더 있습니다. 시험과 써 보기용이라, 운영처럼 보이는 배포(PostgreSQL이거나 `app.public_url` 이 이 기계가 아님)에서는 `serve` 가 시작할 때 경고합니다. 사람을 넣고 빼는 roster의 화면이 없습니다.
- **외부 roster**: 직접 운영하는 배포는 roster를 따로 띄웁니다. 사람을 넣고 빼는 화면(user console)이 roster에 있고, 다른 앱(levyan 등)도 같은 사람을 물을 수 있습니다. 그 화면의 주소를 `auth.roster.console_url` 에 주면 Loft의 사람 화면이 그리로 가는 링크를 보입니다. 설정, roster에서 실행할 명령, Loft가 roster에 묻는 메서드는 [roster를 따로 운영할 때](../dev/identity.md#roster를-따로-운영할-때)에, 내장 roster에서 옮기는 순서는 [`deploy/k8s`](../../deploy/k8s/README.md)의 12에 있습니다.
- **역할 바꾸기**: 외부 roster에서 Loft의 역할은 roster 운영자가 `roster role patch` 로 바꿉니다([역할 바꾸기](../dev/identity.md#역할-바꾸기-roster-role-patch)). 역할에 Loft가 묻는 메서드가 없으면 서버 로그에 `roster role patch` 가 적힙니다.
- **SSO**: roster의 issuer로 로그인하게 하는 설정과 roster 쪽에 필요한 것은 [SSO](../dev/identity.md#sso)에 있습니다.

내장 roster에는 사람을 넣는 화면이 없으므로, 운영자 진입점에서 사람을 만들어 구성원으로 넣고 비밀번호를 줍니다(예: 새로 온 사람, 소유자가 비밀번호를 잊었을 때). `loft init --demo` 의 사람도 같은 길로 만듭니다. 여기서 구성원 자격이 끝난 사람에게 `holder add` 를 하면 그 자격을 다시 들입니다(D88). roster가 그 사람을 멈춰 두었으면 거절합니다. 외부 roster면 둘 다 거부되고 roster의 화면에서 합니다.

```sh
loft holder add --login minsu --name 김민수 --role member   # roster에 없으면 만들고 비밀번호를 한 번 출력, 여기서 끝낸 구성원이면 다시 들임
loft holder password --login admin                          # 새 비밀번호를 한 번 출력, 로그인된 곳은 모두 끝남
loft identity link                                          # roster에 없는 구성원(복구한 워크스페이스)을 같은 ID로 roster에 만듦
```

## 워크스페이스의 기본값

워크스페이스가 스스로 정하지 않은 것은 배포의 값을 따릅니다. 모든 값은 `loft config env` 와 저장소 루트의 [`loft.yaml`](../../loft.yaml)에 있습니다.

| 설정 | 무엇 | 비우면 |
| --- | --- | --- |
| `app.locale.timezone` | 시간대를 정하지 않은 워크스페이스의 시간대 | Asia/Seoul |
| `app.locale.currency` | 통화를 정하지 않은 워크스페이스의 통화 | KRW |
| `app.no_show_after` | 회의실 예약을 체크인 없이 몇 분 뒤 풀지 | 0은 체크인을 받지 않음. 저장소의 `loft.yaml` 은 15분 |
| `app.workspaces.restore_within` | 닫은 뒤 복구할 수 있는 기간 | 30일 |
| `app.retention.apply` | 보관 기간으로 기록을 지울지 | 끔 |
| `app.retention.grace` | 줄인 보관 기간이 기다리는 유예 | 30일 |
| `app.retention.every` | 보존 패스의 주기 | 1시간 |
| `app.members.forget_after` | roster에서 삭제된 구성원이 떠나기까지 | 30일(roster의 `holder.forget_after` 와 같게) |
| `app.members.every` | 떠나는 중인 구성원을 다시 보는 주기 | 1시간 |
| `app.members.reconcile` | roster에 모든 구성원을 다시 묻는 주기 | 24시간. 0이나 음수면 끔 |

## 메일

메일은 `app.mail`(SMTP)로 보냅니다. 알림 메일이 이것을 씁니다.

- 비워 두면 메일을 보내지 않습니다. 알림은 앱에서만 받고, 메일로 보낼 것을 쌓지도, 로그에 남기지도 않습니다.
- 알림 메일은 알림을 만든 작업이 끝난 뒤 1분마다 보냅니다. 되돌려진 작업의 알림은 보내지 않고, 보내지 못한 메일은 다음에 다시 시도하며 8번 실패하면 서버 로그에 남기고 포기합니다.
- 어떤 알림이 메일로 가는지와 받는 사람이 끄는 법은 [안내서의 알림](README.md#알림)에 있습니다.

## 검색 엔진 켜기

설정에 클러스터가 없으면 검색은 데이터베이스가 답합니다. OpenSearch를 붙이면 한국어 분석기 nori가 답합니다. 설정과 클러스터의 role, 이미지는 [검색 엔진](../dev/search.md)에, 켜는 순서는 `deploy/compose/README.md` 의 "검색을 켜려면"과 `deploy/k8s/README.md` 의 9에 있습니다.

```sh
loft search status           # 색인마다 별칭이 가리키는 인덱스, 문서 수와 DB의 행 수
loft search reindex          # 지금 다시 만들기 (보통은 서버가 알아서 만듦). 진행을 stderr에 씀
loft search analyze 웹서버    # 그 글을 어떤 낱말로 나누는지
loft search remove --yes     # 이 배포가 만든 인덱스와 별칭을 모두 지움
```

## 과금 붙이기

`app.billing.levyan` 으로 청구 서비스 levyan을 붙이면 워크스페이스마다 잰 것을 넘기고, levyan이 준 권한으로 제한하고, 워크스페이스를 닫고 되돌리고 파기한 것을 알립니다. 누가 내는지는 포털이 levyan에 알립니다. levyan의 운영자가 할 일(제품, 키, 미터)과 설정은 [과금과 결제](../dev/billing.md)에, Kubernetes에서 키를 붙이는 곳은 `deploy/k8s/README.md` 의 11에 있습니다.

- levyan이 답하지 않아도 Loft는 그대로 돌고, 잰 값과 알림은 기다렸다가 다시 보냅니다.
- 운영자의 명령(`loft holder add` 같은 것)은 제한이나 읽기 전용에 막히지 않습니다.

## 운영자 진입점 (loft-admin)

운영자 진입점은 공개 바이너리(`loft`)에는 들어 있지 않은 별도 바이너리(`loft-admin`)이고, 운영자 네트워크(기본은 루프백, VPN 주소까지)에서만 닿는 포트로 듣습니다. 공개 서버와 같은 RPC를 운영자로 내고, 엔터티의 RPC가 없는 일은 운영자만의 서비스(`Operator<X>Service`)로 냅니다(Connect, design 9.15). 호출마다 운영자의 키가 필요하고, 서버 로그에 누가 무엇을 했는지 이름으로 남습니다. 바꾼 것은 배포의 일로 쓰고, 작업 기록과 접속기록에는 그 운영자가 행위자로 남습니다. 워크스페이스의 이력과 접속기록 점검에는 "운영자"로 보입니다. 오래 걸리는 일(파기, 점검, 다시 색인, 보존 패스, 내보내기)은 진행을 말하며 끝까지 한 호출로 하고, 명령을 멈추면(Ctrl-C) 일도 다음 단계에서 멈춥니다. 다시 돌리면 됩니다.

```sh
# 서버 옆에서: 같은 설정(loft.yaml, LOFT_*)에 운영자와 키만 더함 (admin.addr 기본 127.0.0.1:8081)
export ALICE_KEY=$(openssl rand -hex 24)
LOFT_ADMIN_KEYS='{"alice": "env:ALICE_KEY"}' loft-admin serve

# 운영자의 컴퓨터에서: 진입점 주소와 자기 키가 있는 곳 (--url, --key로 줘도 됨)
export LOFT_ADMIN_URL=http://127.0.0.1:8081 LOFT_ADMIN_KEY=env:ALICE_KEY
# 또는 연결로 두기: loft auth login --operator --url http://127.0.0.1:8081 --key env:ALICE_KEY
loft auth whoami                                 # 키가 누구인지 진입점에 물음
loft tenant ls --q 우리                          # 주소·이름의 일부로 찾기 (--closed: 닫힌 것만)
loft tenant show loft                            # 구성원, 보관 기간과 줄어든 기간의 기한, 계약, hold
loft tenant close --reason "요청" loft            # 무엇을 할지만
loft tenant close --reason "요청" --yes loft      # 닫기 (복구 기간 안에는 운영자가 복구할 수 있음)
loft tenant restore --yes loft
loft tenant name-owner --member gunwoo-lee --yes loft  # 구성원을 소유자로 지정
loft tenant-contract add --name abroad --purge-floors --yes loft
loft legal-hold add --why "사건 번호" --yes loft
loft legal-hold patch --lift --yes <ID>                         # ID는 tenant show의 hold 줄

# 워크스페이스가 떠날 때 (내보내기 → 유예 → 파기, 아래 "워크스페이스가 떠날 때")
loft --reason "폐업 전 보관" tenant export --out loft.zip loft   # 내려받기: 접속기록에 까닭과 함께 남음
loft tenant verify --file loft.zip                  # 배포가 서명한 것인지, 진입점이 답하는 공개 열쇠로 여기서 확인
loft tenant purge loft                              # 무엇을 가져갈지만
loft tenant purge --yes loft                        # 파기 (retire는 바닥이 걸린 기록을 남김)
loft tenant import --file loft.zip --yes            # 내보내기에서 되살리기 (--file - 은 stdin)
loft tenant retired                                 # 은퇴한 워크스페이스와 남은 기록의 기한

# 작업 기록 (아래 "작업 기록 점검"과 runbook/trail-integrity.md)
loft trail verify --full                            # 보관소를 매니페스트와 체크포인트에 맞춰 봄. 찾은 것이 있으면 실패
loft trail rows                                     # 살아 있는 행을 작업 기록의 지문에 맞춰 봄
loft trail accept --of <digest> --why "..."         # 본 것을 받아들임. 누가는 키의 운영자로 남음
loft trail owed                                     # 작업 기록의 지우기가 아직 갚지 않은 것
loft --reason "사건 2026-4" trail export --to keeper.pub --out acme.trail acme   # 내려받기: 받는 사람에게 암호화
loft trail verify-export --file acme.trail --identity keeper.key                 # 묶음과 열쇠는 여기에 있고, 진입점은 공개 열쇠만 답함

# 검색 색인과 보존
loft search status                                  # 색인마다 별칭이 가리키는 인덱스, 문서 수와 DB의 행 수
loft search reindex                                 # 지금 다시 만들기. 진행을 stderr에 씀
loft search remove --yes                            # 이 배포가 만든 인덱스와 별칭을 모두 지움
loft retention plan --tenant loft                   # 보관 기간이 지금 가져갈 것 (아무것도 바꾸지 않음)
loft retention run                                  # app.retention.apply가 켜져 있을 때만 가져감

# 사람 (내장 roster) 과 roster
loft holder add --login minsu --name 김민수 --role member   # roster에 없으면 만들고 비밀번호를 한 번 출력, 여기서 끝낸 구성원이면 다시 들임
loft holder password --login admin                  # 새 비밀번호를 한 번 출력, 로그인된 곳은 모두 끝남
loft identity depart --member minsu                 # 할 일: 누구에게 넘어가는지, 닫히는지 (아무것도 바꾸지 않음)
loft identity depart --member minsu --yes           # 지금 떠남
loft identity reconcile --yes                       # roster에 모든 구성원을 다시 물음
loft identity link                                  # roster에 없는 구성원(복구한 워크스페이스)을 같은 ID로 roster에 만듦

# 작업 기록의 사유와 접속기록 (아래 "작업 기록의 사유를 지울 때"와 "접속기록")
loft event redact --tenant loft --event <작업 기록의 ID> --why "삭제 요청 2026-17"         # 무엇을 지울지만 (아무것도 바꾸지 않음)
loft event redact --tenant loft --event <작업 기록의 ID> --why "삭제 요청 2026-17" --yes   # 지움. 한 사람은 키의 운영자
loft access list --tenant loft --kind download --from 2026-10-01                        # 배포 전체의 접속기록. ID로 말하고 운영자는 이름을 곁들임
loft access-review list --tenant loft                                                     # 워크스페이스의 관리자가 남긴 점검
```

- 키는 32자 이상이어야 하고(`admin.keys.<이름>: env:…|file:…|키`), 키가 하나도 없으면 `serve` 가 시작하지 않습니다. 운영자가 여럿이면 이름마다 키를 하나씩 둡니다. 키를 바꿔도 이름이 같으면 기록에는 같은 운영자입니다.
- `loft` 에 주는 키는 있는 곳(`env:이름`, `file:경로`)으로만 씁니다. 키를 바로 쓰면 셸의 기록과 프로세스 목록에 남으므로 거부합니다.
- 사람의 연결로 운영자의 명령을 부르면 아무것도 묻지 않고 운영자의 연결을 쓰라고 말합니다. `tenant list` 와 `access-review list` 는 두 연결이 같이 씁니다. 워크스페이스를 닫고, 되돌리고, 모두 내보내는 것(`tenant close|restore|export`)은 운영자만 합니다(design D83). 소유자도 워크스페이스 안에서는 하지 못하므로 운영자에게 요청합니다. 운영자는 주소로 닫습니다.
- RPC를 그대로 부를 수도 있습니다: `curl -H "authorization: Bearer $KEY" -H 'content-type: application/json' -H 'connect-protocol-version: 1' -d '{}' http://127.0.0.1:8081/loft.TenantService/List`. 진입점이 내는 메서드는 `server/operator` 의 표(`Table`)에 있습니다.
- Kubernetes에서는 Service가 없고 NetworkPolicy가 닫으므로 `kubectl port-forward` 로만 들어옵니다. compose에서는 호스트의 루프백에만 열리므로 SSH 터널로 들어옵니다. 각 README의 "운영자 진입점"에 있습니다.
- 셸의 운영자 명령은 모두 진입점의 RPC가 됐고(loft#285), `loft` 가 운영자의 연결로 부릅니다. 서버 상자에서 도는 것은 `init`, `migrate`, `serve`, `config`, `version` 뿐입니다. 운영자 웹 화면은 나중입니다.

## 워크스페이스의 문의

- **주소를 바꿔 달라**: 주소는 roster 테넌트의 주소이고(D86), 사용자도 운영자도 Loft에서 바꾸지 않습니다. 화면의 주소에는 쓰이지 않고, 운영자가 워크스페이스를 부르는 이름입니다. roster에서 테넌트의 주소를 바꾸면 Loft는 다음에 서버가 시작할 때 그 주소를 따릅니다. 그동안에도 로그인은 됩니다. 외부 roster에 `auth.roster.tenant` 를 적어 두었다면 새 주소로 고친 뒤 다시 시작합니다(비워 두면 키의 테넌트를 따릅니다). 내장 roster는 시험과 써 보기용이라 주소를 바꾸는 길이 없습니다.
- **소유자가 없다, 소유자를 바꿔 달라**: `loft tenant name-owner` 로 구성원을 소유자로 지정합니다.
- **닫아 달라, 다시 열어 달라, 데이터를 달라**: 워크스페이스 안에서는 소유자도 닫고, 되돌리고, 모두 내보내지 못하므로 운영자가 합니다(design D83). 요청한 사람이 그 워크스페이스의 소유자인지 확인하고(`loft tenant show` 의 소유자), 닫을 때는 까닭을 `--reason` 에 적습니다. 닫힌 워크스페이스에는 소유자도 들어오지 못하고, 화면은 운영자에게 문의하라고만 말합니다. 복구 기간 안에는 되돌릴 수 있고, 지나면 파기됩니다. 내보낸 파일은 모든 사람의 데이터이므로 소유자에게 안전하게 건넵니다.
- 워크스페이스의 이름은 운영자가 바꾸지 않습니다. `loft tenant ls|show` 는 저장된 이름을 보이므로, 소유자의 이름으로 부르는 워크스페이스(이름이 없는 것)는 빈칸입니다. 주소와 소유자의 아이디로 찾습니다.

```sh
loft tenant close --reason "요청" --yes loft       # 복구 기간이 지나면 파기
loft tenant restore --yes loft
loft --reason "소유자의 요청" tenant export --out loft.zip loft   # 닫혀 있어도 됨
```

## 계정을 지워 달라는 요청

- 사람을 지우는 것은 roster의 일입니다. roster에서 삭제된 사람은 떠나는 중이 되고, roster의 유예(`holder.forget_after`)가 지나면 떠납니다. Loft는 그만큼 기다리므로(`app.members.forget_after`) 둘을 같게 둡니다.
- **바로 잊어 달라는 요청**은 `loft identity depart` 입니다. `--yes` 없이는 거기서 일어날 일을 말하고 아무것도 바꾸지 않습니다. 마지막 소유자였으면 누구에게 넘어가는지, 넘겨받을 사람이 없는지, 아무도 남지 않아 닫히는지입니다. `--yes` 를 붙이면 지금 떠나게 합니다. 개인정보 삭제와 같은 일이 일어나고, 마지막 소유자였으면 남은 사람에게 넘깁니다. 내장 roster면 그 사람을 roster에서도 파기합니다. 되돌릴 수 없습니다.
- **스트림이 놓친 삭제**는 서버가 하루에 한 번(`app.members.reconcile`) roster에 다시 물어 찾아냅니다. `loft identity reconcile` 은 같은 일을 바로 하되 무엇을 할지 먼저 보여 주고, `--yes` 를 붙이면 합니다. roster에 없는 구성원(복구한 뒤 `identity link` 를 하지 않은 사람)도 떠나는 중으로 표시되고, `link` 하면 다음 대조에서 표시가 지워집니다.

```sh
loft identity depart --member minsu                 # 거기서 일어날 일 (아이디나 ID)
loft identity depart --member minsu --yes
loft identity depart --member <roster의 holder ID> --yes
loft identity reconcile                             # "N member(s) here; roster has N of them"
loft identity reconcile --yes
```

## 작업 기록의 사유를 지울 때

작업 기록의 사유는 그 일을 한 사람이 쓴 글이라, 누가 언급됐는지 알 수 없습니다. 정보주체의 삭제 요청이 어떤 작업 기록의 사유를 가리키면 워크스페이스의 지시에 따라 그 사유를 지웁니다(payday#151). 무엇을 언제 누가 했는지는 그대로 남습니다.

```sh
loft event redact --tenant loft --event <작업 기록의 ID> --why "삭제 요청 2026-17"         # 무엇을 지울지만 (아무것도 바꾸지 않음)
loft event redact --tenant loft --event <작업 기록의 ID> --why "삭제 요청 2026-17" --yes   # 실제로 지움
```

- `--why` 는 이 삭제가 답하는 요청의 참조(접수 번호 등)이고, 반드시 적습니다. 이 일 자신의 작업 기록("작업 기록의 사유 삭제", `event.redact`)의 사유로 남습니다. 다른 사유처럼 개인정보라서 감사 기록에는 썼다는 것만 남습니다.
- 감사 기록에는 지운 일(`payday.past.redact`)이 남습니다. 접속기록에는 무엇을 읽고 썼는지가 `EventService.Redact` 의 행으로, 키의 운영자를 행위자로 남고, 주소는 `admin:` 으로 시작합니다. 지운 사유도 요청의 참조도 어느 쪽에도 남지 않습니다.
- 지우고 나면 이 일의 작업 기록이 무엇인지(그 ID) 말합니다. 이미 지운 작업 기록에 다시 하면, 어느 작업 기록이 언제 누가 지웠는지 말합니다.
- 여러 대상에 걸친 작업은 그 사유를 대상마다의 기록에 옮겨 적습니다(예약한 것마다, 반복의 회차마다, 건넨 것마다, 한 번에 등록한 것마다, 실사의 처리가 부른 상태 변경). 그 가운데 어느 것을 지정해도 그 작업에서 같은 사유를 가진 기록을 모두 지웁니다(design D49). `--yes` 없이 실행하면 그런 기록이 몇 개이고 어떤 종류인지 말합니다. 사유의 내용은 읽지 않습니다. 지운 기록마다 이 일의 작업 기록이 그 대상의 이력에 남고, 감사 기록과 접속기록에도 하나씩 남습니다.
- 그 사유를 글로 옮긴 알림(예약의 승인·거절 알림)에서도 지웁니다(design D51). 알림의 제목과 나머지는 그대로입니다. `--yes` 없이 실행하면 그런 알림이 몇 개이고 어떤 종류인지, 그 가운데 몇 개가 이미 메일로 갔는지 말합니다. **이미 보낸 메일은 되돌릴 수 없습니다.** 요청에 답할 때 알려 주세요. 아직 보내지 않은 메일은 지운 글 없이 갑니다. 고친 알림은 감사 기록과 접속기록에 이 일이 쓴 것으로 남습니다.
- 그 사유를 자기 글로 가진 행에서도 지웁니다(design D56). 지급·대여의 메모(`custody.issue`), 실사 스캔이 남긴 발견의 메모(`count.scan`), 접속기록 점검 결과(`access.review`), 워크스페이스를 닫은 사유(`workspace.close`, 그 닫기로 닫혀 있을 때)입니다. 행의 나머지는 그대로입니다. `--yes` 없이 실행하면 그런 행이 몇 개이고 어떤 종류인지 말합니다. 고친 행은 감사 기록과 접속기록에 이 일이 쓴 것으로 남습니다.
- 워크스페이스 안에서 이 일을 할 수 있는 역할은 아직 없습니다. `EventService.Redact` 는 정책 표에서 닫혀 있고, 누가 할지 정해지면 표의 한 줄로 엽니다.
- 되돌릴 수 없습니다.

## 워크스페이스가 떠날 때

설계 8.2의 순서대로 **내보내기 → 유예 → 파기**입니다. 순서는 운영자가 지킵니다. 닫힌 워크스페이스는 복구 기간이 지나면 서버가 스스로 파기하고, 아래 명령은 같은 일을 손으로 하는 것입니다.

```sh
loft --reason "폐업 전 보관" tenant export --out loft.zip loft   # 모든 행(종류마다 JSON 줄), 감사 기록, 첨부 파일. 까닭은 접속기록에 남음
loft tenant verify --file loft.zip   # 이 배포(또는 audit.checkpoints.trust에 적은 키)가 쓴 그대로인지. 진입점이 답하는 공개 열쇠로 여기서 확인
loft tenant retire loft              # 무엇이 몇 건 사라지고 무엇이 언제까지 남는지만 (아무것도 바꾸지 않음)
loft tenant retire --yes loft        # 실제로 지움: 모든 행과 파일, 바닥값이 걸린 것을 뺀 감사 기록, 마지막으로 조직
loft tenant purge --yes loft         # 감사 기록까지 모두 지움: 바닥값이 걸린 것도
loft tenant retired                  # retire한 워크스페이스와 무엇이 언제까지 남는지
```

- **retire와 purge 가운데 무엇을 쓰나**: 둘 다 조직의 행과 파일을 지웁니다. roster의 사람과 테넌트의 주소는 roster에 남습니다(D85, D86). 다른 것은 감사 기록입니다. retire는 배포의 바닥값(법이 정한 최소 보관 기간, `audit.min`, `audit.by.<종류>.min`)이 걸린 기록을 그 기간까지 남기고(출력의 `kept until …`), purge는 모두 지금 지웁니다. **기본은 retire**이고, 서버의 파기는 그때 효력이 있는 계약을 따릅니다. 자세한 것은 [워크스페이스의 파기](../dev/retention.md#워크스페이스의-파기)에 있습니다.
- **retire는 보관소가 있어야 합니다.** 바닥값이 있는데 `audit.archive` 가 없으면 아무것도 지우기 전에 거부하고, 서버는 그 워크스페이스를 닫힌 채로 두고 경고합니다.
- **청구하는 배포**는 지우기 전에 마지막 측정을 levyan에 넘깁니다. 넘기지 못해도 지우고, 명령이 그렇다고 한 줄 말합니다([과금](../dev/billing.md#파기-전의-마지막-측정)).
- **내보내기의 서명**: `manifest.json` 에 모든 파일의 SHA-256이 적히고, 감사 기록의 키로 서명한 `signature.json` 이 옆에 붙습니다(design D34). 복구는 서명된 내보내기만 받으므로, 복구할 수 있는 내보내기를 내려면 `audit.checkpoints.key` 를 설정해야 합니다. 키가 없는 배포의 내보내기는 `tenant verify` 가 `the export is not signed` 로 거부합니다.
- **파기 뒤에 보존 요청이 오면** 남은 기록을 암호화된 파일로 꺼내 별도 매체에 보관합니다(`loft trail export`): [보존 요청 대응](../runbook/preserve.md). retire한 워크스페이스에는 나중에 법적 보존을 걸 수 없습니다.

### 계약과 법적 보존

운영자의 몫은 **계약**과 법적 보존(hold)입니다. 운영자 진입점에서만 쓰고, 워크스페이스의 사람은 읽기만 합니다. 계약은 파기할 때 감사 기록을 어떻게 할지만 정합니다. 기록을 얼마나 둘지는 워크스페이스가 정합니다.

```sh
# 파기할 때 감사 기록을 모두 지움: 배포의 바닥값이 걸린 것까지
loft tenant-contract add --name abroad --purge-floors --effective 2027-01-01 --yes loft
loft tenant show loft   # 계약들, 워크스페이스가 정한 기간이 지금 어떻게 되는지, 걸린 hold

loft legal-hold add --why "사건 2026-1" --yes loft   # 이력을 아무것도 지우지 않음
loft legal-hold patch --lift --yes <hold-id>
```

- purge로 바꾸는 계약은 앞 계약이 retire였다면 배포의 유예(`app.retention.grace`)가 지난 뒤에 적용됩니다.
- hold가 걸린 워크스페이스는 보관 기간으로도, 파기로도 아무것도 지우지 않습니다.

## 워크스페이스 복구

서명된 내보내기로 워크스페이스를 되살립니다. 파기한 배포에도, 다른 배포에도 넣을 수 있습니다(design D34). `--yes` 없이는 무엇을 되돌릴지만 말합니다.

```sh
loft tenant import --file loft.zip          # 무엇을 몇 건 되돌릴지만 (아무것도 바꾸지 않음)
loft tenant import --file loft.zip --yes    # 실제로 복구. 파일은 진입점으로 올라감
```

- 바닥값이 있는 배포에서 retire한 워크스페이스를 그 배포에 같은 ID로 되살리면 **감사 기록이 먼저 그 조직을 되찾습니다**(`trail.Policy.Return`, payday#141). 그 배포의 감사 기록은 그 조직을 떠난 조직으로 기억하고 있어서, 되찾지 않으면 되살린 워크스페이스가 쓰는 감사 기록을 보존 패스가 지우고 마지막에는 키까지 지우기 때문입니다. 출력의 `it retired from here` 가 그것입니다. retire한 것인지는 감사 기록의 은퇴 기록이 압니다(`loft tenant retired`). purge한 워크스페이스는 그런 기록을 남기지 않으므로 그대로 되살아납니다.
- `--out -` 는 내보내기를 stdout으로 쓰고, `--file -` 는 stdin에서 읽습니다(`verify` 도 같습니다). 셸도 `tar` 도 없는 컨테이너에서 파일을 꺼내고 넣는 방법이고, Kubernetes에서의 순서는 `deploy/k8s/README.md` 의 "워크스페이스 내보내기와 복구"에 있습니다.
- 서명과 모든 파일의 해시를 먼저 검증합니다. 믿는 키는 이 배포의 키와 `audit.checkpoints.trust` 에 적은 키입니다. 다른 배포에서 온 내보내기는 그 배포의 공개 키를 `trust` 에 넣어야 받습니다.
- 행은 원래 ID와 시각 그대로 들어갑니다. 그래서 붙여 둔 라벨과 링크가 계속 열립니다. 같은 워크스페이스가 이 배포에 있으면 거부합니다. 오래된 내보내기로 지금 데이터를 되돌릴 수 없습니다. 전부 한 트랜잭션이라, 한 행이라도 들어가지 못하면(다른 워크스페이스가 그 사이 같은 라벨 코드를 쓰는 등) 아무것도 남지 않습니다.
- 되살리는 것은 이 배포가 섬기는 roster 테넌트의 워크스페이스뿐이고, 주소는 그 테넌트의 roster 주소입니다(D86). roster에서 주소가 바뀌었으면 내보내기의 주소 대신 그것을 쓰고 출력이 둘을 말합니다. 다른 테넌트의 내보내기는 거부합니다(아무도 로그인할 수 없는 워크스페이스가 되기 때문입니다). 내장 roster에 아직 테넌트가 없으면 내보내기의 주소로 넣고, `loft identity link` 가 그 워크스페이스의 ID로 테넌트를 만들 때 주소가 테넌트의 것(`auth.roster.tenant`)으로 바뀝니다.
- 닫힌 채로 내보낸 워크스페이스는 열린 상태로 돌아옵니다. 닫힌 채 두면 다음 파기 주기에 다시 파기되기 때문입니다.
- 이 빌드의 형식(`loft-export/2`)만 받습니다. 이 빌드에 없는 테이블의 행이 있으면 거부하고, 파일이 없는 테이블은 행이 없는 것으로 읽습니다. 출시 전에는 이전 빌드의 내보내기를 읽는 길을 두지 않습니다(대표의 결정, 2026-11-09).
- 이 roster에 없는 구성원(다른 배포에서 왔거나 그 뒤 roster에서 잊힌 구성원)은 그대로 복구되고 출력에 이름이 나옵니다. `loft identity link` 가 같은 ID로 그들을 roster에 만듭니다(내장 roster는 새 비밀번호를 한 번 보여 주고, roster를 따로 운영하면 무엇을 만들지 출력합니다). roster가 이미 삭제한 ID는 다시 만들 수 없어 `link` 가 그렇다고 말하고, 그 사람은 다음 대조에서 떠나는 중이 됩니다.
- 감사 기록은 이 배포의 체인에 넣지 않고, 따로 보관하지도 않습니다. 내보내기 파일이 그 기록이므로 보관해 두세요. 원래 서명으로 계속 검증할 수 있습니다(`tenant verify`). 배포 안에 서명된 사본을 두면, 나중에 그 안의 사람을 개인정보 삭제해도 서명을 깨지 않고는 그 사본에서 지울 수 없습니다.
- 워크스페이스의 이력에 "워크스페이스 복구 (운영자)" 이벤트가 남습니다. 이벤트에는 그 내보내기의 manifest 해시와 서명 키 지문이 적혀서, 어느 파일에서 복구했는지 맞춰 볼 수 있습니다(`tenant verify` 도 둘을 출력합니다). 검색 색인은 복구한 자산·모델·사람을 다시 만듭니다.

## 감사 기록과 접속기록

### 접속기록

개인정보를 읽거나 쓰거나 내려받은 호출마다 접속기록이 한 행씩 남습니다. 워크스페이스는 자기 것을 설정 › 접속기록에서 점검하고([감사자](auditor.md#접속기록-점검)), 운영자는 배포 전체의 것을 봅니다. 이 명령은 이름 없이 ID만 보여 주고(운영자의 행에는 진입점이 아는 운영자의 이름을 곁들임), 아무것도 바꾸지 않습니다. 보여 준 행의 주소와 까닭은 그 호출을 한 사람의 것이라, 이것을 읽은 것도 운영자의 읽기로 접속기록에 남습니다. 정보주체가 자기 정보의 처리 내역을 물으면 `--subject` 로 뽑습니다.

```sh
loft access list --tenant loft --kind download --from 2026-10-01 --to 2026-11-01
loft access list --subject <사람 기록의 ID>
loft access-review list --tenant loft
```

얼마나 두는지와 무엇이 남는지는 [기록과 보존의 접속기록](../dev/retention.md#접속기록)에 있습니다.

### 감사 기록 보관소 확인

`audit.archive` 를 두면 DB에서 나간 감사 기록이 보관소에 쌓이고, 보관소에 무엇이 있어야 하는지는 DB가 따로 기록합니다. 정기적으로 맞춰 봅니다. `verify` 와 `rows` 는 무언가 찾으면 실패로 끝나므로 일정에 걸어 둘 수 있습니다.

```sh
loft trail verify          # 보관소를 DB의 기록·최신 체크포인트와 비교 (아무것도 바꾸지 않음)
loft trail verify --full   # 모든 청크를 다 읽어 내용이 바뀌지 않았는지까지
loft trail accept --of sha256:… --full --why "백업에서 복원한 청크를 확인함"   # 살펴본 뒤, 보관소가 맞다고 DB의 기록을 맞춤
loft trail rows            # 지금 DB의 행을 감사 기록이 남긴 지문과 비교 (아무것도 바꾸지 않음)
loft trail accept-rows --of sha256:… --why "10-20 이전 작업이 바꾼 것을 확인함"   # 살펴본 뒤, 찾은 행을 그대로 받아들임
loft trail owed            # 개인정보 삭제 중 보관소 쪽에서 아직 끝내지 못한 것: 몇 건, 언제부터, 어느 워크스페이스의 hold 때문에
```

- 받아들임은 **살펴본 것에 대한 것**입니다. `verify` 와 `rows` 가 출력한 다이제스트를 `--of` 로 넘기고, 그 사이 찾은 것이 달라졌으면 거부합니다. 누가 받아들였는지는 키의 운영자로 남습니다.
- DB를 백업에서 복원했다면 `verify` 가 `behind` 를 찾습니다. 런북의 "DB를 백업에서 복원했을 때"를 따릅니다(`accept --restored`).
- 판을 올린 뒤 한 번 받아들여야 하는 것이 있으면 [판을 올릴 때](../runbook/upgrade.md)에 적습니다. 서명 키를 처음 둘 때 받아들이는 것은 [서명 키를 처음 둘 때](../runbook/trail-integrity.md#서명-키를-처음-둘-때)에 있습니다.
- 찾은 것(`differs`, `forged`, `missing` 등)이 무엇인지와 서명 키, 지문의 키, 증인은 [기록과 보존의 감사 기록 보관소](../dev/retention.md#감사-기록-보관소)에, 찾았을 때 할 일은 [감사 기록 위변조 감지 대응](../runbook/trail-integrity.md)에 있습니다.

## 보존 기간

워크스페이스가 정한 보관 기간으로 기록을 지우는 일은 **기본으로 꺼져 있습니다**(`app.retention.apply`). 꺼져 있어도 기간은 읽히고, `plan` 이 무엇이 지워질지 말해 줍니다.

```sh
loft retention plan                 # 지금 지우면 무엇이 몇 건 사라지는지 (아무것도 바꾸지 않음)
loft retention plan --tenant loft   # 한 조직만
loft retention run                  # 실제로 지움 — app.retention.apply 가 켜져 있을 때만
```

`app.retention.apply: true` 면 `serve` 가 `app.retention.every`(기본 1시간)마다 같은 일을 합니다. 무엇이 지워지고 무엇이 남는지는 [이력의 보존](../dev/retention.md#이력의-보존)에 있습니다.

## 백업

무엇을 백업하고 어떤 순서로 되살리는지, 되살린 것을 어떻게 확인하는지는 [백업과 복구](../runbook/restore.md)에 있습니다. compose의 볼륨과 비밀은 `deploy/compose/README.md` 의 "데이터가 있는 곳"에 있습니다. 감사 기록의 서명 키와 지문의 키는 잃으면 이전 기록을 검증할 수 없으니 백업에 함께 넣되, DB 백업과는 따로 둡니다.
