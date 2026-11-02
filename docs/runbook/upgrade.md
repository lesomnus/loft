# 판을 올릴 때 한 번 할 일 (운영자)

새 빌드로 올리는 순서는 [운영자 안내서](../guide/operator.md#새-버전-올리기)와 `deploy/compose`, `deploy/k8s` 의 README에 있습니다. 이 문서는 어떤 판을 넘을 때 **한 번** 해야 하는 일입니다. 지금 배포가 그 판보다 앞이면, 올리기 전에 그 절을 읽습니다.

운영에서는 서버가 테이블을 직접 바꾸지 않습니다(`db.migrate: false`). 새 빌드를 띄우기 전에 `loft migrate` 로 테이블을 맞추고, 모양이 다르면 `serve` 는 시작하지 않습니다. 아래에서 "마이그레이션"은 그 `loft migrate` 입니다.

## 작업 기록과 자산의 사실을 payday의 과거로 옮기는 판

**이 판(payday#151)은 되돌릴 수 없습니다.** 그 `loft migrate` 는 Loft 자신의 Fact 표를 payday의 모양으로 한 번 옮기고(`the assets' facts moved into payday's past: N rewritten, N put on the trail`), 그 전 빌드는 새 표를 읽지 못합니다.

- 올리기 전에 DB를 백업합니다(`pg_dump`, [백업과 복구](restore.md)). 이 판을 넘는 `rollout undo` 는 하지 않습니다. 되돌리려면 백업에서 되살립니다.
- 옮긴 행을 감사 기록에 남기므로 마이그레이션에 다이제스트 키가 필요합니다(`audit.digest.key`). Kubernetes의 `migrate` 컨테이너는 `digest.key` 만 마운트합니다. 옮길 사실이 없는 DB는 키를 읽지 않습니다.
- 과거가 쓸 수 없는 사실(자산의 필드가 아닌 키, ID로 읽히지 않는 유형·모델, UTF-8이 아닌 글)이 있으면 종류별 건수와 함께 멈추고 아무것도 옮기지 않습니다. 옆에 둔 표(`loft_fact_aside`)를 고친 뒤 다시 실행하면 이어서 옮깁니다.
- 올린 뒤 `loft trail rows` 는 그 전에 쓴 작업 기록을 `N rows from before seals` 로 셉니다. 작업 기록의 사유(`reason`)가 이제 개인정보로 선언되어 봉인되기 때문입니다. 살펴본 뒤 `rows --accept --of <다이제스트> --why "사유가 개인정보가 되기 전의 작업 기록 (payday#151)"` 을 한 번 실행합니다.
- 새 작업 기록은 문장을 갖지 않고 종류의 메시지를 갖습니다. 문장은 읽을 때 만들고, 그 전 기록은 쓴 문장 그대로 읽힙니다. 유형과 모델의 사실은 소문자 정규 ID로 읽힙니다.

## 사람의 말인 메모를 개인정보로 선언하는 판

이 판(D56)부터 지급·대여의 메모, 예약·작업·구매·실사의 메모, 실사 발견의 메모, 접속기록 점검 결과, 워크스페이스를 닫은 사유는 감사 기록에 값으로 남지 않습니다.

- 올린 뒤 `loft trail rows` 는 그 전에 쓴 그 종류의 행을 `N rows from before seals` 로 셉니다. 살펴본 뒤 `rows --accept --of <다이제스트> --why "메모가 개인정보가 되기 전의 행 (D56)"` 을 한 번 실행합니다.
- 그 전에 쓴 행의 감사 기록에는 그 글이 그대로 남습니다. 마이그레이션으로 비우지 않습니다(design 8.2절 사람의 말인 자유 텍스트).

## 가입 때 생기는 워크스페이스에 이름을 쓰지 않는 판

이 판(D57)부터 그 워크스페이스는 이름이 비어 있고, 화면과 메일, 터미널은 소유자의 지금 이름으로 "○○의 워크스페이스"라고 부릅니다. 소유자의 이름을 보여 준 것이므로 그렇게 부른 호출은 접속기록에 남습니다(`GET /account` 도).

- 그 전에 만든 워크스페이스는 지은 이름("○○의 워크스페이스")을 그대로 갖고, 감사 기록에 남은 그 이름도 그대로입니다. 마이그레이션은 비우지 않습니다(design 9.11절).
- 운영자 진입점(`loft-admin workspace ls|show`)은 저장된 이름을 보이므로, 새로 생긴 그런 워크스페이스의 이름은 빈칸입니다. 주소와 소유자의 아이디로 찾습니다.

## 지급·대여가 그 시간을 잡는 판

이 판(D53)에서는 `loft migrate` 가 지금 나가 있는 것의 시간을 한 번 잡습니다. 그 전 빌드가 건넨 것은 아무 시간도 잡지 않았으므로, 마이그레이션이 건넨 때부터 잡습니다(`what is out holds its time: N things, held from when they were handed over`). 대여는 반납 기한까지, 지급과 기한이 지난 대여는 돌아올 때까지입니다.

- 서버를 거쳐 쓰므로 감사 기록에 그대로 남고, 다이제스트 키(`audit.digest.key`)를 읽습니다. 나가 있는 것이 없는 DB는 읽지 않습니다.
- 그 시간과 겹치는 예약은 그대로 남고 나가 있는 자산으로 보입니다. 예약한 사람과 관리하는 사람에게는 앱으로 알립니다. 마이그레이션은 메일을 보내지 않습니다. 풀은 하나가 줄어 맞지 않으면 가장 나중 예약이 기다립니다.
- 다시 실행하면 할 일이 없습니다. 그 전 빌드의 내보내기를 `loft tenant import` 로 되살릴 때도 같은 일을 합니다.

## 감사 기록과 접속기록을 시간으로 읽는 인덱스가 생기는 판

이 판(payday#157)에서는 `loft migrate` 가 인덱스 둘을 만듭니다. `audit_by_time` 과 `access_by_time` 이고, 둘 다 `(date_created, id)` 입니다. 워크스페이스나 종류로 좁히지 않고 배포 전체의 기록을 읽을 때 이 인덱스를 씁니다.

- 행이 있는 표에 인덱스를 만드는 동안 PostgreSQL은 그 표에 쓰기를 막습니다. payday가 잰 것으로는 감사 기록 100만 행에 7–9초, 접속기록 30만 행에 1–3초가 걸립니다. `Recreate` 로 올리는 배포([`deploy/k8s`](../../deploy/k8s/README.md))는 마이그레이션하는 동안 어차피 서비스가 내려가 있으므로 따로 할 일이 없습니다.
- 그만큼 쓰기를 멈출 수 없는 큰 배포는 올리기 전에 같은 이름으로 미리 만들어 둡니다. 마이그레이션은 만들어 둔 인덱스를 그대로 둡니다. `CONCURRENTLY` 는 트랜잭션 밖에서 한 문장씩 실행합니다.

  ```sql
  CREATE INDEX CONCURRENTLY IF NOT EXISTS audit_by_time ON audit (date_created, id);
  CREATE INDEX CONCURRENTLY IF NOT EXISTS access_by_time ON access (date_created, id);
  ```

- `CONCURRENTLY` 가 도중에 실패하면 그 이름의 인덱스가 쓸 수 없는(`INVALID`) 채로 남습니다. 쓰기마다 갱신되지만 어떤 읽기도 그 인덱스를 쓰지 않습니다. 다시 실행해도 `IF NOT EXISTS` 가 그것을 건너뜁니다. **`loft migrate` 는 스키마가 선언한 인덱스가 이렇게 남아 있으면 테이블을 맞추기 전에 멈춥니다(payday#176).** 멈추면서 인덱스와 표의 이름, 실행할 문장을 알려 줍니다. 그 문장을 트랜잭션 밖에서 한 문장씩 실행하고 `loft migrate` 를 다시 실행합니다. 짓고 있는 인덱스도 끝날 때까지는 `INVALID` 이므로, 짓는 중이면 끝난 뒤에 실행합니다.

  ```sql
  -- audit_by_time, on audit
  DROP INDEX CONCURRENTLY public.audit_by_time;
  CREATE INDEX CONCURRENTLY audit_by_time ON public.audit USING btree (date_created, id);
  ```

  - 만든 뒤 `\d audit` 와 `\d access` 에 `INVALID` 가 없는지 먼저 보면 올리는 도중에 멈추지 않습니다.
  - 마이그레이션이 직접 다시 만들지 않는 것은, 그러려면 쓰기를 막은 채 짓거나(미리 만든 이유가 없어집니다) 실패한 빌드만큼 오래 걸리고 같은 이유로 또 실패할 수 있는 빌드를 서버가 뜨기 전 단계에서 다시 돌려야 하기 때문입니다.
  - `loft serve`(`db.migrate: false`)는 이런 인덱스를 경고로 남기고(`db: serving over an index PostgreSQL left invalid`) 그대로 뜹니다. 느려질 뿐인 인덱스 때문에 재시작이 장애가 되지 않게 하려는 것입니다.
  - 스키마가 선언하지 않은 인덱스(직접 만든 인덱스, `REINDEX CONCURRENTLY` 가 남긴 `_ccnew`)는 멈추지 않고 경고만 남깁니다. `_ccnew` 는 지우기만 합니다.
- 내장 roster의 DB(`auth.roster.db`)에도 같은 이름의 인덱스 둘이 생깁니다. 내장 roster는 열 때마다 자기 DB를 마이그레이션하므로, 그 기록도 크다면 같은 두 문장을 그 DB에서도 먼저 실행합니다. 그 DB의 인덱스가 `INVALID` 로 남으면 `loft migrate` 와 `loft serve` 가 모두 `embedded roster:` 로 시작하는 같은 안내와 함께 멈춥니다. 안내의 문장은 그 DB에서 실행합니다.

## 세션을 키의 HMAC으로 보관하는 판

세션을 키 대신 키의 HMAC으로 보관하는 판([세션의 키](../dev/security.md#세션의-키))으로 올리면 지금 있는 세션은 맞지 않습니다. 모두 한 번 다시 로그인하고, 터미널도 `loft login` 을 다시 합니다. 그 밖에 할 일은 없습니다.

## 보관 기간을 워크스페이스가 정하는 판

이 판(D36, loft#144)으로 올리면 마이그레이션이 계약에 있던 보관·접속기록·복구 기간을 워크스페이스로 한 번 옮깁니다. 효력 중인 계약의 값을 옮기고, 그 전 계약의 값은 유예가 지킬 그 전 값으로 옮깁니다. 앞으로 효력이 생길 계약의 그 값은 옮기지 않습니다. 옮긴 뒤 한 번 해야 할 일은 [감사 기록 위변조 감지 대응](trail-integrity.md)에 있습니다.

## 행을 봉인하는 판

이 판(payday#115)으로 올린 뒤 봉인하기 전에 쓴 행(사람·구성원·초대·알림)은 `loft trail rows` 가 `N rows from before seals` 로 셉니다. 찾을 것은 아니지만 그 개인정보 필드는 아무것도 보증하지 않으므로, 살펴본 뒤 `rows --accept --of <다이제스트> --why` 를 한 번 실행해 봉인합니다. 다른 찾은 것이 없어도 받아들입니다([런북](trail-integrity.md#행의-봉인과-올린-뒤-한-번의-받아들임)). 봉인이 무엇인지는 [기록과 보존](../dev/retention.md#행의-봉인)에 있습니다.

## 감사 기록의 서명 키를 처음 둘 때

**키를 이미 쓰던 배포는 행에 서명하는 판으로 올린 뒤 `accept` 를 한 번 실행합니다.** 서명이 없는 행은 서명하기 전에 쓴 것인지, 누가 서명을 지운 것인지 구별할 수 없습니다. 그래서 키가 있으면 그런 행을 모두 찾아냅니다(`unsigned`). 살펴본 뒤 한 번 받아들이면 그다음부터는 깨끗합니다.

```sh
loft trail verify                                                 # 그전에 쓴 행마다 `unsigned`, 그리고 다이제스트
loft trail accept --of sha256:… --why "행에 서명하기 전에 쓴 것"   # 한 번
loft trail verify                                                 # 깨끗함
```

**키 없이 보관소를 쓰던 배포**도 키를 둔 뒤 `accept` 를 한 번 실행합니다.

- 키를 두면 그동안의 행과 체크포인트가 모두 `unsigned` 입니다. 그것들을 근거로는 아무것도 파기하지 않고, 그 뒤로는 체크포인트도 남기지 않습니다. 각 작업은 할 일을 한 뒤 오류로 그렇다고 알립니다(`no trusted key signed the checkpoints the store keeps`).
- 살펴본 뒤 `accept` 를 실행하면 지금의 행에 그대로 서명합니다. 그리고 DB가 마지막으로 번호를 매긴 체크포인트를 앞의 것으로 삼아 키 이전의 기록을 이어 받습니다.
- 행에 서명하고 나면 그 행이 가리키는 청크도 비교하게 되는데, 거기서 새로 찾은 것(서명 없던 행이 있다고 한 청크가 없음 등)은 살펴본 것이 아니라서 함께 받아들이지 않습니다. 그다음 `verify` 가 찾고, 따로 한 번 더 받아들입니다. 그래서 `accept` 가 두 번 필요할 수 있습니다.
- 이전 키를 믿지 않은 채 키를 바꾼 배포도 같은 말을 듣습니다. 그때는 `accept` 대신 이전 키의 공개 키를 `audit.checkpoints.trust` 에 넣습니다.

## 그 밖의 한 번

- **roster 이전의 배포**는 `loft identity migrate` 를 한 번 합니다([계정과 로그인](../dev/identity.md#roster-이전의-배포)).
- **동의 기록을 roster로 옮긴 판**(D37)은 예전 동의 기록을 지우고 계정마다 한 번 더 동의를 받습니다. `app.consent.keep` 이나 `audit.by.consent` 를 적어 둔 설정은 서버가 시작하지 않으므로 지웁니다([약관 동의](../dev/identity.md#약관-동의)).
- **새 비밀이 필요한 판**(예: 610c1c7부터의 `digest.key`)은 그 비밀을 먼저 넣고 올립니다. 순서는 `deploy/compose/README.md` 와 `deploy/k8s/README.md` 의 "새 버전" 절에 있습니다.
