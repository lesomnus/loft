# Loft

물리 자산과 공간을 관리하는 서비스입니다. 배포 하나가 조직 하나, [roster](https://github.com/lesomnus/roster)의 테넌트 하나를 섬깁니다. 무엇이 어디에 있었고 누가 가지고 있었는지를 **시간을 따라** 기록하고, 공간과 장비의 예약, 지급·대여, 재고, 실사, 정비, 구매를 한곳에서 다룹니다. 컴퓨터와 모니터뿐 아니라 촬영 장비, 공구, 회의실과 창고까지 같은 대장에 둡니다.

> 이 저장소는 공개되어 있지만 오픈소스가 아닙니다. [`LICENSE`](LICENSE)를 보세요.

## 무엇이 있나

- **자산 대장**: 검색·필터, 등록과 대량 등록(loft 양식의 CSV), 유형과 속성, 모델, 첨부, 라벨 인쇄, CSV 내보내기, 유형별 화면(랙 배치도, 보증)
- **시간을 따라 보기**: 위치·담당·관계·속성의 이력과 그 정정, 과거 어느 시점의 공간의 모습, 두 시점 사이의 변화
- **공간**: 건물·층·방의 트리. 공간도 자산이라 라벨과 이력과 예약이 그대로 붙습니다
- **예약**: 주간 캘린더, 반복 예약, 임시 홀드, 승인, 체크인과 노쇼 자동 해제, 장비 수령, 키트와 풀(같은 장비 여러 대), 쓸 수 없거나 나가 있는 것의 알림, 예약 창의 이력
- **지급·대여**: 지급과 대여, 받은 사람의 인수 확인(라벨을 찍어 스스로), 부분 반납, 기한 연장, 연체 알림
- **재고**: 소모품의 입고·사용·조정·이동, 자산으로 전환, 부족 알림
- **실사**: 범위 지정, 카메라나 스캐너로 스캔(오프라인이면 모아 두었다 전송), 대조, 위치 반영과 분실 처리, 셀프 실사
- **작업**: 수리·점검·정비·RMA, 그 시간의 예약 막기, 정기 점검, 구성원의 고장 신고
- **구매**: 주문, 입고 때 자산이나 재고로 등록
- **리포트**: 자산 현황, 대여 현황, 예약 이용률, 실사 결과, 재고 소모와 그 CSV
- **사람과 역할**: 조직도, 역할 다섯(소유자 · 관리자 · 매니저 · 구성원 · 감사자), 개인정보 삭제. 구성원을 넣고 빼는 일과 비밀번호는 roster의 화면에서 합니다
- **워크스페이스**: 배포가 섬기는 roster 테넌트가 곧 워크스페이스이고, 그 사람들이 처음 로그인하면 구성원이 됩니다. 시간대·통화·노쇼 규칙과 기록 보관 기간. 닫기·복구·내보내기는 운영자가 합니다
- **기록**: 무엇을 왜 했는지(작업 기록), 누가 무엇을 바꿨는지(감사 기록), 누가 누구의 개인정보를 보았는지(접속기록)와 그 점검
- **결제**: 청구 서비스 [levyan](https://github.com/lesomnus/levyan)이 매기고, 워크스페이스의 사용량과 결제 담당, 제한·읽기 전용을 Loft가 보입니다. 결제 계정과 결제수단, 청구서는 Loft의 것이 아닙니다
- **어디서나**: 휴대폰 브라우저(PWA)와 라벨 스캔, 오프라인에서 마지막으로 본 화면, 다른 사람이 바꾼 것이 바로 보이는 화면, 모든 기능을 부르는 터미널(`loft`)과 비서(`loft mcp`)

무엇을 누가 할 수 있는지는 [`server/policy/policy.go`](server/policy/policy.go)의 표 하나가 정하고, 표에 없는 것은 거부됩니다.

## 누가 무엇을 읽나

- **구성원**: 장비를 받아 쓰고, 예약하고, 받은 것을 확인합니다 → [구성원 안내서](docs/guide/member.md)
- **매니저**: 자산을 등록하고 건네고 돌려받고, 예약을 승인하고, 실사와 재고와 수리를 챙깁니다 → [매니저 안내서](docs/guide/manager.md)
- **관리자·소유자**: 워크스페이스와 사람, 역할, 설정, 기록 보관을 맡습니다 → [관리자·소유자 안내서](docs/guide/admin.md)
- **감사자**: 이력과 감사 기록을 읽고 접속기록을 점검합니다 → [감사자 안내서](docs/guide/auditor.md)
- **배포 운영자**: 서버를 띄우고 올리고, 워크스페이스의 닫기·복구·내보내기 같은 요청을 운영자 진입점(`loft-admin serve`)에 운영자의 연결로 묻는 `loft` 로 처리합니다 → [배포 운영자 안내서](docs/guide/operator.md)
- **모두**: 터미널(`loft auth login`, `loft <엔터티> <작업>`)과 비서(`loft mcp`) → [터미널과 비서](docs/guide/terminal.md)

안내서의 예는 모두 아래의 체험용 워크스페이스를 씁니다. 무엇이 들어 있는지는 [안내서](docs/guide/README.md)에 있습니다.

## 바로 써 보기

필요한 것: Go 1.27, Node 22.12 이상(UI를 빌드할 때만). 데이터베이스는 기본이 SQLite 파일이라 따로 설치할 것이 없습니다.

```sh
# UI 빌드 (한 번)
cd ts && npm install && npm run build && cd ..

# 워크스페이스(내장 roster의 테넌트 loft)와 소유자 admin, 그리고 체험용 데이터(5개 팀 50명, 공간, 자산 140여 개)
go run ./cmd/loft init --name "우리 회사" --password admin1234 --demo

# 서버
go run ./cmd/loft serve
```

<http://localhost:8080> 에서 `admin` / `admin1234` 로 로그인합니다. `--demo` 로 만든 사람들은 모두 비밀번호 `demo1234` 로 로그인할 수 있고, `init` 이 역할별 예시 아이디를 출력합니다(예: 매니저 `minjun-kim`, 관리자 `gunwoo-lee`, 감사자 `naeun-park`, 구성원 `haeun-lee`). 처음부터 다시 하려면 `data/` 디렉터리를 지웁니다.

터미널에서도 써 봅니다.

```sh
go install ./cmd/loft
loft auth login --url http://localhost:8080     # 나온 주소를 로그인한 브라우저에서 열고 승인
loft asset search -o wide '{"q": "노트북"}'
```

설정, UI 개발, 휴대폰에서 써 보기는 [로컬에서 실행하기](docs/dev/running.md)에 있습니다. 여기의 roster는 Loft 프로세스 안에서 함께 뜨는 **내장 roster**이고, 시험과 개발, 써 보기용입니다. 사람을 넣고 빼는 화면이 없습니다.

## 직접 운영하기 (self-hosted)

사람이 쓰는 배포는 Loft와 [roster](https://github.com/lesomnus/roster)를 따로 띄웁니다. 사람과 로그인, 구성원을 넣고 빼는 화면(user console)은 roster의 것이고, Loft는 roster의 테넌트 하나를 워크스페이스로 섬기는 앱입니다(design 9.10). 처음부터 띄우는 순서는 이렇습니다. 한 대에 그대로 따라 할 수 있는 구성이 [`deploy/compose`](deploy/compose/README.md)이고, CI가 PR마다 이 순서를 그대로 돌립니다.

1. **roster를 띄웁니다.** 데이터 플레인과 컨트롤 플레인의 DB, 사람 화면(`sign_in.enabled`, `user_console.dir`)과 그 화면의 호스트를 둡니다. 화면은 Loft와 다른 호스트에 둡니다. 세션 쿠키가 호스트에만 붙기 때문입니다.
2. **roster의 운영자와 조직의 테넌트를 만듭니다.** `roster init`, `roster --as admin tenant add @acme`. 테넌트를 만들면 그 관리자 `admin` 이 함께 생기고, 비밀번호는 `roster vouch reset @acme/admin` 이 한 번 보여 줍니다. 화면의 호스트가 그 테넌트의 것이라고 적습니다(`roster --as admin host add`).
3. **그 테넌트에 Loft를 설치하고 키를 만듭니다.** `roster app install --tenant acme --role <목록> --administer '/loft.*/*' loft`, 그리고 `roster key add --tenant acme --holder loft --allow <같은 목록>` 이 테넌트의 키(`rt_…`)를 한 번 보여 줍니다. 목록은 Loft가 roster에 묻는 메서드와, Loft가 테넌트에 써 넣는 처음 역할을 쓰고 넓히고 첫 소유자에게 묶는 데 드는 메서드입니다(`identity.AgentMethods`, [`deploy/compose`의 처음 띄우기](deploy/compose/README.md#처음-띄우기)). `--administer` 는 테넌트의 관리자가 Loft의 역할을 사람에게 묶을 수 있게 합니다.
4. **Loft를 설정합니다.** roster의 주소와 키, 섬기는 테넌트, roster 화면의 주소: `auth.roster.addr`, `auth.roster.key`, `auth.roster.tenant`, `auth.roster.console_url`.
5. **`loft init`** 이 그 테넌트를 같은 ID로 워크스페이스로 세우고, Loft의 처음 역할 다섯(`loft-owner`, `loft-admin`, `loft-manager`, `loft-member`, `loft-auditor`)을 roster에 쓰고, 테넌트의 관리자 `admin`(또는 `--login` 의 사람)에게 `loft-owner` 를 묶어 소유자로 둡니다(D89). 그다음 `loft serve`.
6. **구성원**은 테넌트의 관리자가 roster의 화면에서 넣고 Loft의 역할을 묶으며, 그 사람이 처음 Loft에 로그인하면 구성원이 됩니다. 누가 무엇을 할 수 있는지는 그 역할이 정하고, 역할은 roster에서 고칩니다(D89).

설정과 roster에서 할 일의 자세한 것은 [사람과 로그인](docs/dev/identity.md#roster를-따로-운영할-때), Kubernetes는 [`deploy/k8s`](deploy/k8s/README.md)에 있습니다.

## 개발 문서

| 문서 | 내용 |
| --- | --- |
| [로컬에서 실행하기](docs/dev/running.md) | `init`, `serve`, 설정과 `config env`, UI 개발, 휴대폰에서 써 보기 |
| [구조](docs/dev/architecture.md) | 디렉터리, 생성, 역할과 정책, 실시간과 오프라인, 백그라운드 작업 |
| [PostgreSQL](docs/dev/postgres.md) | PostgreSQL로 띄우기, 마이그레이션, 여러 대로 띄우기 |
| [검색 엔진](docs/dev/search.md) | OpenSearch와 nori, 색인, 명령, 개발용 클러스터 |
| [사람과 로그인](docs/dev/identity.md) | roster(내장과 외부), 테넌트 하나, SSO, 구성원 들이기, 사람이 떠날 때 |
| [과금과 결제](docs/dev/billing.md) | levyan, 미터, 제한과 읽기 전용, 워크스페이스의 결제 설정과 배너 |
| [라벨](docs/dev/labels.md) | 라벨 코드와 그 주소, 스캔 |
| [보안](docs/dev/security.md) | 콘텐츠 보안 정책, 세션의 키, 믿는 프록시, 로그인 제한 |
| [기록과 보존](docs/dev/retention.md) | 이력의 보존, 감사 기록과 보관소, 접속기록, 워크스페이스의 파기와 내보내기 |
| [테스트](docs/dev/testing.md) | Go 테스트, PostgreSQL과 OpenSearch에서, 브라우저 테스트(Playwright) |

## 운영 문서

- [`deploy/compose`](deploy/compose/README.md): Loft와 roster를 한 대에 띄우는 self-hosted 구성
- [`deploy/k8s`](deploy/k8s/README.md): Kubernetes 참조 배포
- [`deploy/observe`](deploy/observe/README.md): 대시보드
- 런북: [판을 올릴 때](docs/runbook/upgrade.md), [백업과 복구](docs/runbook/restore.md), [감사 기록 위변조 감지 대응](docs/runbook/trail-integrity.md), [보존 요청 대응](docs/runbook/preserve.md), [관측](docs/runbook/observe.md)
- [보안 점검](docs/security.md)

## 설계

설계는 [`design.md`](design.md), 개발 계획은 [`plan.md`](plan.md), 프로토타입을 만들며 내린 주요 결정은 [`report.md`](report.md)에 있습니다. 대부분은 [payday](https://github.com/lesomnus/payday)가 `proto/` 에서 생성하고, 코드를 고치는 규칙은 [`CLAUDE.md`](CLAUDE.md)에 있습니다.
