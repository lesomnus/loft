# 과금과 결제 (levyan)

설정에 levyan이 없으면 아무것도 재지 않고, 아무것도 제한하지 않으며, levyan에 아무것도 알리지 않습니다. `app.billing.levyan` 으로 청구 서비스 [levyan](https://github.com/lesomnus/levyan)을 주면 연결 하나로 세 가지를 합니다(design 7장, D59, D60, D62, D64).

- 워크스페이스마다 가진 것을 재서 levyan에 넘깁니다. 금액은 levyan이 가격표대로 매깁니다(아래 표).
- levyan이 워크스페이스마다 주는 권한(Grant)을 이 배포의 DB에 사본으로 둡니다. levyan이 `loft.restricted` 를 켠 워크스페이스는 제한되고, `loft.readonly` 를 켠 워크스페이스는 읽기 전용입니다.
- 워크스페이스를 닫고, 되돌리고, 파기한 것을 levyan에 알립니다. levyan은 닫힌 동안 청구하지 않고, 파기하면 그때 청구를 끝냅니다.

화면을 쓰는 사람의 일은 [관리자·소유자](../guide/admin.md#결제-관리자-이상) 안내서에 있습니다. 결제 담당(지불자)이 결제 계정과 결제수단, 청구서를 다루는 것은 Loft의 일이 아닙니다(아래 "결제 화면").

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
- Kubernetes에서 키를 붙이는 곳은 [`deploy/k8s`](../../deploy/k8s/README.md)의 11입니다.

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
- **연결:** 미터, 권한, 워크스페이스의 소식(닫기·되돌리기·파기)이 연결 하나를 씁니다. 권한의 스트림이 늘 열려 있으므로 5분마다 keepalive를 보냅니다. levyan의 `server.keepalive.min_time` 은 5분보다 짧아야 합니다(짧지 않으면 levyan이 `too_many_pings` 로 끊고, 사본은 다시 연결해 맞춥니다).

## 제한

제한(design 7.5)은 levyan의 Grant를 lease 아래 한 프로세스가 따라 DB에 사본으로 둔 것입니다(루프 `payday.entitle.follow`).

- `loft.restricted` 가 켜진 워크스페이스는 청구되는 것을 늘리지 못합니다. 자산 등록(대량 등록, 재고 전환, 자산으로 받는 구매 입고 포함), 분실·폐기한 자산을 다시 쓰는 상태로 바꾸기, 첨부 올리기, 이력을 지금보다 오래 보관하기입니다. 새 구성원은 roster가 넣고 처음 로그인은 막지 않습니다(D87).
- 거절은 `FailedPrecondition` 이고 화면에 "이 워크스페이스는 제한되어 …"라고 나옵니다.
- 보기, 내보내기, 지급·반납, 예약, 이동, 처분·삭제, 결제는 그대로입니다.
- Grant가 없으면 제한하지 않으므로 levyan이 멈춰도 막히는 워크스페이스는 없습니다. 운영자의 명령(`loft holder add`, `loft tenant close` 같은 것)은 진입점으로 와도 막지 않습니다(design D83).
- 제한의 1단계이고, 유예 뒤에 일상 업무는 그대로 두고 늘리는 것만 막는다는 것은 대표가 정했습니다(levyan#1, 2026-11-01).

## 읽기 전용

읽기 전용(design 7.10)은 제한의 2단계입니다. levyan은 결제가 처음 실패한 청구서가 60일 뒤에도 결제되지 않은 워크스페이스와 결제 담당이 없어진 지 60일이 지난 워크스페이스에 `loft.readonly` 를 켜고, 결제되거나 누가 결제 담당을 받아들이면 제한과 함께 끕니다.

- 켜진 워크스페이스는 levyan의 `docs/apps.md` 3장이 말하는 것만 합니다. 곧 보기(리포트, 접속기록 읽기 포함), 내보내기와 다운로드(자산과 리포트의 CSV, 첨부), 결제, 떠나기(구성원 자격 끝내기, 개인정보 삭제)와, roster의 일인 비밀번호 바꾸기, 자기 알림을 읽음으로 표시하기와 메일 끄기(design 7.13)입니다. 닫기와 복구, 워크스페이스 전체의 내보내기는 운영자의 것이고, 운영자는 읽기 전용인 워크스페이스에도 합니다(design D83).
- 그 밖의 쓰기는 모두 막습니다. 자산, 예약, 지급·반납, 재고, 실사, 작업, 구매, 사람 기록, 접속기록 점검의 기록, 설정이 그렇습니다.
- 되는 것은 표 하나(`domain.ReadOnlyAllows`)에 있고, 표에 없는 쓰기는 막힙니다. 거절은 `FailedPrecondition` 이고 화면에 "이 워크스페이스는 읽기 전용이라 바꿀 수 없습니다 …"라고 나옵니다.
- 읽기 전용은 워크스페이스마다라서 그 구성원도 다른 워크스페이스에서는 그대로 일합니다. Grant가 없으면 읽기 전용이 아니고, 운영자의 명령은 막지 않습니다.
- 배포의 일(sweep)은 읽기 전용인 동안에도 돌아 예약이 만료되고 대여가 연체됩니다. 하지만 할 수 없는 일을 하라는 알림(반납 예정, 연체, 예약된 자산이 나가 있음, 풀 수량 부족, 다가오는 작업)은 앱으로도 메일로도 보내지 않습니다. 예약의 만료·해제·확보에 관한 알림은 그대로 갑니다. 다시 쓸 수 있게 되면 다음 sweep이 그때도 참인 것을 한 번 알립니다(design 7.13, `domain.ReadOnlyTells`).
- 읽기 전용인 동안 해제하거나 만료한 예약의 알림은 체크인·수령·승인을 할 수 없었다는 까닭을 말합니다. 운영자의 명령이 쓰는 알림은 종류와 상관없이 그대로 갑니다(design 7.14).
- 그렇게 해제한 예약은 노쇼가 아니라 만료이고, 이력은 "예약 만료: … (워크스페이스가 읽기 전용이던 때)"라고 하며 예약 이용률은 노쇼로 세지 않습니다(design 7.15).

## 지불자

지불자(design 7.6)는 levyan이 정합니다. 워크스페이스를 누가 만들었고 누가 떠났는지는 포털이 levyan에 알립니다. Loft는 levyan에 계정에 관한 것을 알리지 않습니다(D84).

## 생애 사건

생애 사건(design 7.8)은 워크스페이스를 닫을 때와 되돌릴 때(운영자, 구성원이 아무도 남지 않아 배포가 닫는 것 모두), 그리고 파기할 때입니다.

- 닫기와 되돌리기는 그 트랜잭션에 levyan에 알릴 것을 남기고(`LevyanNotice`), 파기는 마지막 측정을 넘긴 뒤 무엇이든 지우기 전에 남깁니다. 루프 `loft.levyan.notices` 가 10초마다 `WorkspaceService`(`Close`, `Restore`, `Destroy`)로 일어난 시각과 함께 보냅니다.
- 워크스페이스마다 일어난 순서대로입니다. levyan이 멈춰도 Loft의 쓰기는 기다리지도 실패하지도 않고, 알림이 기다렸다가 levyan이 돌아오면 나갑니다. 기다리는 알림이 있는데 levyan이 답하지 않으면 경고 로그(`levyan: what it is to be told of the workspaces waits`)가 남습니다.
- levyan이 그 워크스페이스를 들은 적 없다고 답하면(`NotFound`) 아무도 그 워크스페이스가 생겼다고 알리지 않은 것이라 levyan은 누구에게도 청구하지 않습니다. 보낸 것으로 표시하고 다시 보내지 않습니다(로그 `levyan: what became of a workspace it heard nothing of is not told again`).
- 그 밖에 levyan이 거부한 것은 Loft의 잘못이라 경고 로그(`levyan: what became of a workspace was refused, which is a fault of Loft's`)에 남기고 다시 보내지 않습니다.
- 루프가 처음 돌 때 그 전의 일을 한 번 알립니다. 남아 있는 닫기·되돌리기 이벤트를 오래된 것부터, 기록이 없는 곳은 지금 닫혀 있는 워크스페이스를 알립니다. 로그 `levyan: what became of the workspaces before it was told of it is to be told it` 가 몇 건인지 말합니다. levyan에 알리지 않던 동안 파기한 워크스페이스는 알리지 않습니다. 그것이 있었다는 것이 이 배포에 남지 않기 때문입니다(D86).

## 파기 전의 마지막 측정

청구하는 배포는 지우기 전에 잽니다(design 7.3). 닫힌 워크스페이스를 파기하는 sweep도, 운영자의 `loft tenant retire`·`purge --yes` 도 그 워크스페이스의 미터를 재서 levyan에 마지막 측정으로 넘긴 뒤에 지웁니다.

- **넘기지 못해도 지웁니다.** 파기는 기한이 있는 법적 의무라 청구 서비스를 기다리지 않습니다. 그때는 경고 로그(`closing: a workspace is destroyed without its final reading`)에 워크스페이스와 까닭이 남고, 명령은 그렇다고 한 줄(`meters: its final reading could not be handed to levyan, ...`) 말하고 성공합니다. levyan은 마지막 일일 측정을 이어서 매깁니다.
- 기다리는 시간은 호출의 끝(`timeout`, 기본 10초)까지입니다. `--yes` 없는 미리 보기는 재지 않습니다.
- 처음에는 넘기지 못하면 지우지 않았는데, Claude가 검토에서 이렇게 바꿨습니다.
- 그다음, 무엇이든 지우기 전에 levyan에 파기를 알릴 것을 남깁니다. 명령은 `levyan: it is to be told the workspace was destroyed …` 한 줄을 말하고, 돌고 있는 `loft serve` 가 보냅니다(design 7.8). 미리 보기는 남기지 않습니다.
- levyan이 답하지 않는 동안 복구 기간이 지난 워크스페이스는 마지막 측정 없이 파기되고, 경고 로그에 남습니다.

## 결제 화면

누가 무엇을 내는지는 levyan의 것이고, Loft는 levyan에 묻고 보여 줄 뿐 결제에 관한 것을 저장하지 않습니다(design 7.9, D63). 문은 `loft.PaymentService` 하나이고, `app.billing.levyan` 이 있으면 화면에 결제가 나타납니다. Loft에 있는 것은 워크스페이스의 것뿐입니다. 결제 담당(지불자)이 결제 계정과 결제수단을 다루고, 결제창을 열고, 제안을 받아들이고, 청구서와 명세서를 읽는 것은 포털의 일이라 Loft에 없습니다(#326, D81).

- **워크스페이스의 설정 › 결제**(관리자 이상): 누가 내는가(지금, 다음 기간부터, 제안, 수금 상태, 연체), 이번 기간 사용량과 예상 금액, 지난 명세서(명세서마다 "보기"로 그 기간의 줄). 운영자가 준 추가 무료량과 사용량 크레딧(남은 양, 이번 기간 뒤에 남을 양), 워크스페이스에 남은 크레딧, 그리고 줄마다 무료(가격의 무료량 + 프로모션)·사용량 크레딧·유료를 보입니다. 예상 합계는 크레딧을 빼기 전입니다. 소유자는 구성원에게 결제 담당을 제안합니다(자기에게도). 제안받은 사람이 받아들이면 이번 기간이 끝난 뒤부터 냅니다.
- **배너**: 워크스페이스가 제한되거나 읽기 전용이 되면 모든 구성원에게 그것과 그 까닭을, 결제 담당이 없거나 청구서의 결제가 실패했으면 소유자와 관리자에게 언제부터 제한되고 언제부터 읽기 전용이 되는지(levyan의 `date_readonly`)와 할 일을 말합니다. 그 워크스페이스를 내는 결제 계정으로 지금 결제할 수 없으면(결제수단 없음 같은 것) 무엇이 실패하기 전에 소유자와 관리자에게 그것도 말합니다. 제한은 아닙니다.
- **가는 곳**: 소유자와 관리자는 설정 › 결제로 가고, 그 밖의 구성원은 소유자나 관리자에게 알리라는 말을 봅니다. 지불자가 결제하는 곳으로 가는 링크는 없습니다. 무엇을 할지는 말로만 합니다(D81).
- **혜택과 크레딧**(design 7.11): 추가 무료량, 사용량 크레딧, 금액 크레딧은 levyan의 운영자가 줍니다(`levyan-admin allowance|pool|credit`). Loft에는 주는 길이 없고 levyan이 답하는 것을 보이기만 합니다. 수량은 levyan이 소수점 아래 6자리까지 말하는 것을 첫째 자리까지 반올림해 보이고(0보다 크고 0.1에 못 미치면 "0.1 미만"), 사용량 크레딧은 "자산·월"처럼 미터의 단위에 한 달을 곱한 단위로 말합니다.
- **터미널**: 모두 RPC라 `loft payment get-payer` 같은 명령이 됩니다. 비서(`loft mcp`)의 도구에는 아직 없습니다.
- **levyan이 거절하면** 그 말을 한국어 앞머리 뒤에 보입니다. 예를 들어 이미 내는 사람에게 다시 제안하면 "결제 서비스가 받지 않았습니다: the account pays for the workspace already"입니다. levyan이 닿지 않으면 "결제 서비스에 지금 닿지 않습니다"입니다.

## 테스트

Go 테스트와 브라우저 테스트는 가짜 levyan(`internal/levyantest`)에 청구합니다. Loft 밖에서 하는 일(포털이 알리는 워크스페이스를 만든 사람, 지불자의 결제 계정 열기와 제안 받아들이기)은 테스트가 그 가짜에게 말합니다([테스트](testing.md#브라우저-테스트-playwright)).
