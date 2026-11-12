# 구조

Loft는 [payday](https://github.com/lesomnus/payday) 앱입니다. 대부분은 payday가 `proto/` 에서 생성하고, 생성할 수 없는 것(도메인의 규칙, 화면)을 그 위에 씁니다. 왜 이렇게 생겼는지는 [`design.md`](../../design.md), 일하는 규칙은 [`CLAUDE.md`](../../CLAUDE.md)에 있습니다.

```
proto/loft/*.proto          엔터티 (스키마의 원천)
proto/ext/loft/*.ext.proto  엔터티에 더한 RPC
server/domain/              도메인 계층: 시간 기록, 예약 충돌, 지급, 재고, 실사 … 그리고 백그라운드 작업
server/policy/              처음 역할 다섯과 줄 수 있는 RPC, roster에 묻는 게이트
server/session/             로그인(roster에 묻는다)과 DB에 두는 세션, GET /session
server/tenancy/             워크스페이스가 처음 갖는 것, 처음 로그인한 사람이 구성원이 되는 일, 주소가 roster의 것을 따르는 일
internal/identity/          roster: 따로 운영하는 것(gRPC) 또는 프로세스 안의 것(bufconn)
server/storage/             첨부 파일과 서명된 다운로드 주소
cmd/, cli/                  서버 조립, init, serve
ts/src/                     React UI
```

## 생성

스키마를 고쳤다면 다시 생성합니다.

```sh
go tool pd gen .          # Go
go tool pd gen --ts .     # TypeScript
go tool pd gen --check .  # 생성물이 스키마와 같은지 (CI가 하는 일)
```

생성 파일(`*.g.go`, `*.pb.go`, `server/bare/`, `server/pd/`, `internal/ent/`, `ts/gen/`)은 직접 고치지 않습니다. 자세한 규칙은 [`CLAUDE.md`](../../CLAUDE.md)에 있습니다.

## 역할과 정책

누가 무엇을 할 수 있는지는 **roster의 역할**입니다(D89). 게이트는 호출마다 부르는 사람이 그 RPC를 부를 수 있는지 roster에 묻고(`policy.Policy`, roster의 `reach`), 어떤 역할인지는 묻지 않습니다. 역할마다 하는 일은 [안내서](../guide/README.md)에 있습니다.

- 처음 역할은 **소유자 · 관리자 · 매니저 · 구성원 · 감사자**(`loft-owner` … `loft-auditor`)입니다. 처음 담는 메서드는 [`server/policy/policy.go`](../../server/policy/policy.go)가 정하고 `server/policy/testdata/roles.txt` 에 메서드마다 적혀 있습니다. `loft init` 이 roster에 쓰고, 그 뒤로는 테넌트가 roster에서 고칩니다.
- 처음 역할 어디에도 없는 메서드는 roster에 묻기 전에 거부됩니다. 새 RPC는 `policy.go` 의 역할에 더하기 전까지 닫혀 있습니다. 감사자는 읽기에 더해, 관리자의 것인 감사 기록·접속기록 읽기와 리포트를 받습니다.
- 메서드보다 잘게 나눈 권한은 `PermissionService` 의 메서드이고(`proto/loft/permission.proto`), 도메인 계층이 roster에 묻습니다(`Tx.may`). 화면은 `PartyService.Me` 의 `methods` 로 메뉴와 버튼을 그립니다.
- 모든 호출은 그 배포가 섬기는 워크스페이스의 구성원으로 처리합니다(D85).
- 아직 어느 역할에도 주지 않은 메서드(예: `EventService.Redact`)는 닫힌 것으로 적혀 있어 roster에서 담아도 거부됩니다. 워크스페이스를 닫고, 되돌리고, 모두 내보내는 것(`TenantService.Close`·`Restore`·`Export`)은 운영자의 것이라 어느 역할에도 없습니다(design D83).
- 도메인 계층(`server/domain`)은 게이트 위에서 생성된 동사를 완성하고(`Asset.Add`, `Reservation.Add` …) 나머지를 더합니다. 작업 하나가 트랜잭션 하나이고, 작업 기록(Event)을 남기며, 그것이 말하는 상태와 함께 시간 행을 씁니다.

## 실시간

자산 상세, 예약 캘린더, 진행 중인 실사는 다른 사람이 바꾼 것이 새로고침 없이 바로 보입니다. 화면은 자기가 그리는 것을 대표하는 행(자산, 예약 자원의 `schedule_version`, 실사의 `findings_version`)을 Watch하다가 바뀌면 다시 읽습니다. 스트림이 끊기면 1초에서 30초까지 늘려 가며 다시 열고, Watch가 없는 배포(`watch.broker: none`)에서는 조용히 예전처럼 동작합니다. 여러 서버면 `watch.broker: postgres` 로 다른 서버의 변경도 전합니다([PostgreSQL](postgres.md#여러-대로-띄우려면)).

## 오프라인

연결이 끊겨도 앱이 열리고, 이 브라우저에서 마지막으로 본 화면을 그대로 보여 줍니다. 오프라인에서는 읽기만 하고, 실사 스캔만 모아 두었다가 연결되면 보냅니다. 사람에게 보이는 동작은 [안내서의 오프라인](../guide/README.md#오프라인)에 있습니다.

- 앱 자체는 서비스 워커(`ts/sw.js`)가 보관합니다. 빌드한 UI를 서버가 줄 때만 등록합니다(`npm run dev` 에서는 아님).
- 본 내용(IndexedDB)과 보내지 못한 스캔은 구성원과 워크스페이스별로 보관합니다(`ts/src/offline.ts`).
- 화면은 모든 호출에 자기가 누구의 것인지(`payday-identity`, `GET /session` 의 `identity`)를 실어 보냅니다. 같은 브라우저의 다른 탭에서 다른 사람이 로그인하면, 남겨진 화면의 호출은 그 사람으로 처리되지 않고 거절됩니다. 그러면 화면은 지금 누가 로그인했는지 다시 묻고 그 사람으로 다시 시작합니다.

## 백그라운드 작업

예약 만료, 노쇼 해제, 연체 알림, 메일, 탈퇴 처리, 워크스페이스 파기, 색인, levyan에 알리는 일 같은 백그라운드 작업은 `cmd/serve.go` 가 띄우는 루프이고, 배포에서 한 프로세스만 돌립니다(`spin.lease`, payday#54). 루프마다 이름이 있습니다(`loft.search.index`, `loft.levyan.notices`, `payday.meter.read` 등). 리스는 장애가 넘어가는 순간에는 둘일 수 있어서, 루프는 두 번 돌아도 괜찮게 짜여 있습니다.

## 터미널과 비서

`loft <엔터티> <작업>` 은 실행 중인 배포의 모든 RPC를 명령으로 부릅니다(`cli/tree.go`, D75). 누구로 부를지는 연결이 정합니다. 사람의 연결은 브라우저가 쓰는 같은 주소의 Connect로 말하고(`cli/connect.go`), 운영자의 연결은 운영자 진입점에 키를 들고 말합니다(`cli/connection.go`). 터미널의 명령은 서버의 설정을 읽지 않습니다. `loft mcp` 는 그 터미널을 MCP 서버로 감싼 것입니다(`cli/mcp.go`, D32). 쓰는 법은 [터미널과 비서](../guide/terminal.md)에 있습니다.
