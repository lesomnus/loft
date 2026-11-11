# 터미널과 비서

모든 기능은 API(RPC)이고, 같은 기능을 터미널에서도 씁니다. 터미널은 브라우저와 같은 주소로 접속하고, 같은 권한 표와 같은 벽을 따릅니다. 이 문서는 모두에게 같은 부분입니다. 역할마다 쓰는 명령은 각 안내서에 있습니다.

## 설치

체크아웃에서 `loft` 를 PATH에 둡니다.

```sh
go install ./cmd/loft
```

이 문서의 명령은 `loft ...` 로 적습니다. 체크아웃에서 바로 돌린다면 `go run ./cmd/loft ...` 와 같습니다.

## 로그인

터미널의 로그인은 이미 로그인한 브라우저에서 승인해서 합니다. SSO만 쓰는 배포에서도 그대로 됩니다.

```sh
loft auth login --url http://localhost:8080   # 주소와 코드가 나오면, 로그인한 브라우저에서 열고 승인
loft auth login --no-browser                  # 브라우저를 열지 않고 주소만 출력 (--url을 빼면 지금 쓰는 연결의 주소)
loft auth whoami                              # 계정과 속한 워크스페이스
loft use loft                                 # 기본 워크스페이스 (하나뿐이면 필요 없음)
loft use                                      # 지금 기본으로 쓰는 워크스페이스
loft use --none                               # 기본을 지움
loft auth logout
```

- 승인 화면(`/cli?code=…`)은 요청한 터미널과 위치를 보여 줍니다. **내 터미널에서 방금 `loft auth login` 을 실행했고 같은 코드가 보일 때만 승인하세요.**
- 승인하면 그 계정의 세션이 터미널에 생깁니다. 브라우저와 같은 권한 표를 따르고, 유지 시간도 같으며, roster에서 계정이 중지·삭제되면 함께 끝납니다.
- 로그인은 연결(아래)로 `~/.config/loft/client.json` 에 주인만 읽을 수 있게 저장됩니다. 세션 키가 들어 있습니다. 다른 곳에 두려면 `LOFT_CLIENT_CONFIG` 에 경로를 줍니다. 연결이 생기기 전에 만든 파일은 읽지 않으니, `loft auth login` 을 다시 합니다.
- 워크스페이스가 둘 이상인 계정은 명령마다 어느 워크스페이스인지 밝힙니다. `loft use` 로 기본을 정하거나, 그때그때 명령 앞에 `--workspace` 를 줍니다(`loft --workspace loft asset ls`). 정하지 않으면 `this account is in more than one tenant` 로 거부됩니다.
- 한 주소에서 하는 터미널 로그인은 10분에 20번까지입니다.

### 좁혀서 로그인하기 (`--allow`)

터미널이 부를 수 있는 작업만 적어서 로그인할 수 있습니다(D33). 비서에게 맡기는 터미널이 그 정도만 하게 하려는 것입니다.

```sh
loft auth login --allow read                                          # 읽기만: 조회, 검색, 이력
loft auth login --allow read --allow '/loft.ReservationService/*'     # 읽기와 예약
loft auth login --allow '/loft.AssetService/Move'                     # 메서드 하나
```

- 패턴은 `/패키지.서비스/메서드` 이고, 어느 부분이든 `*` 로 쓸 수 있습니다(`/loft.*/Get`). `read` 는 읽기만 하는 메서드 모두입니다.
- 승인 화면에 그 목록이 나오고, 세션에 그대로 적힙니다. 서버가 모든 호출에서 지키므로, 세션 키를 가진 누구도 목록 밖은 하지 못합니다(`this credential is not for that`).
- 좁힌 세션은 RPC 옆의 HTTP 중 계정·동의 내역·라벨 조회 같은 읽기만 됩니다. 다른 터미널 승인, 데이터 내보내기, 워크스페이스 만들기, 약관 동의는 거부됩니다.
- `loft auth whoami` 가 그 목록을 보여 줍니다. 넓히려면 `--allow` 없이 다시 로그인합니다.

## 연결: 누구로 부르는지

명령은 같고, 누가 하는지는 연결이 정합니다(D75). 사람의 연결은 위의 로그인이고, 운영자의 연결은 운영자 진입점의 주소와 키가 있는 곳입니다([배포 운영자](operator.md)). 터미널은 이름 붙인 연결을 여럿 두고, 명령마다 하나를 씁니다.

