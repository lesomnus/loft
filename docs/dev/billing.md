# 과금과 결제 (levyan)

설정에 levyan이 없으면 아무것도 재지 않고, 아무것도 제한하지 않으며, levyan에 아무것도 알리지 않습니다. `app.billing.levyan` 으로 청구 서비스 [levyan](https://github.com/lesomnus/levyan)을 주면 연결 하나로 네 가지를 합니다(design 7장, D59, D60, D62, D64).

- 워크스페이스마다 가진 것을 재서 levyan에 넘깁니다. 금액은 levyan이 가격표대로 매깁니다(아래 표).
- levyan이 워크스페이스마다 주는 권한(Grant)을 이 배포의 DB에 사본으로 둡니다. levyan이 `loft.restricted` 를 켠 워크스페이스는 제한되고, `loft.readonly` 를 켠 워크스페이스는 읽기 전용입니다.
- 워크스페이스를 누가 만들었는지와 어느 계정이 떠났는지를 levyan에 알립니다. levyan은 그것으로 누가 내는지(지불자)를 정합니다.
- 워크스페이스를 닫고, 되돌리고, 파기한 것을 levyan에 알립니다. levyan은 닫힌 동안 청구하지 않고, 파기하면 그때 청구를 끝냅니다.

화면을 쓰는 사람의 일은 [결제 담당](../guide/payer.md)과 [관리자·소유자](../guide/admin.md#결제-관리자-이상) 안내서에 있습니다.

## 무엇을 재나

| 미터 | 단위 | 세는 것 |
| --- | --- | --- |
| `loft.assets.active` | `{asset}` | 지우지 않은 자산 중 처분(`disposed`)·분실(`lost`)이 아닌 것. 종류를 가리지 않습니다 |
| `loft.attachments.bytes` | `By` | 지우지 않은 첨부의 크기 합 |
| `loft.members` | `{member}` | 지우지 않은 구성원 자격 |
| `loft.history.retention_days` | `d` | 지금 적용 중인 이력 보존 기간의 일수. 0은 영원히입니다. 줄인 기간은 유예(`app.retention.grace`)가 지난 날부터입니다 |

- **언제:** `serve` 가 시작할 때 한 번, 그 뒤로 자정(UTC)마다 모든 워크스페이스를 잽니다(루프 `payday.meter.read`, 배포에서 한 프로세스). 닫힌 워크스페이스도 파기될 때까지 잽니다. 파기하기 직전에 한 번 더 재서 마지막 측정으로 넘깁니다(아래 "파기 전의 마지막 측정").
- 흐름(전송량 같은 것)은 아직 없습니다(levyan#14).

## 설정

```yaml
app:
  billing:
    levyan:
      addr: levyan.levyan.svc:50051            # levyan serve의 gRPC 주소(server.addr)
      key: file:/run/secrets/loft-levyan/key   # levyan-admin key issue가 준 제품 키(lvk_…). env:NAME, 또는 값 그대로
      insecure: true                           # 평문. 클러스터 안에서 NetworkPolicy가 그 사이를 막을 때만. 없으면 TLS
      # tls:                                   # TLS일 때: 사설 CA, 이 배포가 내밀 인증서, 인증서에서 확인할 이름
      #   ca_file: /run/secrets/loft-levyan/ca.pem
      #   cert_file: /run/secrets/loft-levyan/tls.crt
      #   key_file: /run/secrets/loft-levyan/tls.key
      #   server_name: levyan
      # timeout: 10s                           # 호출 하나가 기다리는 시간. 기본 10초
```

환경변수로는 `LOFT_APP_BILLING_LEVYAN_ADDR`, `LOFT_APP_BILLING_LEVYAN_KEY`, `LOFT_APP_BILLING_LEVYAN_INSECURE`, `LOFT_APP_BILLING_LEVYAN_TLS_CA_FILE` 처럼 씁니다(`loft config env`).

- 키는 로그에도 오류에도 남지 않습니다. 키가 없거나 `lvk_` 로 시작하지 않으면 서버가 뜨지 않습니다.
- `insecure: true` 가 아니면 TLS입니다. 키를 싣는 연결이라, 블록 하나를 빠뜨렸다고 평문으로 보내지 않습니다. `tls.enabled` 는 따로 쓰지 않아도 됩니다.
- Kubernetes에서 키를 붙이는 곳은 [`deploy/k8s`](../../deploy/k8s/README.md)의 12입니다.

## levyan에서 할 일

levyan의 운영자가 제품을 세우고 키를 발급하고, 가격표에 위의 미터 넷을 **같은 키와 단위로** 더합니다. levyan에서는 미터의 키·단위·종류·집계를 나중에 바꿀 수 없습니다. 집계는 가격을 정할 때 정하고, 아래는 예입니다.

```sh
levyan-admin product add --name Loft --yes loft
levyan-admin key issue --alias loft --yes loft   # 키는 이때 한 번만 나옵니다
levyan-admin meter add --unit '{asset}' --kind stock --aggregation time_weighted_average --name '활성 자산' --yes loft loft.assets.active
levyan-admin meter add --unit By --kind stock --aggregation time_weighted_average --name '첨부 저장량' --yes loft loft.attachments.bytes
levyan-admin meter add --unit '{member}' --kind stock --aggregation max --name '구성원' --yes loft loft.members
levyan-admin meter add --unit d --kind stock --aggregation last --unbounded-zero --name '이력 보존 기간' --yes loft loft.history.retention_days
```

## 실패했을 때

- levyan이 답하지 않거나 늦으면(`timeout`) 그 값을 1분 뒤부터 점점 길게, 다음 자정 전까지 그대로 다시 보냅니다. levyan은 같은 값(워크스페이스, 미터, 잰 시각)을 한 번만 남기므로 다시 보내도 됩니다.
- levyan이 거부한 값(가격표에 없는 미터, 미터의 것이 아닌 단위)은 경고 로그(`levyan: a reading was refused`)에 남기고 다시 보내지 않습니다. 가격표에 미터를 더하면 다음 측정부터 받습니다.
- **연결:** 미터, 권한, 지불자 알림이 연결 하나를 씁니다. 권한의 스트림이 늘 열려 있으므로 5분마다 keepalive를 보냅니다. levyan의 `server.keepalive.min_time` 은 5분보다 짧아야 합니다(짧지 않으면 levyan이 `too_many_pings` 로 끊고, 사본은 다시 연결해 맞춥니다).

## 제한

제한(design 7.5)은 levyan의 Grant를 lease 아래 한 프로세스가 따라 DB에 사본으로 둔 것입니다(루프 `payday.entitle.follow`).

- `loft.restricted` 가 켜진 워크스페이스는 청구되는 것을 늘리지 못합니다. 자산 등록(대량 등록, 재고 전환, 자산으로 받는 구매 입고 포함), 분실·폐기한 자산을 다시 쓰는 상태로 바꾸기, 첨부 올리기, 로그인 발급·초대 보내기·받아들이기, 이력을 지금보다 오래 보관하기입니다.
- 거절은 `FailedPrecondition` 이고 화면에 "이 워크스페이스는 제한되어 …"라고 나옵니다.
- 보기, 내보내기, 지급·반납, 예약, 이동, 처분·삭제, 닫기, 결제는 그대로입니다.
- Grant가 없으면 제한하지 않으므로 levyan이 멈춰도 막히는 워크스페이스는 없습니다. 운영자의 명령(`loft holder add` 같은 것)은 막지 않습니다.
- 제한의 1단계이고, 유예 뒤에 일상 업무는 그대로 두고 늘리는 것만 막는다는 것은 대표가 정했습니다(levyan#1, 2026-11-01).

## 읽기 전용

읽기 전용(design 7.10)은 제한의 2단계입니다. levyan은 결제가 처음 실패한 청구서가 60일 뒤에도 결제되지 않은 워크스페이스와 결제 담당이 없어진 지 60일이 지난 워크스페이스에 `loft.readonly` 를 켜고, 결제되거나 누가 결제 담당을 받아들이면 제한과 함께 끕니다.

- 켜진 워크스페이스는 levyan의 `docs/apps.md` 3장이 말하는 것만 합니다. 곧 보기(리포트, 접속기록 읽기 포함), 내보내기와 다운로드(자산과 리포트의 CSV, 첨부, 워크스페이스 내려받기), 결제, 닫기, 떠나기(구성원 자격 끝내기, 개인정보 삭제)와, 계정의 일인 비밀번호 바꾸기, 자기 알림을 읽음으로 표시하기와 메일 끄기(design 7.13)입니다.
- 그 밖의 쓰기는 모두 막습니다. 자산, 예약, 지급·반납, 재고, 실사, 작업, 구매, 구성원과 초대, 접속기록 점검의 기록, 설정, 닫은 워크스페이스의 복구가 그렇습니다.
- 되는 것은 표 하나(`domain.ReadOnlyAllows`)에 있고, 표에 없는 쓰기는 막힙니다. 거절은 `FailedPrecondition` 이고 화면에 "이 워크스페이스는 읽기 전용이라 바꿀 수 없습니다 …"라고 나옵니다.
- 읽기 전용은 워크스페이스마다라서 그 구성원도 다른 워크스페이스에서는 그대로 일합니다. Grant가 없으면 읽기 전용이 아니고, 운영자의 명령은 막지 않습니다.
- 배포의 일(sweep)은 읽기 전용인 동안에도 돌아 예약이 만료되고 대여가 연체됩니다. 하지만 할 수 없는 일을 하라는 알림(반납 예정, 연체, 예약된 자산이 나가 있음, 풀 수량 부족, 다가오는 작업)은 앱으로도 메일로도 보내지 않습니다. 예약의 만료·해제·확보와 도메인에 관한 알림은 그대로 갑니다. 다시 쓸 수 있게 되면 다음 sweep이 그때도 참인 것을 한 번 알립니다(design 7.13, `domain.ReadOnlyTells`).
- 읽기 전용인 동안 해제하거나 만료한 예약의 알림은 체크인·수령·승인을 할 수 없었다는 까닭을 말하고, 도메인의 알림은 그동안 할 수 없는 확인과 활성화를 권하지 않습니다. 운영자의 명령이 쓰는 알림은 종류와 상관없이 그대로 갑니다(design 7.14).
- 그렇게 해제한 예약은 노쇼가 아니라 만료이고, 이력은 "예약 만료: … (워크스페이스가 읽기 전용이던 때)"라고 하며 예약 이용률은 노쇼로 세지 않습니다(design 7.15).

## 지불자

지불자(design 7.6)는 levyan이 정합니다. Loft는 워크스페이스를 만들 때와 구성원 자격이 끝날 때, 같은 트랜잭션에 levyan에 알릴 것을 남기고(`LevyanNotice`), 루프 `loft.levyan.notices` 가 10초마다 levyan의 `WorkspaceService`(`Arrive`, `Leave`)로 보냅니다.

- 워크스페이스마다 일어난 순서대로입니다. levyan이 멈춰도 Loft의 쓰기는 기다리지도 실패하지도 않고, 알림이 기다렸다가 levyan이 돌아오면 나갑니다.
- 루프가 처음 돌 때, levyan에 알린 적 없는 워크스페이스를 한 번씩 만든 때와 첫 소유자로 알립니다(levyan을 붙이기 전에 있던 것).
- 기다리는 알림이 있는데 levyan이 답하지 않으면 경고 로그(`levyan: what it is to be told of the workspaces waits`)가 남습니다.

## 생애 사건

생애 사건(design 7.8)은 워크스페이스를 닫을 때와 되돌릴 때(소유자, 운영자, 구성원이 아무도 남지 않아 배포가 닫는 것 모두), 그리고 파기할 때입니다.

- 닫기와 되돌리기는 그 트랜잭션에 levyan에 알릴 것을 남기고, 파기는 마지막 측정을 넘긴 뒤 무엇이든 지우기 전에 남깁니다. 같은 루프가 `WorkspaceService`(`Close`, `Restore`, `Destroy`)로 일어난 시각과 함께 보냅니다.
- levyan이 그 워크스페이스를 들은 적 없다고 답하면(`NotFound`) `Arrive` 를 먼저 보내고 다시 보냅니다.
- levyan이 거부한 것은 Loft의 잘못이라 경고 로그(`levyan: what became of a workspace was refused, which is a fault of Loft's`)에 남기고 다시 보내지 않습니다.
- 루프가 처음 돌 때 그 전의 일을 한 번 알립니다. 남아 있는 닫기·되돌리기 이벤트를 오래된 것부터, 기록이 없는 곳은 지금 닫혀 있는 워크스페이스를, 그리고 levyan에 알린 워크스페이스 가운데 파기된 것을 주소 보류의 시각으로 알립니다. 로그 `levyan: what became of the workspaces before it was told of it is to be told it` 가 몇 건인지 말합니다.

## 파기 전의 마지막 측정

청구하는 배포는 지우기 전에 잽니다(design 7.3). 닫힌 워크스페이스를 파기하는 sweep도, 운영자의 `loft tenant retire`·`purge --yes` 도 그 워크스페이스의 미터를 재서 levyan에 마지막 측정으로 넘긴 뒤에 지웁니다.

- **넘기지 못해도 지웁니다.** 파기는 기한이 있는 법적 의무라 청구 서비스를 기다리지 않습니다. 그때는 경고 로그(`closing: a workspace is destroyed without its final reading`)에 워크스페이스와 까닭이 남고, 명령은 그렇다고 한 줄(`meters: its final reading could not be handed to levyan, ...`) 말하고 성공합니다. levyan은 마지막 일일 측정을 이어서 매깁니다.
- 기다리는 시간은 호출의 끝(`timeout`, 기본 10초)까지입니다. `--yes` 없는 미리 보기는 재지 않습니다.
- 처음에는 넘기지 못하면 지우지 않았는데, Claude가 검토에서 이렇게 바꿨습니다.
- 그다음, 무엇이든 지우기 전에 levyan에 파기를 알릴 것을 남깁니다. 명령은 `levyan: it is to be told the workspace was destroyed …` 한 줄을 말하고, 돌고 있는 `loft serve` 가 보냅니다(design 7.8). 미리 보기는 남기지 않습니다.
- levyan이 답하지 않는 동안 복구 기간이 지난 워크스페이스는 마지막 측정 없이 파기되고, 경고 로그에 남습니다.

## 결제 화면

누가 무엇을 내는지는 levyan의 것이고, Loft는 levyan에 묻고 보여 줄 뿐 결제에 관한 것을 저장하지 않습니다(design 7.9, D63). 문은 `loft.PaymentService` 하나이고, `app.billing.levyan` 이 있으면 화면에 결제가 나타납니다.

- **계정의 결제 화면**(`/billing`, 워크스페이스 전환기의 "결제"): 결제 계정(나라만 고르면 채널과 통화는 levyan이 정합니다), 결제수단 등록(Paddle의 결제창), 수금 상태와 구독, 받은 제안과 내는 워크스페이스, 청구서와 정정 청구, 결제가 실패한 청구서(연체).
  - 결제 계정마다 받는 사람과 사업자 정보(세금계산서에 적을 것)를 만들 때 적고 "청구 정보 바꾸기"로 고칩니다. 계좌이체 결제 계정의 사업자 정보는 계약에 따른 것이라 운영자가 바꿉니다.
  - 지금 결제할 수 없는 결제 계정(결제수단 없음 같은 것)은 맨 위에 할 일과 함께 모아 보입니다.
  - 날짜는 브라우저가 어디에 있든 배포의 시간대(`app.locale.timezone`)로 보입니다.
  - 계정 자신의 호출이라 어느 워크스페이스에서도 같습니다. levyan에는 이 배포의 제품 키와 로그인한 roster 계정(`levyan-account`)으로 전합니다.
  - 카드는 levyan이 답하는 종류, 브랜드, 끝 네 자리만 보입니다.
  - 결제 계정마다 다음 청구서가 결제수단보다 먼저 쓸 크레딧(그 결제 계정의 것과 그것으로 내는 워크스페이스의 것, 남은 금액과 만료일, 30일 안에 만료되면 곧 만료)과 잔액을 보이고, 청구서에는 크레딧이 낸 금액과 청구한 금액, 그리고 줄마다 그 명세서(levyan이 지불자에게 답하는 것: 미터마다 사용량·무료·사용량 크레딧·유료·금액, design 7.12)를 보입니다.
  - 휴대폰(너비 520px 이하)에서는 청구서와 정정 청구를 표 대신 한 건씩 상자로 보이고, 청구서는 그 상자를 눌러 엽니다(design 7.9).
- **워크스페이스의 설정 › 결제**(관리자 이상): 누가 내는가, 이번 기간 사용량과 예상 금액, 지난 명세서(명세서마다 "보기"로 그 기간의 줄). 운영자가 준 추가 무료량과 사용량 크레딧(남은 양, 이번 기간 뒤에 남을 양), 워크스페이스에 남은 크레딧, 그리고 줄마다 무료(가격의 무료량 + 프로모션)·사용량 크레딧·유료를 보입니다. 예상 합계는 크레딧을 빼기 전입니다. 소유자는 구성원에게 결제 담당을 제안하고, 제안받은 사람은 자기 결제 화면에서 받아들입니다(이번 기간이 끝난 뒤부터 냅니다). 소유자가 직접 내려면 "내가 결제 담당 맡기"에서 결제 계정을 골라 한 번에 맡습니다. 이것도 이번 기간이 끝난 뒤부터입니다.
- **배너**: 워크스페이스가 제한되거나 읽기 전용이 되면 모든 구성원에게 그것과 그 까닭을, 결제 담당이 없거나 청구서의 결제가 실패했으면 소유자와 관리자에게 언제부터 제한되고 언제부터 읽기 전용이 되는지(levyan의 `readonly_from`)와 할 일을 말합니다. 읽기 전용이면 소유자·관리자·지불자에게 밀린 청구서를 결제하면 풀린다는 것과 갈 곳(지불자는 결제 화면, 소유자와 관리자는 설정 › 결제)을 말합니다. 그 워크스페이스를 내는 결제 계정으로 지금 결제할 수 없으면(결제수단 없음 같은 것) 무엇이 실패하기 전에 소유자와 관리자에게 그것도 말합니다. 제한은 아닙니다.
- **혜택과 크레딧**(design 7.11): 추가 무료량, 사용량 크레딧, 금액 크레딧은 levyan의 운영자가 줍니다(`levyan-admin allowance|pool|credit`). Loft에는 주는 길이 없고 levyan이 답하는 것을 보이기만 합니다. 수량은 levyan이 소수점 아래 6자리까지 말하는 것을 첫째 자리까지 반올림해 보이고(0보다 크고 0.1에 못 미치면 "0.1 미만"), 사용량 크레딧은 "자산·월"처럼 미터의 단위에 한 달을 곱한 단위로 말합니다. 명세서의 줄은 워크스페이스의 설정 › 결제(지난 명세서)에 있고, 지불자에게는 결제 화면의 청구서에 줄마다 있습니다. 지불자가 읽는 것은 levyan이 지불자에게 답하는 것(그 결제 계정이 그 기간에 낸 명세서)이고, Loft가 제품 키로 대신 읽지 않습니다(design 7.12).
- **결제수단 등록**은 결제 링크 페이지(`/billing/checkout`, 아래)에서 Paddle의 결제창을 열고 결제 화면으로 돌아옵니다. 그래서 `app.billing.checkout` 이 있어야 하고, 그 환경(sandbox, live)이 levyan의 MoR 설정의 환경과 같아야 합니다.
- **터미널**: 모두 RPC라 `loft api payment list-billing-accounts`, `loft api --workspace loft payment get-payer` 같은 명령이 됩니다. 비서(`loft mcp`)의 도구에는 아직 없습니다.
- **levyan이 거절하면** 그 말을 한국어 앞머리 뒤에 보입니다. 예를 들어 levyan에 roster가 설정되지 않았으면 결제 화면이 "결제 정보를 불러오지 못했습니다. 결제 서비스가 받지 않았습니다: levyan asks roster whether an account is in good standing …"이라고 말합니다. levyan이 닿지 않으면 "결제 서비스에 지금 닿지 않습니다"입니다.

## 결제 링크 페이지 (Paddle)

Paddle은 거래마다 결제창 링크를 만듭니다. 기본 결제 링크(default payment link)에 `?_ptxn=<거래 ID>` 를 붙인 것이고, 결제수단을 바꾸라는 Paddle의 메일도 이 링크를 씁니다. Loft의 `/billing/checkout` 이 그 페이지입니다(design 7.7, D61). 페이지가 Paddle.js를 불러 초기화하면 Paddle.js가 주소의 거래로 결제창을 스스로 엽니다. `_ptxn` 이 없으면 결제할 것이 없다고 말합니다. 로그인하지 않아도 열립니다.

```yaml
app:
  billing:
    checkout:
      environment: sandbox                           # sandbox 또는 live
      client_token: test_000000000000000000000000000 # 그 환경의 클라이언트 토큰(이 값은 예)
```

- 환경변수로는 `LOFT_APP_BILLING_CHECKOUT_ENVIRONMENT`, `LOFT_APP_BILLING_CHECKOUT_CLIENT_TOKEN` 입니다.
- 클라이언트 토큰은 Paddle의 **Paddle > My account > Settings > Authentication** 의 Client-side tokens에서 만듭니다. 공개용이라 비밀이 아닙니다(브라우저마다 갑니다). 그래도 저장소에는 넣지 않습니다.
- 샌드박스는 `test_`, live는 `live_` 로 시작해야 하고, Paddle이 말하는 모양(접두사 뒤 영숫자 27자)이어야 합니다. 아니면 설정을 읽을 때 그 위치(`loft.yaml:줄:열` 또는 환경변수)와 함께 거부하고 서버가 뜨지 않습니다. `pdl_` 로 시작하는 API 키나 알림의 비밀을 넣으면 공개되는 값이므로 거부합니다. 오류는 값을 말하지 않습니다.
- 설정이 없으면 페이지가 없습니다(404).
- Paddle 쪽에서는 **Paddle > Checkout > Checkout configuration** 의 기본 결제 링크를 `https://<이 배포의 주소>/billing/checkout` 으로 둡니다. live에서는 그 도메인이 승인되어 있어야 합니다(**Website approval**). 샌드박스는 승인 없이 됩니다.
- 이 페이지만 Paddle의 스크립트를 부르므로, 콘텐츠 보안 정책도 이 페이지에만 Paddle의 호스트를 더합니다([보안](security.md#콘텐츠-보안-정책-csp)).

## 테스트

Go 테스트와 브라우저 테스트는 가짜 levyan(`internal/levyantest`)에 청구합니다. Paddle.js도 테스트의 가짜(`ts/e2e/paddle.ts`)이고, Paddle에는 아무 요청도 가지 않습니다([테스트](testing.md#브라우저-테스트-playwright)).