```sh
loft auth list                          # 연결들과 지금 쓰는 것(*)
loft auth switch <이름>                  # 명령이 쓸 연결을 바꿈
loft --connection <이름> asset ls        # 이 명령만 다른 연결로
loft auth logout <이름>                  # 연결을 잊음 (사람의 것은 서버의 세션도 끝냄)
```

- 연결의 이름은 `loft auth login --name` 으로 주고, 주지 않으면 주소의 호스트(`localhost:8080`)입니다. 로그인한 연결이 지금 쓰는 연결이 됩니다.
- 명령이 쓰는 연결은 위에서부터 처음 것입니다. 같은 수준에서 둘을 주면 거부됩니다.
  1. 명령 앞의 `--connection 이름`, 또는 운영자의 `--key`(주소는 `--url`)
  2. 환경의 `LOFT_CONNECTION`, 또는 `LOFT_ADMIN_KEY`(주소는 `LOFT_ADMIN_URL`)
  3. `loft auth switch` 로 고른 것
- 운영자의 연결은 `loft auth login --operator --url http://127.0.0.1:8081 --key file:/경로/키` 로 둡니다. 키 자체가 아니라 키가 있는 곳(`env:이름`, `file:경로`)만 받고 그것만 저장하며, 두기 전에 진입점에 그 키가 누구인지 묻습니다. 파일에 두지 않고 그 명령만 부를 때는 `loft --url http://127.0.0.1:8081 --key file:/경로/키 <명령>` 입니다.
- `--workspace`, `LOFT_WORKSPACE`, `loft use` 는 사람의 연결의 것입니다. 운영자는 명령마다 워크스페이스를 주소로 주고, 운영자의 연결에 이것을 주면 거부됩니다.
- 계정의 일인 `loft use`, `loft consent`, `loft mcp` 는 운영자의 연결로는 거부됩니다. 다른 종류의 연결로 부른 작업은 어느 연결을 쓰라고 말합니다(`… is a person's, and this is an operator's connection: use a person's`).

## 스키마의 명령: 모든 RPC를 명령으로

```sh
loft asset ls -o wide                                   # 엔터티마다 get·list(ls)·watch·add·patch·erase
loft asset search -o wide '{"q": "노트북"}'              # 그 밖의 작업도 엔터티 아래에
loft asset move <자산 ID> '{"to": {"id": "<공간 ID>"}, "reason": "자리 배치"}'
loft call loft.AssetService.Search '{"q": "노트북"}'     # 아무 RPC나 이름으로
```

명령의 모양은 이렇습니다.

```text
loft [--workspace 주소] [--reason 이유] <엔터티> <작업> [-o 형식] [REF] [요청 JSON]
```

- **명령은 스키마의 이름이고, 작업은 RPC의 이름입니다.** `AddMany` 는 `add-many`, `SetRole` 은 `set-role` 입니다. 다른 말은 셋뿐입니다: `list` 의 `ls`, `tenant` 의 `workspace`·`ws`, `tenant configure` 의 `conf`·`config`.
- **플래그는 인자보다 앞에 씁니다.** `loft asset search '{"q": "x"}' -o wide` 는 `flag must come before arguments` 로 거부됩니다. 명령 앞의 플래그(`--workspace`, `--reason`, `--connection`)는 명령 이름보다 앞에 씁니다.
- **요청**은 JSON입니다. 필드 이름은 스키마의 이름(`date_start`, `dateStart` 모두 됨)이고, ID는 UUID로 씁니다. `-` 를 주면 stdin에서 읽습니다.
- **REF**는 그 행의 ID(UUID)입니다. 워크스페이스는 `@주소`(`@loft`), 구성원 자격은 `@워크스페이스/아이디`(`@loft/haeun-lee`)로도 씁니다.
- **출력**(`-o`)은 `pretty`(기본), `json`, `table`, `wide`(이름과 ID), `name`(ID만 한 줄에 하나), `protojson`, `prototext`, `template=…`(Go 템플릿, ID는 base64)입니다.
- 엔터티마다 할 수 있는 작업은 `loft <엔터티> --help` 가, 모든 명령은 `loft --help-all` 이 보여 줍니다. 도움말은 연결과 상관없이 같습니다. 할 수 없는 작업은 서버가 역할 표로 거부합니다(`구성원 역할로는 할 수 없는 작업입니다`).
- 다른 작업이 쓰는 행(`placement`, `link`, `stewardship`, `fact`, `reservation-item`, `allocation`, `custody-line`, `stock-movement`, `purchase-line`)은 도움말에 없지만, 이름으로 부르면 돌고(`loft fact list`) `loft call` 로도 닿습니다. 부를 수 있는지는 다른 작업처럼 서버가 정합니다.

### ID 찾기

화면의 주소에도 ID가 있지만, 터미널에서는 검색으로 찾습니다. `-o name` 은 ID만 출력하므로 셸 변수에 담기 좋습니다.

```sh
loft asset search -o wide '{"q": "PH-004"}'          # 태그, 이름, 시리얼, 모델, 사용자로
loft party search -o wide '{"q": "이하은"}'           # 사람·팀·거래처
loft item-model search -o wide '{"q": "M650"}'       # 모델
loft scan PH-004                                      # 라벨 코드·주소·태그가 가리키는 것

phone=$(loft asset search -o name '{"q": "PH-004"}')
loft asset timeline $phone
```

### 목록과 지켜보기

```sh
loft custody ls -o wide '{"filters": [{"status": "open"}]}'   # 거르기는 filters로
loft asset ls -n 20 -o wide                                   # 몇 개까지
loft asset ls -o json -n 20 | jq -r .next                     # 다음 쪽의 표시
loft asset ls -o wide --next <앞 답의 next>                     # 이어서 읽기
loft asset ls -o wide --reverse                               # 거꾸로
loft asset watch -o table <자산 ID>                            # 바뀔 때마다 한 줄 (--retry: 끊기면 다시)
```

엔터티마다 거를 수 있는 필드는 스키마가 정합니다(예: 지급·대여는 `party`, `status`, `kind`, 예약은 `party`, `status`).

### 내려받기

개인정보가 담긴 내려받기(자산 대장 CSV, 대여 현황 CSV, 워크스페이스의 모든 데이터)는 무엇에 쓰는지 명령 앞의 `--reason` 으로 밝혀야 하고, 그 이유는 접속기록에 남습니다. CSV는 답의 `data` 에 base64로 옵니다.

```sh
loft --reason "11월 자산 점검" asset export -o 'template={{.data}}' | base64 -d > 자산.csv
loft --reason "11월 대여 점검" custody export-outstanding -o 'template={{.data}}' | base64 -d > 대여.csv
```

### `loft call`

이름으로 아무 RPC나 부릅니다. 요청은 protojson 그대로라서, ID는 UUID가 아니라 base64로 씁니다. 엔터티 명령이 있는 것은 그쪽이 편합니다.

```sh
loft call loft.AssetService.Search '{"q": "노트북"}'
loft call /loft.PartyService/Me
```

### 워크스페이스

계정의 워크스페이스는 `tenant` 의 작업입니다. `loft workspace mine`, `loft ws mine` 도 같습니다.

```sh
loft tenant mine                                              # 속한 워크스페이스
loft tenant create '{"name": "연구실"}'                         # 워크스페이스 만들기
loft tenant configure @<주소> '{"name": "연구지원팀 장비"}'       # 소유한 워크스페이스의 이름 ("" 이면 소유자의 이름으로 부름)
loft --reason "보관" tenant export <주소>                        # 소유한 워크스페이스의 모든 데이터 (화면의 내려받기와 같음, --out 파일)
```

### 그 밖의 명령

```sh
loft scan 7K3M-Q9TX                                # 라벨이 가리키는 것 (내 다른 워크스페이스의 라벨도)
loft asset form 양식.csv                            # 대량 등록 양식
loft asset add-many 입고.csv                        # 무엇이 등록될지와 문제를 줄마다 (아무것도 바꾸지 않음)
loft asset add-many --yes --reason "11월 입고" 입고.csv
loft consent                                       # 동의해야 할 문서와 주소
loft consent --agree                               # 읽은 뒤 동의
```

터미널의 명령은 서버의 설정을 읽지 않습니다. 서버의 설정을 읽는 것은 서버 상자에서 쓰는 `init`, `migrate`, `serve`, `config` 뿐입니다([배포 운영자](operator.md)).

## 비서에게 맡기기 (MCP)

`loft mcp` 는 MCP 서버입니다(D32). Claude Code나 Claude Desktop 같은 MCP 클라이언트가 이 명령을 띄우면, 비서가 터미널에 로그인한 계정으로 워크스페이스를 읽고, 그 사람이 승인한 것을 바꿉니다. 먼저 `loft auth login` 을 하고, `loft` 가 PATH에 있어야 합니다.

```sh
claude mcp add --scope user loft -- loft mcp                      # Claude Code
claude mcp add --scope user loft-lab -- loft mcp --workspace lab  # 다른 워크스페이스는 따로
```

Claude Desktop은 설정 파일(`claude_desktop_config.json`)의 `mcpServers` 에 넣습니다.

```json
{
  "mcpServers": {
    "loft": { "command": "loft", "args": ["mcp"] }
  }
}
```

- **읽는 도구:**
  - 자산 찾기(`search_assets`), 자산 하나(`get_asset`), 이력(`asset_history`), 어느 시점의 모습(`asset_at`)
  - 최근에 일어난 일(`recent_events`, 설정 › 작업 기록과 같은 권한으로 관리자 이상과 감사자), 지급과 대여(`list_custody`), 예약(`reservations`), 예약 하나의 이력(`reservation_history`)
  - 사람과 팀 찾기(`find_people`), 모델 찾기(`find_models`)
  - 재고(`list_stock`), 작업 지시(`list_work_orders`), 실사와 그 결과(`list_counts`), 구매(`list_purchases`)
  - 리포트(`report`): 자산 현황, 대여 현황, 예약 이용률, 실사 결과, 재고 소모. 리포트 화면과 같은 권한(매니저 이상과 감사자)이고, CSV는 내지 않습니다.
  - 누구로 읽는지(`whoami`)
- **쓰기는 미리보기와 적용, 두 단계입니다(D33).**
  - 미리보기 도구는 아무것도 바꾸지 않습니다.
    - `plan_asset`: 등록, 이동, 속성 변경, 담당 지정, 연결, 정정, 등록 취소. 키트에 넣는 연결은 `required` 면 필수 구성품(키트를 예약하면 함께 잡힘), 아니면 선택 구성품이고, 미리보기가 어느 쪽인지 말합니다. 분실·불용·폐기로 바꾸는 미리보기는 그 자산이 남는 키트·그룹을 말하고, `leave_kits` 면 거기서도 뺍니다
    - `plan_custody`: 지급·대여, 반납, 기한 연장, 인수 확인
    - `plan_reservation`: 예약, 확정, 승인, 반려, 취소, 체크인, 완료. 분실·불용·폐기인 것의 예약과, 그렇게 된 것을 예약한 것의 확정·승인·체크인은 미리보기에서 거절하고 무엇이 어떤 상태인지 말합니다(서버도 거절합니다)
    - `plan_stock`: 재고 등록, 입고, 사용, 조정, 이동, 자산으로 전환
    - `plan_count`: 실사 시작, 스캔, 대조, 처리, 마감. 분실 처리도 `leave_kits` 로 키트·그룹에서 뺍니다
    - `plan_work_order`: 수리·점검 접수, 변경, 완료, 취소
    - `plan_purchase`: 구매 등록, 입고
    - `plan_model`: 모델 등록, 변경
    - `plan_label`: 라벨 만들기, 연결, 해제
  - 이 도구들은 바뀔 것을 이름으로 적은 문장("이동: NB-1 개발용 노트북, 본관 › 3층 창고 → 본관 › 4층 회의실 (놓임)")과 토큰을 돌려줍니다.
  - 사람이 그 문장을 보고 "예"라고 하면, 비서가 `apply` 에 토큰과 그 문장을 그대로 넘겨 실행합니다. 문장이 `apply` 의 인자라서, 클라이언트가 실행 전에 묻는 승인 창에도 그 문장이 보입니다.
  - 토큰은 10분 동안, 한 번만 쓰입니다. 같은 작업을 다시 보내도 서버는 한 번으로 처리합니다.
  - 이력에는 그 사람이 한 것으로 남습니다.
- **비서가 할 수 있는 것을 좁히려면** 그 터미널을 `loft auth login --allow` 로 로그인합니다. 예를 들어 `--allow read` 면 읽기만 하고, 미리보기 단계에서 거부됩니다.
- 브라우저와 같은 권한 표와 벽을 따르므로, 비서는 그 사람이 볼 수 있는 것만 봅니다. 서버는 MCP를 모르고, 새로 여는 것도 없습니다.
- 로그인은 부를 때마다 다시 읽습니다. `loft auth login` 과 `loft use` 는 비서를 다시 띄우지 않아도 다음 호출부터 적용됩니다.
- 도구의 답(자산과 사람의 이름 등)은 비서를 제공하는 쪽으로 갑니다. 그래서 사람의 이메일과 전화번호는 답에 넣지 않습니다.
- 결제(`loft payment ...`)는 비서의 도구에 아직 없습니다.
