# Rove — 범용 유형자산 관리 플랫폼 기획 및 구현 설계

> 상태: 설계 초안 v2 (2026-10-10) — 1차 검토 반영, 구현 기반을 [payday](https://github.com/lesomnus/payday)로 확정, Phase 0 스파이크 결과 반영. 개발 계획은 [plan.md](plan.md)  
> 목표: 물리적 자산과 공간의 식별·구성·소유·위치·사용·예약·변경 이력을 통합 관리하는 멀티테넌트 SaaS.  
> 원칙: ITAM에 종속되지 않고, 기본 기능은 관대하게 제공하며 기업이 필요로 하는 **이력 보존·감사·관리·연동·규모**를 유료화할 수 있도록 한다. **가격과 요금제는 추후 결정한다.**  
> 이번 개정에서 정한 것은 이유와 함께 [12장 결정 기록](#12-결정-기록)에 모았다.  
> 2026-10-11: 제품군과 두 층의 테넌트(D27), SaaS의 로그인과 주소(D28), 워크스페이스 닫기와 alias(D29).  
> 2026-10-12: 초대(D30).  
> 2026-10-16: 검색 엔진(D31)과 비서(MCP, D32, D33).  
> 2026-10-18: 가져오기의 범위, 서명된 내보내기와 복구, 이벤트의 원인(D34).

## 1. 제품 정의와 원칙

Rove는 컴퓨터, 부품, 책상, 의자, 공구, 연구·촬영 장비, 차량, 비품뿐 아니라 **회의실·작업실·창고·스튜디오 같은 공간**까지 다루는 범용 자산 레지스트리이자 자산 운영 플랫폼이다. 자산은 시간에 따라 이동하고, 담당자와 구성 및 사용 상태가 바뀐다. Rove는 이러한 변화를 기록하여 특정 시점의 상태를 조회할 수 있게 한다.

- **자산 식별과 자산 구성은 별개**: 부품은 필요하면 독립 Asset으로 등록; 대량 비품·소모품은 Stock으로 수량 관리.
- **정체성 + 시간 행 + 이벤트**: 자산의 정체성(`Asset`), 기간을 가진 상태(배치·담당·속성, 3.3절의 시간 행), 업무 작업 기록(`Event`)을 분리한다.
- **시간은 일급 개념**: 모든 상태 변화는 효력 시각(실제로 일어난 때)과 기록 시각(시스템이 알게 된 때)을 함께 가진다. 소급 기록과 정정은 예외가 아니라 일상이다. 첫 가져오기부터 과거 데이터이고, 오프라인 실사는 늦게 도착하며, 실사 결과는 과거를 고친다.
- **현장 업무 우선**: QR 스캔, 지급·반납, 실사, 예약, 장착·분리 같은 도메인 작업을 API로 노출한다. 필드 단위의 일반 쓰기는 열지 않는다.
- **플러그인은 코어의 데이터 경로를 우회하지 않음**: 타입·Capability·조건식으로 뷰와 도메인 확장을 고르고, 플러그인 데이터도 테넌트 경계·이력·보존·내보내기·계측을 그대로 거친다.
- **과금 정책과 업무 로직 분리**: 지금부터 계측할 수 있게 설계하되 초기에는 제한 없이 운영한다.
- **삭제는 제품의 계약**: 보존 기간 만료 삭제를 지원하되 고객 고지·내보내기·유예·법적 보존 및 백업 정책을 정의하기 전에는 파괴적 동작을 활성화하지 않는다.

### 1.1 첫 고객 가설과 비목표

- **가설 고객**: 공용 장비와 공간을 여러 사람이 빌려 쓰고 위치와 구성이 자주 바뀌는 조직. 연구실, 학교·메이커스페이스, 촬영·제작사, 스튜디오. 구성원 10~500명, 자산 500~5만 개.
- **검증할 가치**: "그때 무엇이 어디에, 누구에게, 어떤 구성으로 있었나"에 답하는 것, 그리고 QR 기반 지급·반납.
- **비목표**
  - 회의실 전용 예약의 대체. 회의실은 보통 Google·Microsoft 캘린더가 권위이므로, 공간 예약은 장비와 함께 쓰이거나 캘린더 밖에서 운영되는 공간(스튜디오·실험실·작업실)에 집중한다.
  - IT 자산 자동 탐지와 패치 관리. OS 인벤토리는 외부 도구와 대조하는 연동으로만 다룬다.
  - 회계 원장. 취득·처분 기록과 회계 시스템으로의 내보내기까지만 한다.
  - 테넌트 사이의 자산 대여·이관.
  - GPS·센서 같은 고빈도 텔레메트리 저장. 구역 진입·이탈처럼 의미 있는 변화만 이벤트로 받는다.

### 1.2 비기능 요구(초기 목표)

| 항목 | 목표 |
| --- | --- |
| 테넌트 규모 | 자산 10만 개, 시간 행 연 100만 건, 동시 사용자 200명 |
| 응답 시간 | 단건 조회·목록 p95 300ms 미만, 1,000노드 이하 하위 트리의 시점 조회 p95 1초 미만 |
| 가용성 | 99.5% (자체 서버 한 대, 단일 복제본으로 시작) |
| 복구 | RPO 1시간(WAL 보관 기반 PITR, 다른 장소에 백업), RTO 4시간, 분기마다 복구 리허설 |
| 지역화 | 한국어 우선, 영어 병행. 테넌트별 시간대와 통화(워크스페이스 설정, 비우면 배포의 `app.locale`), 저장은 UTC. 금액은 통화 최소 단위의 정수(원, 센트)이고 행마다 통화를 갖는다 |
| 클라이언트 | 최신 데스크톱·모바일 브라우저(PWA), 카메라 QR 스캔 |

**측정 (2026-10-17, 스테이징, rove#99).**

| 항목 | 측정 |
| --- | --- |
| 테넌트 규모 | 자산 101,111개(물품 10만, 공간 1,111), 시간 행 약 71만 건(위치 10만, 속성 61만), 감사 행 101만 건, DB 1.06GB. 동시 사용자 200명으로 시험 |
| 응답 시간 | 동시 200명(초당 약 170건)에서 p95: 단건 조회 24ms, 목록 22ms, 검색 44ms, 시점 조회(한 층, 1,011노드 이하) 94ms. 1,011노드가 모두 있는 시점은 106ms(동시 20명) |
| 처리량 | 쉬는 시간 없이 200명이 몰면 초당 약 460건에서 멈춥니다. 먼저 차는 것은 PostgreSQL의 CPU(12코어의 96%)입니다 |
| 가용성 | 재지 않았습니다. 배포와 설정 변경마다 파드를 바꿔 끼우므로(`Recreate`) 그때마다 잠깐 끊깁니다 |
| 복구 | RTO 64~79초(DB 수 MB), 68초(DB 1GB): 사고부터 다른 namespace에서 첫 로그인까지. RPO: 정상 종료는 0이었습니다(마지막 WAL까지 보관). 갑작스러운 장애의 상한은 WAL이 `archive_timeout` 5분, 볼륨(첨부, 감사 기록)이 복사 주기 1시간입니다 |
| 클라이언트 | 휴대폰 QR·PWA 확인은 아직 하지 않았습니다 |

- **환경:** 4GB, 12코어 노드 세 대(k3s). Rove 파드와 PostgreSQL 18(CloudNativePG, 인스턴스 하나)이 서로 다른 노드에 있고, OpenSearch 3.9 + nori(힙 512MB)가 세 번째 노드에 있습니다. 부하는 같은 망의 다른 컴퓨터에서 k6로 걸었습니다.
- **부하의 모양:** 사용자마다 0.5~1.5초 간격으로 단건 조회(태그) 40%, 방 하나의 목록(50개) 30%, 검색 20%, 지난 1년 중 어느 달 1일의 한 층 시점 조회 10%를 섞었습니다.
- **가져오기:** 시트로 고치는 가져오기는 D34로 없앴습니다. 마지막으로 잰 5,000행 미리 보기는 22초였습니다(처음에는 128초, rove#103). 신규 대량 등록은 묶어 쓰므로 만든 뒤 다시 잽니다.
- 목표는 모두 넉넉히 맞습니다. 다만 DB가 Rove와 다른 노드에 있어 문장마다 왕복이 붙는 구성입니다. 한 대 서버라면 더 빠르고, DB가 커지면 다시 잽니다.

### 1.3 제품군

| | SaaS | Dedicated | CE |
| --- | --- | --- | --- |
| 무엇 | 우리가 roster와 Rove를 운영하고, 누구나 가입해 자기 워크스페이스를 만든다 | 한 고객을 위해 독립된 Rove 인스턴스를 운영한다 | 고객이 직접 설치한다 |
| roster의 테넌트 | 하나. SaaS의 계정 전체가 들어 있는 계정 디렉터리 | 고객마다 하나. 그 회사의 IdP와 SCIM이 붙는다 | 내장 roster, 또는 고객의 roster |
| roster가 보는 앱의 모양 | A: 인스턴스 하나가 여러 테넌트 | B: 테넌트마다 인스턴스 | 내장, 또는 C: 테넌트가 직접 운영 |
| 로그인 | Google·Microsoft·GitHub(SSO만) | 회사 IdP | 비밀번호(옵션), 또는 고객의 roster |

**지금은 SaaS만 만든다.** Dedicated와 CE는 같은 코드에서 설정과 roster의 모양만 바꿔 열 수 있게 둔다. 워크스페이스는 어느 제품군에서나 Rove의 테넌트다(9.10절, D27).

## 2. 범위 및 기능 목록

Phase 번호는 10장의 구현 단계와 같다.

| 영역 | 기능 | Phase |
| --- | --- | --- |
| 자산 레지스트리 | 내부 ID와 자산 번호(태그), 유형·모델, 사용자 정의 속성, 사진·문서, 검색·필터, 신규 대량 등록(rove 양식), CSV/Excel 내보내기, 서명된 내보내기와 복구(D34) | 0–1 |
| 그래프 | 물리 배치(단일 부모)와 장착 슬롯, 논리 그룹, 담당 관계, 관계 검증 / 키트 | 0–1 / 2 |
| 이력 | 배치·담당·속성의 이원 시간 이력, 소급 기록과 정정, 시점 조회, 두 시점 Diff, 업무 이벤트 | 0–1 |
| 공간 | 공간 Asset과 계층, 용량·설비, QR / 운영시간·예약 정책 | 1 / 2 |
| 지급/반납 | Custody 문서, 인수 확인, 반납 / 연체 알림 | 1 / 2 |
| 예약/대여 | 공간·장비·키트 예약, 홀드, 승인, 충돌 방지, 체크인, 취소, no-show, 반복 예약 / 외부 캘린더 | 2 / 3 |
| 재고 | 수량형 재고, 소모품 입출고, 임계값 알림, 개별 자산으로 전환 | 2 |
| 실사 | QR/바코드 스캔, 정기 실사, 위치·담당자 불일치, 분실 보고, 셀프 실사, 오프라인 실사 | 2 |
| 구매/재무 | 업체, 구매, 영수증, 취득원가, 보증, 처분 / 감가상각 | 2 / 3 |
| 유지보수 | 고장·수리·RMA, 정기 점검, 작업 지시와 정비 블록 / TCO | 2 / 3 |
| 현장/자동화 | 모바일 PWA·QR 인쇄 / 오프라인 실사 / RFID·GPS 연동, OS 인벤토리 대조 | 1 / 2 / 3 |
| 통합/기업 기능 | roster 로그인(SaaS는 Google·Microsoft·GitHub SSO)과 역할 / API 토큰·Webhook, 기업 IdP·SCIM(roster), 사업장 단위 권한, ERP·HR·캘린더 연동, 고급 보고서 | 0 / 3 |
| 플러그인 | 빌드 시 등록하는 뷰 플러그인 / 백엔드 도메인 모듈 / 서드파티 | 1 / 2–3 / 3 이후 |
| SaaS 계측 | Rove 밖의 계량 작업이 원천(DB 읽기 복제본, 첨부 저장소의 목록)에서 잰다(7장) / 사용량 원장·Entitlement·과금(Rove 밖) | 0 / 3 |

**MVP는 Phase 0 + Phase 1**이다. 시간 복원 자산 레지스트리, 공간 계층, QR, 지급·반납까지를 담는다. 공간·장비 예약은 MVP 직후 Phase 2의 첫 기능이다. 차별점인 시점 복원을 먼저 검증하고, 충돌·승인·캘린더처럼 범위가 큰 예약은 그 위에 얹는다.

## 3. 도메인 모델

### 3.1 핵심 엔터티

| 엔터티 | 역할 |
| --- | --- |
| `Tenant` (payday) | 워크스페이스. 데이터·접근·과금의 경계. roster의 테넌트(계정 디렉터리)와는 다른 층이다(9.10절, D27) |
| `Holder` (payday) | 워크스페이스의 구성원 자격과 역할. 계정은 roster의 것이고 Holder는 그 계정을 가리킨다(9.10절). 한 계정이 여러 워크스페이스에 속하면 워크스페이스마다 Holder가 하나씩 있다 |
| `Audit` / `Outbox` (payday) | 모든 쓰기의 시스템 감사 기록(쓰기와 같은 트랜잭션), 발행 큐. 감사 기록은 기본으로 쓰기가 쓴 내용(patch)을 남기고, 행 전체는 `audit.content: value`일 때만 남긴다. personal로 선언한 사람의 값은 어느 수준에서도 남기지 않는다(payday#92) |
| `Asset` | 추적 대상의 공통 정체성. `kind` = `ITEM`(실물) / `SPACE`(장소) / `KIT` / `GROUP`(풀·논리 그룹). 관계·QR·첨부·플러그인이 모두 여기에 붙는다 |
| `AssetType` | 테넌트별 유형, 속성 스키마(버전), 기본 Capability, 단일 상속 |
| `ItemModel` | 제조사·모델·규격·슬롯 정의. 여러 Asset이 같은 모델을 참조 |
| `Party` | 사람·조직 단위·법인·업체. 개인정보는 이 엔터티에만 둔다. 선택적으로 Holder와 연결 |
| `Placement` | 물리 배치. 자식 Asset이 부모 Asset(공간·장비·랙) 안에 있다. 한 시점에 부모 하나, 슬롯 점유 |
| `Link` | 논리 관계. 키트·그룹 소속(`MEMBER_OF`), 연결(`CONNECTED_TO`). 다대다 |
| `Stewardship` | Asset과 Party의 담당 관계. `OWNER` / `MANAGER` / `CUSTODIAN`. 역할마다 한 시점에 한 명 |
| `Fact` | Asset 속성 이력. "이 시각부터 이 키의 값은 V"라는 단언 |
| `Event` | 업무 작업 하나의 기록. 종류, 수행자, 발생 시각, 사유, 만들고 대체한 시간 행 |
| `TreeLock` | 테넌트마다 하나인 잠금 행. 배치 트리를 바꾸는 작업을 직렬화한다(3.2절) |
| `Label` | QR/바코드 라벨. 워크스페이스와 무관한 전역 코드로 인쇄하고(9.9절), 미리 인쇄한 뒤 연결하거나 교체할 수 있다 |
| `TenantDomain` | 테넌트가 쓰는 자기 도메인(선택). 지금은 QR 라벨용이다(9.9절) |
| `Attachment` | 사진·계약서·증빙의 메타데이터와 객체 저장소 키 |
| `Custody` / `CustodyLine` | 지급·대여 문서와 품목 줄. 인수 확인, 반납 기한, 부분 반납 |
| `Bookable` | Asset의 예약 정책 프로파일 |
| `Reservation` / `ReservationItem` / `Allocation` | 예약, 요청한 자원, 실제로 시간대를 막는 할당 행 |
| `Stock` / `StockMovement` | 수량형 재고의 잔량(모델 × 보관 공간)과 입출고 원장 |
| `InventoryCount` / `CountFinding` | 실사 세션과 발견한 차이 |
| `WorkOrder` | 고장·수리·점검 작업. 정비 블록을 만든다 |
| `Purchase` / `PurchaseLine` | 취득과 원가의 출처. 원가 이중 계산을 막는다 |
| `SpaceProfile` | 공간 Asset의 면적, 정원, 설비, 주소·좌표(사업장 수준) |

**공간**: 사업장·건물·층·방·구역·선반 같은 모든 장소는 `Asset(kind=SPACE)`이고, 장소 계층은 `Placement` 트리 하나다. 이전 초안의 `Location` 엔터티는 없앴다. 주소와 좌표는 `SpaceProfile`의 값이고, 예약 가능 여부는 `Bookable` 프로파일이 정한다. 과금과 목록에서 공간을 따로 셀지는 `kind`로 구분한다(11장).

**사람과 계정**: `Party`(사람 기록), `Holder`(워크스페이스의 구성원 자격), 계정(roster)은 다르다. 자산을 지급받는 직원은 계정이 없어도 된다. 둘을 잇는 것은 `Party.account → Holder`(선택, 하나의 Holder에 하나의 Party)다. 계정과 그 로그인 수단(Google·Microsoft·GitHub의 계정)은 roster가 갖는다(9.10절). 그래서 Rove에는 `Identity` 엔터티가 없다. 행위자는 사용자만이 아니다. API 토큰, 가져오기 작업, 보존 잡 같은 시스템 작업도 행위자이며, 시스템 작업은 Holder 없이 기록된다. 이력과 Event에는 Party·Holder의 ID만 남기고 이름·이메일을 복사하지 않는다(8.2절).

### 3.2 관계 규칙

- **물리 배치는 자식마다 한 시점에 부모 하나다.** 놓임(`LOCATED`), 장착(`INSTALLED`), 구성품(`PART`)은 별도 관계 타입이 아니라 `Placement.mode`로 구분한다. 타입을 나누면 "부모 하나" 규칙이 타입 사이를 넘나들며 깨진다. 예를 들어 컴퓨터에 장착된 부품이 동시에 다른 방에 놓인 것으로 기록될 수 있다.
- **슬롯**: `ItemModel`(또는 `AssetType`)이 슬롯을 정의한다. 이름 붙은 슬롯(CPU0, DIMM3)이나 랙 U 범위다. 한 슬롯, 또는 겹치는 U 범위에는 한 시점에 하나만 들어간다.
- **순환 금지**: Placement 트리에는 순환이 없다. 배치를 바꾸는 작업은 테넌트당 하나인 잠금 행을 먼저 갱신해 직렬화하고, 그 뒤 영향을 받는 기간 전체에 대해 조상 경로를 재귀로 검사한다. 자식과 부모 행만 잠그는 방식으로는 부족하다. x→y를 "x를 y 안에 둔다"로 읽을 때, b→…→c와 d→…→a 경로가 이미 있는 상태에서 a→b와 c→d가 동시에 들어오면 두 작업은 서로 다른 행을 잠그고 각자 검사를 통과해 함께 순환을 만든다. PostgreSQL에서 이 경우를 재현했고, 테넌트 잠금 행을 먼저 잡으면 두 번째 작업이 거부된다(확인함).
- **Link**는 다대다이고 배타 규칙이 없다. 키트 소속은 `Link(MEMBER_OF)`이며 필수 구성품 여부를 속성으로 가진다.
- **Stewardship**은 (자산, 역할)마다 한 시점에 한 명이다. `CUSTODIAN`은 Custody 문서로만 열고 닫는다.
- **상속은 계산값이다.** 부품의 현재 위치와 담당자는 Placement 사슬을 따라 계산하며, 직접 기록과 구분한다.
- **관계 종류는 Phase 2까지 시스템이 정한 값이다**(Placement mode, Link kind, Stewardship role). 사용자 정의 관계 타입은 확장 단계에서 다룬다.
- **모든 관계의 양 끝은 같은 테넌트다**(6장, payday `agrees`).

### 3.3 시간과 이력

**시간 행**은 `Placement`·`Link`·`Stewardship`(기간 `valid_from`~`valid_to`)과 `Fact`(시작 `valid_from`만 있고 같은 키의 다음 Fact까지 유효)다. 모든 시간 행은 기록 시각 `recorded_at`(payday `date_created`, 서버가 찍는다)과 대체 시각 `superseded_at`을 가진다.

**규칙: 시간 행은 대체 표시를 한 번 하는 것 외에는 바꾸지 않는다.** 바꿀 일이 생기면 기존 행에 `superseded_at`과 그렇게 한 Event(`superseded_by`)를 찍고, 바뀐 기간으로 새 행을 쓴다. 일반적인 이동·반납도, 과거를 고치는 정정도 같은 방식이다. 그래서 어느 시점에 시스템이 무엇을 알고 있었는지가 모두 남는다. 대체 표시를 먼저 하고 새 행을 쓴다. PostgreSQL 제약은 문장마다 바로 검사하므로, 새 행을 먼저 넣으면 아직 대체되지 않은 행과 겹쳐 거부된다(확인함).

```text
10/1 기록: 9/1부터 A실             → #1 A실 [9/1, ∞)
10/5 기록: 9/15에 B실로 옮겼다      → #1 대체, #2 A실 [9/1, 9/15), #3 B실 [9/15, ∞)
10/9 정정: 옮긴 날은 9/12였다       → #2·#3 대체, #4 A실 [9/1, 9/12), #5 B실 [9/12, ∞)

QueryAt(9/13)      = B실 (#5)   지금 아는 사실 기준
AsOf(9/13, 10/6)   = A실 (#2)   10/6에 시스템이 보여 준 상태
AsOf(9/20, 10/2)   = A실 (#1)
```

- **조회의 의미**
  - `QueryAt(T)`: 대체되지 않은 행으로 본 T 시점의 상태. 기본 조회다.
  - `AsOf(T, K)`: K 시점에 기록돼 있던 지식으로 본 T 시점의 상태. `recorded_at ≤ K`이고 `superseded_at`이 없거나 K보다 뒤인 행을 쓴다. 감사와 분쟁 대응용이며 유료 기능 후보다.
  - `Diff(T1, T2)`: 두 `QueryAt` 결과의 차이.
  - `Timeline`: 자산 하나의 시간 행과 Event를 효력 시각 순으로 나열한다.
- **속성 이력이 before/after가 아니라 Fact인 이유**: before/after는 '현재 값' 기준의 diff라서 과거 구간만 바꾸는 변경을 표현하지 못한다. 10/1에 이미 '폐기'가 된 자산에 10/5에 "9/20~10/1은 수리 중이었다"를 기록하면 현재 값은 그대로이고, 이 변경의 before/after는 정의되지 않는다. Fact는 "이 시각부터 값은 V"라는 단언이므로 중간에 끼워 넣어도 그대로 성립한다. 속성 키는 표시 이름이 아니라 불변 키로 저장해, 이름을 바꿔도 이력이 깨지지 않게 한다.
- **현재 상태**: `Asset` 행에 현재 부모(`parent`), 현재 보유자(`custodian`), 생애주기 상태 같은 값을 비정규화해 둔다. 도메인 작업이 같은 트랜잭션에서 시간 행과 함께 갱신하며, 변경이 '지금'에 영향을 줄 때만 갱신한다. 목록·검색·watch는 현재 행을, 과거 조회는 시간 행을 읽는다.
- **Event**: 도메인 작업 하나마다 하나를 남긴다. 필드는 `kind`, 수행자, `occurred_at`, `recorded_at`, `reason`, `trace_id`다. 시간 행은 자신을 만든 Event(`event`)와 대체한 Event(`superseded_by`)를 가리킨다. 모든 쓰기의 시스템 감사는 payday trail이 맡고, Event는 업무 의미("누가 왜 옮겼나")를 맡는다. 둘은 보존 기간이 다르다(8장).
- **원인**: 여러 대상에 걸친 작업은 부모 Event 하나와, 영향을 받은 대상마다 자식 Event를 남긴다. 자식은 원인(`cause`)으로 부모를 가리키고, 주 대상은 그 대상이다. 대상의 이력에는 그 대상에 일어난 일이 그 대상의 Event로 있다(9.13절, D34).
- **시각 검증**: 효력 시각은 미래일 수 없다(허용 오차 5분). 오프라인 기기의 시각은 그대로 받되, 서버가 받은 시각(`recorded_at`)과 함께 남긴다.
- **과거 복원의 한계**: 보존 기간이 지나 지운 구간은 복원할 수 없다. API와 화면은 조회 가능한 가장 이른 시각을 함께 알려 준다.

### 3.4 주요 스키마 예시

payday의 헤더 필드(1 id, 2 tenant, 4 alias, 5 name, 6 desc, 7 labels, 13~15 시각)를 따르고, Rove 필드는 8~12와 16 이후 번호를 쓴다. 필드 3은 사업장(Site) 축으로 비워 둔다(6장).

```text
Asset(id, tenant, alias?, name, desc, labels, kind, type, model?, tag, serial?,
      status, condition, parent?, custodian?, attributes, acquired_at?, disposed_at?,
      date_updated, date_erased, date_created)
AssetType(id, tenant, parent?, schema, schema_version, capabilities)
ItemModel(id, tenant, maker, model, slots)
Party(id, tenant, kind, name, contact, parent?, account?, date_erased)

Placement(id, tenant, child, parent, mode, slot?, u_from?, u_to?,
          valid_from, valid_to?, event, superseded_at?, superseded_by?, date_created)
Link(id, tenant, source, target, kind, attrs, valid_from, valid_to?,
     event, superseded_at?, superseded_by?, date_created)
Stewardship(id, tenant, asset, party, role, valid_from, valid_to?,
            event, superseded_at?, superseded_by?, date_created)
Fact(id, tenant, asset, key, value, valid_from, event, superseded_at?, superseded_by?, date_created)
Event(id, tenant, kind, actor_id, occurred_at, reason, trace_id, payload, date_created)
TreeLock(id, tenant, version)

Label(id, tenant, domain, subject_id?, state, bound_at?, printed_at?)
TenantDomain(id, tenant, host, purpose, state, token, verified_at?)
Attachment(id, tenant, subject_id, object_key, size_bytes, content_type, sha256)
Custody(id, tenant, kind, party, reservation?, issued_at, due_at?, acknowledged_at?, status)
CustodyLine(id, tenant, custody, asset? | stock? + quantity, out_at, returned_at?,
            condition_out, condition_in?)
Bookable(id, tenant, asset, timezone, opening_hours, buffer_before, buffer_after, approval,
         min_duration, max_duration, lead_time, units, exclusive_group?, schedule_version)
Reservation(id, tenant, requester, status, begins_at, ends_at, purpose, expires_at?, series?)
ReservationItem(id, tenant, reservation, resource, units)
Allocation(id, tenant, resource, begins_at, ends_at, kind, blocking, exclusive, units,
           reservation?, work_order?)
Stock(id, tenant, model, space, quantity)
StockMovement(id, tenant, stock, delta, reason, ref_id?, occurred_at)
```

- **엣지**: 시간 행과 문서가 테넌트 행을 가리키는 엣지는 모두 불변이며 payday `agrees`로 같은 테넌트임을 확인한다. 대체할 때 한 번 쓰는 `superseded_by`와 `subject_id`, `ref_id`, `actor_id`는 엣지가 아니라 pdid 열이다. pdid의 도메인 바이트가 무엇을 가리키는지 말해 주므로, 여러 종류를 가리키는 참조에 쓴다. 대상은 도메인 작업이 테넌트 경계를 통해 읽어 확인한다.
- **자산 번호(`tag`)**: 사람이 읽는 번호이며 테넌트 안에서 유일하다. 자유 형식이고 한글도 허용한다. payday `alias`(영문 소문자 슬러그, 63자)는 CLI·설정에서 이름으로 부를 자산에만 선택적으로 쓴다.
- **폐기와 삭제의 구분**: 처분(Dispose)은 업무 상태다. 행이 남으므로 태그도 계속 점유한다. Erase(soft)는 잘못 등록한 자산을 취소할 때만 쓰며, 이때만 태그가 풀린다. 보존 기간 만료 삭제(purge)는 8장에서 다룬다.
- **시리얼**: (모델, 시리얼)이 유일하다. 제조사와 모델이 다르면 같은 시리얼이 있을 수 있다.
- **상태 값을 섞지 않는다**: `status`는 생애주기(`ORDERED`, `ACTIVE`, `IN_REPAIR`, `LOST`, `RETIRED`, `DISPOSED`), `condition`은 물리 상태(`GOOD`, `DAMAGED`, `BROKEN`)다. 가용성은 저장하지 않고 Custody·Allocation·WorkOrder로부터 계산한다.
- **AssetType 변경**: 단일 상속이며, 하위 타입은 속성을 추가할 수만 있고 Capability도 추가만 할 수 있다. 스키마를 바꾸면 `schema_version`이 오른다. 자산의 타입을 바꾸는 것은 `Fact(type)`으로 기록한다.

## 4. 공간 및 자산 예약/대여 설계 (Phase 2)

**예약(미래 사용 권한)과 Custody(실제 인도)는 별도 엔터티다.** 장비 예약은 픽업할 때 Custody를 만들어 이행하고, 공간 예약은 입실 확인(체크인)으로 이행한다. 예약 자체는 체크아웃 상태를 갖지 않는다. 장비의 체크아웃(반출)과 공간의 체크인(입실)은 의미가 다르기 때문이다.

- **Bookable 프로파일**: 시간대, 운영 시간, 휴일, 준비·정리 시간(buffer), 승인 필요 여부, 최소·최대 이용 시간, 사전 예약 기간, 이용 자격, 수량(`units`, 1이면 독점 자원), 배타 그룹. 공간의 면적·정원은 `SpaceProfile`, 장비의 인수·반납 장소는 Bookable 속성이다.
- **예약 상태**: `HELD`(만료가 있는 임시 홀드) → `REQUESTED`(승인 대기) → `CONFIRMED` → `IN_USE` → `COMPLETED`. 종료 상태는 `CANCELLED`, `REJECTED`, `EXPIRED`, `NO_SHOW`다. 승인이 필요 없는 자원은 `REQUESTED`를 건너뛴다.
- **시간대를 막는 상태**: `HELD`, `REQUESTED`, `CONFIRMED`, `IN_USE`의 할당은 `blocking = true`다. 요청 단계에서 시간대를 잡아 두므로 승인 시점에 충돌이 생기지 않는다. 대신 `HELD`와 `REQUESTED`에는 만료 시각과 사용자별 동시 요청 한도를 둔다. 종료 상태로 바뀌면 같은 트랜잭션에서 `blocking = false`로 바꾼다.
- **Allocation 행이 모든 차단을 표현한다**: buffer를 포함한 기간, 키트 구성품마다 하나씩, 상위 공간을 예약하면 배타 그룹에 속한 하위 공간마다 하나씩, `WorkOrder`의 정비 블록, 반복 예약 인스턴스가 모두 Allocation 행이다. 그래서 한 가지 제약으로 모든 충돌을 막는다.
- **충돌 방지는 두 겹이다.**
  1. 도메인 작업이 관련 `Bookable` 행을 id 순서로 갱신(`schedule_version` 증가)해 자원 단위로 직렬화한 뒤, 겹침을 검사하고 할당을 넣는다. 이 갱신은 캘린더 화면이 watch하는 신호도 겸한다.
  2. PostgreSQL에서는 독점 자원에 `EXCLUDE` 제약을 건다(9.4절). 도메인 규칙에 빈틈이 있어도 겹치는 차단 행은 들어가지 않는다.
- **관리자 override**: DB 제약이 있으므로 겹침 허용은 `blocking = false`인 `OVERRIDE` 할당과 사유를 담은 Event로만 표현한다.
- **풀 자원**: 수량형 자원이나 "Canon R5 아무거나 1대" 같은 모델 단위 예약은 `Asset(kind=GROUP)`과 `Bookable.units = N`으로 표현한다. 기간별 사용 합계가 N을 넘지 않는지는 잠금 후 검사한다(EXCLUDE로는 표현할 수 없다). 체크아웃할 때 실물 자산을 지정해 그 자산의 독점 할당으로 바꾼다.
- **키트**: 가용성은 구성품까지 검사한다. 키트 구성(Link)이 바뀌면 그 키트의 미래 예약을 다시 검증하고 충돌을 알린다.
- **반복 예약**: RRULE과 시간대(tzid)를 저장하고 예외 인스턴스를 지원한다. 인스턴스는 정해 둔 범위(예: 6개월)까지 할당 행으로 만든다. 단건 예약부터 구현한다.
- **no-show**: 체크인 마감이 지나면 spin 루프가 자동으로 해제한다. 마감은 워크스페이스가 정한다(`no_show_minutes`, rove#144). 체크인을 받지 않는 워크스페이스도 있고, 비우면 배포의 `app.no_show_after`이다.
- **외부 캘린더(Phase 3)**: Rove에서 예약하는 자원은 Rove가 권위다. 읽기용 ICS 발행부터 시작한다.

## 5. 플러그인 시스템

**선택 구조: `Asset.kind + AssetType(상속된 Capability) + Predicate → View Slot Resolver → Plugin`.** 단일 플러그인이 상세 화면 전체를 대체하지 않고 기본 자산 페이지를 확장한다.

- 슬롯: `asset.detail.main`, `.panel`, `.tab`, `asset.list.column`, `asset.preview`, `asset.action`, `reservation.detail`, `space.calendar`. `asset.list.column`은 행마다 데이터를 요청하면 N+1이 되므로, 목록 전체의 데이터를 한 번에 받는 계약을 둔다.
- 한 Asset에 여러 플러그인이 매칭될 수 있다. 슬롯별 단일/다중 허용, 우선순위, 사용자가 고른 레이아웃은 따로 관리한다.
- **세 종류로 나눈다.**
  1. **뷰 플러그인(Phase 1)**: 빌드할 때 등록하는 React 모듈. Resolver는 클라이언트에서 Asset 행과 타입만으로 판단한다. 관계 존재 같은 조건은 서버 계산이 필요하므로 Phase 2 이후에 추가한다. 예: 랙 U 배치도, 하드웨어 구성도, 가구 치수 뷰, 공간 배치도.
  2. **도메인 모듈(Phase 2–3)**: 별도 proto 패키지 + Go 레이어로, 빌드할 때 함께 컴파일된다(payday의 다중 proto 패키지와 레이어 구조). 모듈의 엔터티는 payday 테넌트 경계·trail·watch를 그대로 받고, 이력이 필요한 데이터는 코어의 시간 행 규약을 따른다. 예: 차량 운행 기록, 랙 전력, RFID 판독 연동. 유지보수·예약·구매는 코어 기능이며 모듈이 아니다.
  3. **서드파티(Phase 3 이후)**: iframe으로 격리한 UI와 공개 API 토큰만 허용한다. 서버에서 실행되는 서드파티 코드는 받지 않는다. 필요해지면 WASM 샌드박스를 다시 평가한다.
- 플러그인 데이터 접근은 Host API와 테넌트 권한을 통과한다. 직접 DB 접근은 허용하지 않는다. 플러그인별 저장량·이벤트·호출량도 계측한다.

## 6. 멀티테넌트·권한·API

- **테넌트 격리는 payday의 테넌트 경계(wall)를 쓴다.** 모든 Rove 엔터티는 `tenant` 엣지로 경계 안에 있다(payday에서는 아무 선언도 하지 않는 것이 이 뜻이다). `global` 엔터티는 두지 않는다. 기본 자산 유형 같은 템플릿은 테넌트를 만들 때 복사한다. 읽기는 쿼리 조건으로 좁혀지고, Add는 payday Gate 레이어가 보이지 않는 테넌트로의 생성을 막는다.
- **엣지의 테넌트 일치**: payday Gate는 Add할 때 모든 엣지가, Patch할 때 바뀌는 엣지가 호출자에게 보이는지를 테넌트 경계를 통해 읽어 확인한다. payday 문서에는 "경로의 첫 홉만 본다"고 되어 있지만, 코드와 테스트는 이렇게 동작한다(확인함). 이 검사가 닿지 않는 곳은 두 군데다. 여러 테넌트를 보는 호출자는 양쪽 행이 다 보이므로 통과하고, Gate 아래에서 하는 쓰기는 검사를 받지 않는다. 그래서 세 겹으로 막는다.
  1. 테넌트 행을 가리키는 불변 엣지는 `agrees`에 선언한다. 비교는 Sink에서 하므로 운영자 스택에도 적용되고, 엣지가 비어 있으면 비교하지 않는다(확인함).
  2. 도메인 레이어를 Gate **위**에 두어, 도메인 작업의 내부 쓰기도 Gate를 지나게 한다(9.3절).
  3. PostgreSQL에서는 바뀌는 참조(`Asset.parent` 같은 현재 상태 값)에 같은 테넌트인지 확인하는 트리거를 마지막 방어선으로 둔다(9.4절). 복합 FK는 payday의 서버 시작 전 검사가 "스키마 불일치"로 거부하므로 쓸 수 없다(확인함).
- **RLS는 쓰지 않는다.** payday의 테넌트 경계는 애플리케이션 쿼리 조건이고, 경계 없는 운영자 스택과 여러 테넌트를 보는 운영자 정책이 RLS의 세션 변수 방식과 맞지 않는다. 대신 위의 세 겹과 교차 테넌트 테스트(10장)로 보완한다.
- **배포를 둘로 나눈다**: 공개 진입점(테넌트 경계 있음, 자기 테넌트만)과 운영자 진입점(내부망, 여러 테넌트를 보는 정책)을 다른 바이너리로 둔다. 공개 바이너리 안에는 "전체 보기" 코드 경로가 없다. 구현(2026-10-12, 결정 17): `rove-admin serve`가 `admin.addr`(기본 루프백)에서 운영자 API(`/admin/...`, 운영자마다 키, 요청마다 운영자 이름으로 로그)와 on-demand TLS 확인을 낸다. `rove-admin`의 나머지 명령은 그 API를 부르는 운영자의 CLI다(워크스페이스 찾기·닫기·복구·주소 바꾸기·소유자 지정, 계약, hold). 공개 바이너리 `rove`는 `server/admin`을 링크하지 않는다. 바뀐 내용은 셸의 운영자 명령처럼 배포 자신의 일로 쓴다. 셸 명령은 아직 `rove`에 남아 있고, 운영자 웹 화면은 나중이다.
- **인증은 roster가 한다(9.10절, D26·D27).** 계정과 그 로그인 수단은 roster의 행이고, Rove는 roster가 확인해 준 계정에 자기 세션을 준다. SaaS는 roster의 OIDC issuer를 통한 SSO만 쓴다(D28). 로그인은 앱 호스트 한 곳에서 계정으로 하고, 워크스페이스는 경로(`/@<alias>`)로 고른다(9.11절). CE의 비밀번호 로그인은 roster에 묻는다(`VouchService.Verify`). SCIM 프로비저닝과 기업 IdP 연결은 roster의 기능을 쓴다.
- **권한**: payday에는 역할이 없으므로 Rove가 `gate.Policy`에서 구현한다. Holder에 `role`(`owner`, `admin`, `asset_manager`, `member`, `auditor`)을 덧붙이고, 메서드별 최소 역할 표로 판단한다. 같은 Policy를 batch guard에도 넘긴다(넘기지 않으면 batch 안의 작업이 권한 검사를 건너뛴다). 일반 직원 역할(`member`)은 자산 보기, 셀프 실사, 요청, 인수 확인을 할 수 있다. 사업장 단위 권한은 Phase 3에서 payday 필드 3(Site)으로 도입한다. 역할은 roster로 옮기지 않고 Rove의 것으로 둔다. roster가 주는 권한을 읽는 방식으로 바꿀지는 정할 것이다(9.10절).
- **역할과 가격 정책을 결합하지 않는다.** 요금제는 한도를 정하고, 권한은 무엇을 할 수 있는지를 정한다.
- **쓰기는 도메인 작업으로만 한다.** payday는 일반 쓰기(Patch/Apply)를 기본으로 닫는다. Rove의 도메인 작업은 두 가지다.
  1. **생성 동사 완성**: 생성 동사가 이미 뜻하는 일이면, 도메인 레이어가 그 동사를 완성한다(payday의 "completing a generated verb"). 자산 `Add`는 초기 배치·Fact·Event를 함께 써서 등록을 마친다. 자산 `Erase`는 열린 시간 행을 대체해 오입력을 취소한다. Custody `Add`는 담당 행을 열어 지급을 마친다.
  2. **새 RPC**: 생성 동사가 이름 붙이지 않는 일만 새로 만든다(`Move`, `Return`, `Correct` 등).
- **`seal` 레이어**(9.3절)는 시간 행·Event·할당처럼 도메인 작업만 쓰는 엔터티의 생성 Add/Erase와, 시간 행의 생성 Get/List를 외부 호출에 닫는다. 이력 조회는 `Timeline`·`QueryAt`·`AsOf`·`Diff`로만 한다. 설정성 엔터티(`AssetType`, `ItemModel`, `Party`, `Bookable` 정책 등)는 생성 Add/Get/List/Erase를 쓰고, 수정은 짧은 도메인 RPC(예: `AssetTypeService/Update`)로 한다.
- **동시성과 멱등성**: 현재 상태 행은 payday 버전 필드(`date_updated`)로 낙관적 동시성을 지킨다. 도메인 RPC는 클라이언트가 미리 발급한 pdid를 작업 ID로 받아 Event ID로 쓴다. 재시도하면 이미 있는 Event를 찾아 같은 결과를 돌려준다.
  - 작업은 상태를 확인하기 **전에** 그 Event부터 찾는다(`Tx.done`). 첫 사본이 바꾼 상태 때문에 재시도가 거부되면 안 되기 때문이다. 다시 온 반납은 닫힌 대여를, 다시 온 예약은 자기 할당을, 다시 온 재고 시작은 자기가 만든 재고를 만난다. 기한이 지났거나 실사가 끝난 뒤에 와도 같다.
  - 작업이 만드는 것(자산, 지급, 재고, 예약, 실사 결과, 전환된 자산)은 작업 ID에서 이름을 얻는다(`made`: 같은 pdid에 도메인 바이트만 바꾸고, 여럿이면 끝 바이트로 구분한다). 그래서 재시도는 첫 사본이 만든 것을 그 이름으로 찾아 돌려준다.
  - 동시에 온 사본은 PostgreSQL에서 작업 ID의 advisory lock(`pg_advisory_xact_lock`)을 기다렸다가 첫 사본의 Event를 본다(`Tx.claim`). 트리 잠금 밖의 작업(재고, 실사 스캔, 예약)도 그래서 한 번만 일어난다. SQLite는 쓰기를 하나씩 하므로 기다릴 것이 없다.
  - 재고 수량은 그 행을 잠근 뒤에 읽는다(`Tx.moveStock`). 같은 재고를 동시에 바꾸는 서로 다른 작업은 차례로 반영되고, 각자 앞의 작업이 남긴 수량에서 계산한다.
- **트랜잭션**: 도메인 RPC 하나가 트랜잭션 하나다. payday trail·outbox가 같은 트랜잭션에 기록되므로 데이터와 감사 기록은 함께 성립하거나 함께 취소된다.
- **연동(Phase 3)**: Webhook은 payday Outbox에서 `Event` 추가만 골라 전달한다. 조직 디렉터리·SSO·SCIM은 roster가 맡고(9.10절), 회계·ERP·HR, 캘린더, 자동 수집 에이전트를 확장 경계로 둔다.

## 7. SaaS 과금 준비: 계량은 Rove 밖에서, 원천에서

**원칙: 청구할 숫자는 Rove 밖의 작업이 원천에서 계산한다.** 원천은 Rove의 데이터베이스(읽기 복제본)와 첨부 파일을 담은 저장소의 목록(inventory)이다. Prometheus·OTel 같은 모니터링 메트릭은 청구 기준이 아니다. 빠지거나 겹쳐도 알 수 없고, 다시 계산할 수 없으며, 테넌트 라벨은 카디널리티 문제를 만든다. Rove는 계량을 위한 행을 따로 쓰지 않는다. 원천에서 다시 계산할 수 있는 값을 Rove 안에 한 벌 더 두면 원천과 어긋날 수 있는 사본이 하나 늘 뿐이다. 그래서 하루 한 번 쓰던 `UsageSnapshot`은 지웠다(D35. 도메인 번호 20은 비워 둔다).

### 7.1 단계별로 만드는 것

```text
Rove DB ──(읽기 복제본)──┐
                         ├──> 계량 작업 ──> 사용량 원장 ──> 집계 ──> 청구 어댑터
첨부 저장소의 목록 ──────┘   └──────────────── Rove 밖 ────────────────┘
```

- **계량 작업**은 정해진 주기로 원천을 읽고, 읽은 시각과 함께 테넌트·미터별 값을 자기 원장에 쓴다. 원장은 불변이고 멱등이다(같은 시각·테넌트·미터는 한 행). 쿼리가 바뀌면 계산 버전을 올리고, 지난 값은 그때의 버전으로 남긴다.
- **요금제·Entitlement·한도·청구**도 Rove 밖의 것이다. 요금제는 한도를 정하고 권한은 무엇을 할 수 있는지를 정한다는 6장의 원칙은 그대로다.
- **원천에서 다시 계산할 수 있다**: 개수와 저장량의 원천은 행의 생성·삭제 시각(`date_created`, `date_erased`)과 첨부의 크기(`size_bytes`)다. 그래서 Rove 안에 원장이 없어도 된다. 다만 보존 작업과 워크스페이스 파기가 원천을 지우므로, 지워지기 전에 잰다(7.3절).

### 7.2 미터와 원천 쿼리

| Meter | 단위 | 원천 |
| --- | --- | --- |
| `active_assets` (kind별) | 개 | `asset`. 공간·키트·그룹을 따로 센다 |
| `members` / `admin_seats` | 명 | `holder`(구성원 자격) |
| `stock_skus` / `stock_quantity` | SKU / 개 | `stock`. 잰 그때의 값만 있다 |
| `bookable_resources` / `reservations` | 개 / 건 | `bookable`, `reservation` |
| `history_rows` / `history_bytes` | 행 / bytes | 시간 행(`placement`, `link`, `stewardship`, `fact`)과 `event`의 수와 논리 크기. payday trail은 서비스 비용이므로 제외 |
| `attachment_bytes` | bytes | `attachment.size_bytes`, 또는 저장소의 목록 |
| `attachment_gb_month` | GB-month | `attachment`의 생성·삭제 시각, 또는 저장소 목록의 일 단위 표본(7.4절) |

원천이 없는 것은 미터로 두지 않는다. 기간 안에 무언가 한 사람(활성 사용자)은 쓰기라면 `event.actor_id`로 셀 수 있지만 읽기만 한 사람은 남지 않으므로, 사람은 좌석(`members`)으로 센다. API 호출 수도 데이터베이스에 없다. 접속기록(`access`, 9.7절)은 개인정보를 담은 호출만 남기는 기록이라 호출 수가 아니고, HTTP 로그는 모니터링이다. 호출로 과금하게 되면 그때 원천(게이트웨이의 요청 기록 같은)을 함께 정한다. Webhook과 플러그인은 그 기능이 생길 때 원천을 정한다.

아래 쿼리는 PostgreSQL 읽기 복제본에 대한 것이다. `:at`은 재는 시각, `:from`·`:to`는 청구 기간 `[from, to)`이며, 워크스페이스는 `tenant_id`로 나눈다. 복제 지연만큼 늦은 값이므로 원장에는 읽은 시각을 함께 남긴다.

**어느 시각의 개수.** soft erase하는 엔터티는 지워도 행이 남고 `date_erased`가 찍히므로, 행이 남아 있는 동안은 지난 시각의 개수도 다시 셀 수 있다.

```sql
-- active_assets: kind(item, space, kit, group)별 자산 수
SELECT tenant_id, kind, count(*) AS assets
FROM asset
WHERE date_created <= :at AND (date_erased IS NULL OR date_erased > :at)
GROUP BY tenant_id, kind;

-- members / admin_seats
SELECT tenant_id,
       count(*) AS members,
       count(*) FILTER (WHERE role IN ('owner', 'admin')) AS admin_seats
FROM holder
WHERE date_created <= :at AND (date_erased IS NULL OR date_erased > :at)
GROUP BY tenant_id;
```

자산의 Erase는 오입력 취소이므로 빠진다. 처분·분실한 자산(`status`가 `disposed`, `lost` 등)을 셀지는 요금제가 정한다(11장 1). 구성원 자격을 끝내면(비활성화, 탈퇴) `date_erased`가 찍힌다.

**지금 값만 있는 것.** 아래는 지난 시각으로 다시 셀 수 없다. 재고 수량은 이동(`stockmovement`)으로 바뀌는데 이동은 보존 작업이 지우고, 예약 자원의 `enabled`는 바뀐 이력이 행에 없다. 잰 그때의 값이 원장에 남는 값이다.

```sql
-- stock_skus / stock_quantity
SELECT tenant_id, count(*) AS skus, sum(quantity) AS quantity
FROM stock
WHERE date_erased IS NULL
GROUP BY tenant_id;

-- bookable_resources
SELECT tenant_id, count(*) AS bookables
FROM bookable
WHERE enabled AND date_erased IS NULL
GROUP BY tenant_id;
```

**기간 안에 생긴 것.**

```sql
-- reservations: 기간 안에 만든 예약
SELECT tenant_id, count(*) AS reservations
FROM reservation
WHERE date_created >= :from AND date_created < :to
GROUP BY tenant_id;
```

**이력의 규모.** `pg_column_size(t.*)`는 행 하나의 크기다(압축된 TOAST 값을 넣고, 인덱스와 페이지 오버헤드는 뺀다). 이력은 바이트가 아니라 보존 기간 구간으로 과금하는 것을 권한다(7.4절).

```sql
-- history_rows / history_bytes
SELECT tenant_id, 'placement' AS kind, count(*) AS rows, sum(pg_column_size(t.*)) AS bytes FROM placement t GROUP BY tenant_id
UNION ALL SELECT tenant_id, 'link', count(*), sum(pg_column_size(t.*)) FROM link t GROUP BY tenant_id
UNION ALL SELECT tenant_id, 'stewardship', count(*), sum(pg_column_size(t.*)) FROM stewardship t GROUP BY tenant_id
UNION ALL SELECT tenant_id, 'fact', count(*), sum(pg_column_size(t.*)) FROM fact t GROUP BY tenant_id
UNION ALL SELECT tenant_id, 'event', count(*), sum(pg_column_size(t.*)) FROM event t GROUP BY tenant_id;
```

**첨부.** 행으로 재면 고객에게 보이는 첨부의 양이다.

```sql
-- attachment_bytes: 그 시각에 보이는 첨부
SELECT tenant_id, count(*) AS files, sum(size_bytes) AS bytes
FROM attachment
WHERE date_created <= :at AND (date_erased IS NULL OR date_erased > :at)
GROUP BY tenant_id;

-- attachment_gb_month: 기간 안에 각 첨부가 보인 시간 × 크기
SELECT tenant_id,
       sum(size_bytes::numeric
           * extract(epoch FROM least(coalesce(date_erased, :to), :to) - greatest(date_created, :from)))
         / extract(epoch FROM :to::timestamptz - :from::timestamptz) / 1e9 AS gb_month
FROM attachment
WHERE date_created < :to AND (date_erased IS NULL OR date_erased > :from)
GROUP BY tenant_id;
```

byte·초는 `numeric`으로 곱한다. bigint로는 31일 달 기준 테넌트당 약 3.4 TB·월에서 넘친다.

첨부를 지워도(`AttachmentService.Erase`, soft) 파일은 저장소에 남는다. 파일은 보존 작업이 그 첨부가 달린 문서와 함께 지우거나, 워크스페이스가 파기될 때 지운다. 그래서 고객에게 보이는 양(위의 쿼리)과 저장소가 실제로 가진 양이 다르고, 어느 쪽으로 청구할지는 계약이 정한다(11장 4). 저장소가 가진 양은 저장소의 목록에서 잰다. 파일의 키는 `<워크스페이스 id>/<YYYY>/<MM>/<첨부 id>`이므로 키의 첫 마디로 묶는다.

- 로컬 디스크(`app.files`): `du -sb <app.files>/*/`. 디렉터리 이름이 워크스페이스 id다.
- S3 호환 저장소(D25): 하루 한 번 나오는 S3 Inventory 보고서의 `Key`·`Size`를 키의 첫 마디로 묶어 더한다.

목록으로 잰 GB-month는 하루 한 번의 표본을 하루치로 친다: `Σ(그날의 bytes) × 86400 / 그 달의 초 / 10^9`. 행이 지워진 뒤에도 그날 잰 값이 원장에 남으므로, 보존 작업과 파기가 지운 파일까지 들어간다. 하루보다 짧게 있다 지워진 파일은 빠질 수 있다.

### 7.3 원천이 지워지기 전에 잰다

미터는 그 원천이 지워지기 전에 잰다. Rove가 원천을 지우는 곳은 둘이다.

1. **보존 작업**(8.3절, `rove retention run`. `serve`가 `app.retention.every`마다 돌리고, `app.retention.apply`가 켜졌을 때만 지운다): 보존 창이 시작되기 전에 끝난 시간 행, 끝난 문서(예약·Custody·실사·작업)와 거기 달린 첨부의 행과 파일, 재고 이동, 남은 행이 가리키지 않는 Event.
2. **워크스페이스 파기**(8.4절, `server/closing`): 복구 기간이 지난 닫힌 워크스페이스의 모든 행과 파일.

그래서 다음을 지킨다.

- 계량 작업은 보존 작업보다 자주, 적어도 하루 한 번 돌고 잰 값을 원장에 남긴다. 한 달의 값은 월말에 원천을 다시 읽어서가 아니라 원장에 쌓인 값으로 계산한다. 보존 작업이 지운 행은 다시 읽을 수 없다.
- 닫힌 워크스페이스도 파기될 때까지는 행이 남으므로 같은 주기로 잰다. 마지막 기간의 값은 파기 전에 확정한다. 복구 기간이 그 여유다.
- 보존 기간을 줄이거나 보존 작업의 주기를 바꿀 때는 계량 작업이 그보다 먼저 재는지 확인한다(8.3절의 "해당 기간 계량 확정").

### 7.4 GB-month 계산 정의

- 예시 단위: **GB-month** = 한 달 동안 보유한 데이터의 시간가중 평균 GB. `GB`(10^9 bytes)인지 `GiB`(2^30 bytes)인지를 계약에 명시한다.
- 적분형: `usage = ∫ stored_bytes(t) dt / seconds_in_billing_month / 10^9`(해당 달의 시간 길이 기준). 예: 한 달 내내 10 GB이면 10 GB-month, 한 달의 절반 동안만 10 GB이면 약 5 GB-month.
- 행의 생성·삭제 시각으로 정확한 byte·초를 계산하거나, 저장소 목록의 일 단위 표본으로 근사한다(7.2절).
- **과금 데이터 크기 정의를 먼저 고정한다**: 첨부 원본, 미리보기·파생물, 이력, DB 레코드, 인덱스, 복제본, 백업 중 무엇을 포함하는가? 권장은 `원본 첨부 + 별도로 공지한 플러그인 데이터`를 바이트로 세고, 이력은 바이트가 아니라 **보존 기간 구간**으로 과금하는 것이다. 고객은 이벤트 하나가 몇 바이트인지 알 수 없어 이력 바이트를 예측하지 못한다. 물리 복제·DB 오버헤드는 서비스 비용으로 처리한다.
- 무료 저장량, 초과분 GB-month, 프로레이션, 계측 지연·정정, 월 경계와 테넌트 시간대(청구는 UTC 등)를 계약으로 정의한다.
- 고객에게 보이는 사용량(현재 데이터량, 월 예상 초과량, 보존 구간별 이력 규모)은 청구 쪽이 원장에서 보여 준다. 고객이 보는 값과 청구값은 한 원장에서 나온다(10장 과금 도입 게이트의 (2)).

## 8. 보존 기간·삭제 정책

**모델(소유자의 결정, D36)**: 과금은 사용량으로 한다(7장). 워크스페이스는 자기 기록을 얼마나 보관할지 스스로 정한다. S3의 수명 주기처럼, 정하지 않으면 영원히 보관하고, 정한 기간이 지난 것은 지운다. 보관하는 것은 모두 보인다.

- **과금하지 않았다는 이유만으로 약관 고지 없이 소급 삭제하지 않는다.** 지우는 일은 배포가 켤 때까지 꺼져 있다(`app.retention.apply`).
- 장기 내보내기, 감사·규제 보존, `AsOf` 조회, 고급 검색·복원 정책은 운영자와의 계약으로 다룰 수 있다.
- 저장량 초과는 즉시 차단보다 알림·유예·관리자 승인을 우선한다.

### 8.1 보관하는 것은 보인다

정할 것은 둘이다.

1. **보존 기간**: 서비스가 이력을 얼마나 오래 저장하는가. 워크스페이스의 설정이다.
2. **삭제 정책**: 기간이 지난 뒤 실제로 지우는 일정, 예외(법적 보존), 복원과 백업 제거 방식. 배포와 운영자의 것이다.

처음에는 셋째로 **조회 창**을 두었다. 이력이 DB에 있어도 정해진 기간 밖은 보여 주지 않는 것이었다. 지금은 없앴다(D36). 보관하는데 보이지 않는 이력은 고객이 볼 수 없는 것에 값을 치르게 하고, 이력을 읽는 모든 곳(타임라인, 시점 조회·비교, 리포트, 끝난 문서, 감사 기록)에 창을 지키는 코드를 둬야 했다. 이제 보관하는 만큼 보인다.

**구현**(`server/retention`, `server/domain/keep.go`):

- **워크스페이스의 설정이다**(`Tenant.keep_days`, `access_keep_days`, `restore_days`). 소유자가 `TenantService.Configure`로 정하고, 그 일은 작업 기록(`workspace.configure`)에 남는다. 생성된 `Patch`는 이 필드를 거절한다. 설정 화면은 설정 › 워크스페이스 › 기록 보관이다.
  - 이력: 0은 영원히다.
  - 접속기록: 0은 배포의 `audit.by.access`이고, 배포의 바닥값(`audit.by.access.min`)보다 짧은 값은 거절한다.
  - 복구 기간: 0은 배포의 `app.workspaces.restore_within`이다.
  - 어느 것도 100년(36500일)을 넘지 않는다.
- **줄인 기간은 유예가 지난 뒤에 적용한다.** 이력과 접속기록의 기간을 줄이면, 그 전 값과 줄인 때를 함께 저장한다(`keep_days_before`, `date_keep_shortened` 등). 배포의 유예(`app.retention.grace`, 기본 30일)가 지날 때까지는 더 긴 그 전 기간을 지키므로, 그 안에 되돌리면 아무것도 지워지지 않는다. 유예 중에 다시 줄이면 아직 지켜야 할 가장 긴 기간을 마지막 변경의 유예까지 지킨다. 늘리면 바로 적용한다.
- **저장하기 전에 무엇이 언제 지워지는지 말한다**(`TenantService.Preview`, 소유자). 말하는 것은 세 가지다. 바꾼 기간이 언제부터 적용되는지(지금, 또는 유예가 끝나는 날), 그때 보존 작업이 지울 이력의 종류별 건수(지금 있는 이력으로 셈), 접속기록이 언제부터 얼마 동안 보관되는지다. 건수는 보존 작업이 지울 때 쓰는 술어로 **세기만** 한다(`Tx.takes`). 지웠다가 되돌리는 방식(`rove retention plan`)은 되돌릴 때까지 행의 잠금을 쥐는데, Preview는 소유자가 몇 번이든 부르는 읽기이기 때문이다. Event는 남는 행이 가리키지 않는 것을 센다. 지울 행만 가리키는 Event도 지워지므로 그것까지 센다.
- 계약(`TenantContract`)에는 운영자의 몫만 남는다. 이름·설명·효력일, 그리고 파기할 때 trail을 모두 지울지(`purge_floors`, 8.4절)다. 그 전 계약이 은퇴라면 purge는 배포의 유예가 지난 뒤에 적용한다. 그 밖의 보존 항목(보기·보관·유예·접속기록·복구)은 계약에서 지웠다. 마이그레이션이 효력 중인 계약의 값을 워크스페이스로 한 번 옮긴다(`cli.Migrate`).
- 시간 행(Placement·Link·Stewardship·Fact)은 여전히 생성된 Get·List·Watch로 직접 읽지 않고, 이력 조회(`Timeline`·`QueryAt`·`Diff`)로 읽는다. 이력은 그것을 쓰는 계층이, 사람의 이름과 함께 말한다.

### 8.2 구현상의 핵심 안전장치

- **시간 행 자체가 체크포인트다.** 보존 기준일 전에 끝난 기간 행과, 기준일 전에 대체된 행을 지운다. 기준일에 걸친 행은 남는다. Fact는 (자산, 키)마다 기준일 이전의 마지막 값을 남긴다. 상태를 이벤트 재생으로 만들지 않으므로 "관계 시작 이벤트만 지워져 해석이 꼬이는" 문제가 생기지 않는다. Event는 기준일 전 것을 지우되, 남는 시간 행이 가리키는 Event는 함께 남긴다.
- **보존 TTL의 대상이 아닌 것**: 현재 상태 엔터티(Asset, Party, 현재 유효한 시간 행), 진행 중인 Custody·예약, 끝나지 않은 실사·분쟁 기록, 취득·처분 기록(Purchase와 처분 Fact), 법적 보존 대상.
- **payday trail과 맞추기**: payday trail은 모든 쓰기의 값 스냅샷을 담는다. payday가 테넌트별 보존을 지원하므로(payday#35) 다음처럼 나눈다.
  1. 제품 이력은 Rove의 시간 행과 Event가 테넌트별 정책으로 보존한다.
  2. trail에서 이력을 담는 도메인(Asset, Placement, Link, Stewardship, Fact, Event)은 **그 워크스페이스가 정한 보존 기간**을 따른다. payday가 조직마다 묻고, Rove가 워크스페이스의 설정과 `LegalHold`로 답한다(`server/retention`). 그래서 제품에서 지운 이력이 trail에 값으로 남지 않고, 법적 보존은 둘을 함께 붙든다.
  3. 계정·권한 도메인(Holder)의 trail은 payday `pipa` 프로필(최소 1년) 이상으로 보존한다. 접속기록(9.7절)은 trail의 한 종류(`access`)로 보존한다. 기간은 워크스페이스가 정한다(`access_keep_days`, rove#144, D36). 1년·2년을 가르는 기준(정보주체 수, 고유식별정보·민감정보 처리)이 조직마다 다르기 때문이다. 워크스페이스가 말하지 않으면 배포의 `pipa-sensitive`(2년)이고, 배포의 바닥값 `pipa`(1년, `audit.by.access.min`)보다 짧게는 정할 수 없다. 로그인과 비밀번호의 기록은 roster의 trail에 있다(9.10절).
  4. trail 용량이 문제가 되면, 그 자체로 기록 시각을 가진 불변 기록인 시간 행·Event의 쓰기를 recorder에서 빼는 것을 검토한다(payday의 "Changing what the trail records").
- **개인정보**: 이력과 Event에는 Party·Holder ID만 남긴다. Event의 문장은 사람을 이름 대신 표시로 쓰고(`{party}` 사람 기록, `{holder}` 구성원 자격을 행위자처럼, `{alias}` 구성원 자격의 아이디), 그 ID를 payload에 둔다(`party=`, `holder=`). 읽을 때(`EventService.Recent`, `AssetService.Timeline`) 지금의 이름으로 채우므로, 이름을 바꾼 사람은 바뀐 이름으로, 잊힌 사람은 가명으로 읽힌다. 이름을 채운 읽기는 그 사람의 정보를 보여 준 것이므로 접속기록에 남는다(9.7절, `access.Saw`). 물건(태그, 유형, 모델, 예약·작업의 이름)은 그때의 이름을 쓴다. 개인정보가 아니고, 그때 무엇이라 불렸는지가 기록이다. 이렇게 하기 전(2026-10-12)에 쓴 Event는 쓴 그대로 남는다. 문장에 이름이 있고 누구에 대한 것인지 ID가 없어, 안전하게 가려낼 방법이 없다(이름은 같을 수도, 그 뒤에 바뀌었을 수도 있다). 테넌트 내보내기는 저장된 그대로(표시와 ID)를 담는다. 삭제 요청은 다음 순서로 처리한다. Party를 가명화하고, 그 Party와 Holder를 대상으로 한 trail 행의 `value`·`patch`를 DB와 아카이브에서 함께 비운다(payday `trail.Policy.Forget`, 가명화와 같은 트랜잭션). 사람의 이름·설명·라벨·연락처·사번, 초대한 주소, 알림의 글, 구성원 자격의 이름과 아이디는 personal로 선언되어 trail에 애초에 값으로 남지 않는다(payday#92). trail은 그 필드를 바꿨다는 것과 누가 언제 바꿨는지만 기록한다. 그래서 비우는 것은 그 밖의 것, 곧 자유 텍스트와 이 선언 전에 쓴 행이다. 법적 보존이 걸린 행은 그대로 두고 그 건수를 Event에 남기며, 보존이 풀린 뒤 다시 요청하면 마저 비운다. Holder는 soft erase하여 로그인을 막고, trail의 행위자 ID는 그대로 둔다.
- **분할**: 시간 행과 Event는 테넌트와 시각 기준으로 지울 수 있게 인덱스를 둔다. 테넌트마다 보존 기간이 다르므로 파티션 통째 삭제는 가장 긴 보존 기간에만 쓸 수 있고, 나머지는 배치 삭제다.
- 만료 예정 데이터 관리자 대시보드와 사전 알림, 고객 내보내기(CSV/JSON/첨부 패키지) 경로와 유예 기간을 제공한다.
- 보존 기간을 줄이면 배포의 유예(`app.retention.grace`)가 지난 뒤에 적용하고, 저장하기 전에 무엇이 언제 지워지는지 말한다(8.1절). 결제가 한 번 실패했다고 바로 삭제하지 않는다.
- **데이터 분류**: 시간 행과 Event에 분류(업무, 재무, 인사, 보안)를 두어 분류별로 보존 규칙을 다르게 적용한다.
- **실제 삭제 방식**: 시간 행은 watch하지 않는다. 클라이언트가 `Timeline` 응답을 복제본에 담더라도 보관 기간(기본 7일)이 지나면 사라지고, 보존 삭제 대상은 보존 기간 밖의 오래된 행이라 최근에 내려받았을 수 없다. 그래서 보존 잡이 운영자 스택에서 배치 DELETE로 지울 수 있다. watch되는 현재 상태 엔터티는 이 방식으로 지우지 않는다(payday는 앱을 거치지 않은 삭제를 클라이언트가 알 수 없다고 경고한다). 검색 인덱스·캐시도 함께 지운다. 백업과 복제본에서 특정 레코드를 즉시 지울 수는 없으므로 백업 만료 주기와 지연을 계약에 명시한다.
- `legal_hold`, `retention_exception`, `deletion_job`, `deletion_receipt`와 감사 추적을 마련한다. 삭제 영수증에는 내용 데이터를 남기지 않는다.
- **테넌트 탈퇴**: payday에서 Tenant는 행이 남아 있으면 지울 수 없다(FK). 탈퇴는 내보내기 → 유예 → 테넌트의 모든 행 삭제 → Tenant 삭제 순서로 한다. 구현(`server/offboard`, `rove tenant export|purge`): 내보내기는 zip 하나에 종류별 JSON 줄(API가 답하는 모양), 테넌트가 읽을 수 있는 trail(DB와 아카이브), 첨부 파일을 담고, 뺀 것(비밀번호 해시·세션·hold 등)과 이유를 manifest에 적는다. 삭제는 스키마의 FK에서 순서를 읽어(새 엔터티도 자동으로 포함) 한 트랜잭션으로 지우고 Tenant를 마지막에 지운다. trail은 payday `PurgeTenant`가 앞뒤로 한 번씩 지운다. hold가 걸린 테넌트는 거부한다. dry run은 같은 삭제를 하고 되돌린다.
- 개인정보 보호와 현지 법률의 보존·삭제 의무는 출시 국가별로 법무 검토가 필요하다.

### 8.3 삭제 작업 단계

```text
계약/정책 평가 → 보존 만료 후보 식별 → 법적·운영상 예외 검사
 → 관리자 사전 알림/내보내기/유예 → 해당 기간 계량 확정(Rove 밖, 7.3절)
 → 기준일에 걸친 행과 남길 Fact 확인 → 기록 삭제/비식별화
 → trail·검색·캐시 반영 → 백업 만료 대기/삭제 증빙 기록
```

삭제 작업은 멱등이고 재시도할 수 있어야 한다. 먼저 `dry-run`으로 영향받는 행 수·용량·복원 가능 범위를 보여 주는 기능을 구현한다.

**구현**(`server/domain/expire.go`, `rove retention plan|run`): 조직마다 한 트랜잭션으로, 보존 창이 시작되기 전에 끝난 것을 지운다. 끝났거나 대체된 시간 행, 같은 키의 다음 값이 창 시작 전에(그리고 그때까지 기록되어) 시작된 Fact, 끝난 문서(마친·취소된 예약과 그 항목·할당, 다 돌려받은 Custody와 그 줄, 닫힌 실사와 그 발견, 끝난 작업과 그 할당), 재고 이동, 그리고 남은 행이 아무것도 가리키지 않는 Event 순서다. 폐기·불용·분실 상태 Fact와 등록(`asset.add`) Event는 취득·처분 기록이라 남는다. hold가 있거나 보존이 무기한이면 아무것도 지우지 않는다. dry run은 같은 삭제를 트랜잭션 안에서 하고 되돌리므로 실제 실행과 행 수까지 같다. 영수증은 `retention.expired` Event이고 종류별 건수와 기준 시각만 담는다. 파괴는 `app.retention.apply`가 켜졌을 때만 일어나며, `serve`가 `app.retention.every`마다 돌린다. trail의 이력 종류는 payday의 pass가 같은 보존 창으로 지운다.

### 8.4 워크스페이스 닫기 (D29)

```text
닫기 → 복구 기간(기본 30일) → 파기 → alias 보류(기본 1년) → alias 풀림
          ↑ 되돌리기           ↑ legal hold가 있으면 풀릴 때까지 기다린다
```

1. **닫기**: 소유자가 직접 해지하거나, 운영자가 닫는다(미납, 약관 위반). 그 워크스페이스로의 접근(세션, API 토큰)을 바로 막고 데이터는 그대로 둔다. 닫힌 동안 소유자는 내보내기만 할 수 있다.
2. **복구**: 복구 기간 안에는 그대로 되돌린다.
3. **파기**: 복구 기간이 지나면 워크스페이스의 모든 행과 파일을 지운다(8.2절의 테넌트 탈퇴). trail은 파기하는 순간 효력이 있는 계약을 따른다(`TenantContract.purge_floors`, payday#115). 기본은 은퇴(`rove tenant retire`)로, 배포의 바닥값(`audit.min`, `audit.by.<종류>.min`)이 걸린 trail은 그 바닥값이 지날 때까지 남긴다. 바닥값은 배포가 법 때문에 지는 의무(개인정보 보호법의 접속기록 1년 등)라서 조직이 떠나도 같이 가져가지 않는다. 계약이 말하면 전부 지운다(`rove tenant purge`). 은퇴는 남긴 것과 떠났다는 서명된 기록을 보관소에 두므로, 바닥값은 있는데 보관소가 없는 배포에서는 파기하지 않고 닫힌 상태로 두고 운영자에게 알린다. legal hold가 걸려 있으면 풀릴 때까지 파기하지 않고 닫힌 상태로 둔다.
4. **법정 보존**: 워크스페이스의 데이터를 통째로 남기는 것이 아니다. 법이 정한 항목만 분리해 그 기간 동안 보관한다. 대개 우리 쪽의 계약·청구 기록이고, 워크스페이스 안의 데이터가 아니다. 항목과 기간은 법률 검토로 정한다.
5. **alias**: 파기할 때 풀고, 보류 기간 동안 다른 워크스페이스가 쓰지 못하게 한다. 예전 주소로 찾아오는 사람(북마크, 메일 속 링크)을 노린 사칭을 막기 위해서다. 복구 기간 안의 되돌리기는 alias를 그대로 되찾는다. 운영자는 의도적으로 일찍 풀 수 있고, 그 사실은 기록에 남는다. 라벨은 alias를 쓰지 않으므로(9.9절) 보류와 상관없다.

복구 기간은 기본값이다. 배포 설정(`app.workspaces.restore_within`)으로 기본값을 바꾸고, 워크스페이스가 자기 것을 정한다(`Tenant.restore_days`, rove#144, D36). 보존 기간과 같은 방식이다(8.1절). alias 보류 기간은 배포의 것이다. 주소는 배포 전체가 나눠 쓰는 이름이기 때문이다.

**계정이 탈퇴할 때**: 그 계정이 속한 모든 워크스페이스에서 Holder를 끝내고 Party를 가명화한다(8.2절). 마지막 소유자가 탈퇴하면 남은 사람 중 역할이 가장 높은 사람(같으면 먼저 들어온 사람)에게 넘어간다(9.10절). 개인 워크스페이스처럼 그 계정만 있던 워크스페이스는 구성원이 아무도 남지 않으므로 저절로 닫히고, 위의 닫기 절차를 밟는다(닫은 쪽: `deployment`). 감사자만 남으면 운영자에게 알린다.

**만든 것**(2026-10-12): 닫기·복구는 `TenantService.Close`·`Restore`(소유자, 닫을 때 이름을 입력), 닫힌 워크스페이스는 호출을 받는 입구(`cmd.Resolver`)에서 막고 소유자의 조회·복구만 받는다. 파기는 `server/closing`의 sweep이 하고, 법적 보존이 있으면 닫힌 채로 기다린다. 주소 보류는 `AliasHold`이다. 닫힌 동안 소유자는 `GET /account/workspaces/{alias}/export`로 내보내기를 받는다.

## 9. 구현 아키텍처

### 9.1 구현 기반: payday

[payday](https://github.com/lesomnus/payday)는 proto에 엔터티를 선언하면 ent 스키마, CRUD 서버, 테넌트 경계, 페이지 목록·watch, 감사 trail, TypeScript 클라이언트를 생성하는 gRPC 프레임워크다. Rove는 다음을 payday에서 받는다.

| payday가 주는 것 | Rove에서의 쓰임 |
| --- | --- |
| proto → ent·CRUD·TS 생성 | 모든 엔터티 선언. 생성된 Get/List/Watch |
| 테넌트 경계(선언하지 않으면 경계 안) | 모든 Rove 엔터티. `global` 없음 |
| Gate의 엣지 확인(Add는 모든 엣지, Patch는 바뀌는 엣지)과 `agrees` | 다른 테넌트 행을 가리키는 참조 거부(6장) |
| 일반 쓰기(Patch/Apply)의 기본 닫힘 | 도메인 RPC만 상태를 바꾼다는 원칙과 같다 |
| trail (쓰기와 같은 트랜잭션, 값 스냅샷, 도메인별 보존·아카이브·`Forget`, `pipa` 프로필) | 보안·시스템 감사. 제품 이력과는 보존 정책을 분리(8.2절) |
| Outbox · Watch | 현재 상태 화면 갱신, Webhook(Phase 3) |
| pdid (UUIDv8 + 도메인 바이트, 클라이언트에서도 발급) | 멱등 키, batch에서 미리 정한 ID, 오프라인 큐, 여러 종류를 가리키는 참조 열 |
| `auth`(세션·Bearer), `gate.Policy`, batch guard | 세션과 역할 권한. 사람이 누구인지는 roster가 답한다(9.10절) |
| `spin` | 홀드 만료, no-show, 연체 알림, 알림 메일 발송, 테넌트 도메인 확인, 보존 잡 |
| 두 진입점(경계 있는 스택 / 운영자 스택) | 공개 바이너리와 운영자 바이너리 |

**payday에 없어서 Rove가 직접 만드는 것**

- 엔터티별로 생성 쓰기를 닫는 `seal` 레이어. payday 설정의 닫힘 목록은 일반 쓰기(Patch/Apply)만 다룬다. 레이어로 막아야 batch 안의 호출까지 막힌다.
- 여러 행을 쓰는 도메인 RPC의 트랜잭션 헬퍼(`dialect.BeginTx` + `enttx.Rebind` 패턴을 하나로 묶는다).
- 범위 조건 질의·검색·그래프 탐색 RPC. 생성 List는 등식 필터만, Watch는 행을 지정하는 방식만 지원한다.
- 기간 제약(`EXCLUDE`), 테넌트 일치 트리거, 검색 인덱스 같은 PostgreSQL 전용 DDL과, 이것을 ent 마이그레이션과 나눠 적용하는 절차(9.4절).
- 테넌트별 보존 정책, DB에 있는 trail의 개인정보 삭제.
- 역할 권한(`gate.Policy`). 여러 테넌트에 걸친 로그인 매핑과 IdP 연결은 roster가 갖는다(9.10절).
- 오프라인 쓰기 큐. payday 클라이언트는 읽기 복제본(메모리 + IndexedDB 미러)이며 쓰기 큐를 갖지 않는다.

### 9.2 스택과 배포

- **백엔드**: Go 1.27 + payday. 전송은 gRPC와 Connect/gRPC-Web(payday `web`).
- **DB**: 운영은 PostgreSQL 18이다. 공식 이미지를 그대로 쓰고(btree_gist는 기본 포함), 확장을 더 넣지 않는다. 관리형으로 옮길 때의 선택지는 9.5절 표에 있다. SQLite는 빠른 단위 테스트와 브라우저 데모(payday sandbox)에만 쓴다. 규칙은 도메인 레이어에 있으므로 두 DB에서 같게 동작하고, PostgreSQL 제약은 마지막 방어선이다. SQLite는 타입·NULL 정렬·잠금이 관대해서 PostgreSQL에서만 드러나는 버그가 있으므로, 시간 행·예약·동시성 테스트는 PostgreSQL에서 돌린다.
- **첨부 저장소**: 첨부는 저장소 인터페이스 뒤에 둔다.
  - 서버 한 대에서는 로컬 파일시스템에 저장하고, 앱이 서명한 짧은 만료의 다운로드 URL로 내준다.
  - 서버를 나눌 때 S3 호환 저장소(SeaweedFS, Garage 등)로 옮긴다.
  - MinIO 커뮤니티판은 2025년 10월 바이너리·이미지 배포를 멈췄고 2026년 4월 저장소가 보관 처리되어 쓰지 않는다.
  - 업로드 후 해시와 악성 파일 검사를 거친 뒤 `Attachment`를 확정한다.
- **프런트엔드**: React + `@lesomnus/payday`(store, query, watch). 모바일은 PWA이고 카메라로 QR을 스캔한다.
- **비동기**: payday spin 루프와 Outbox 드레인. 별도 메시지 브로커는 필요해질 때 채택한다.
- **관측**: OpenTelemetry(payday `otx`). 수집기와 대시보드도 같은 서버에서 직접 운영한다.
- **배포(self-host)**: 클라우드 없이 서버 한 대에 Docker Compose로 올린다.
  - 구성: 리버스 프록시(TLS, ACME), 공개 진입점, 운영자 진입점(내부망·VPN에서만 접근), PostgreSQL, 백업. 이미지는 하나이고 진입점이 둘이다.
  - watch broker는 단일 복제본이므로 `memory`로 시작하고, 복제본을 늘릴 때 PostgreSQL broker로 바꾼다.
  - roster는 이 구성에서 Rove 프로세스 안에 내장한다(9.10절). 여러 제품이 한 roster를 쓰는 배포에서는 roster(와 그 앞의 Ory Hydra)를 따로 띄우고 Rove가 외부로 붙는다.
- **백업**: PostgreSQL은 WAL 보관 기반 PITR(pgBackRest 등)로, 첨부 파일은 파일 백업으로 다른 장소에 보낸다. 복구 리허설은 분기마다 한다.
- **Rove 기본 도메인**: 앱 호스트, OIDC 리다이렉트 주소, 테넌트 라벨 도메인의 CNAME 대상, 기본 하위 도메인이 모두 이 도메인 아래에 있다. 그래서 한 번 정하면 바꾸지 않는다(9.9절).

### 9.3 서버 레이어

```text
grpc.Server
  └ 인터셉터: auth(roster가 확인한 세션 → frame) → 테넌트별 제한·호출 계측 → gate.Policy(역할) → watch 발행
    └ seal   (Rove) 시간 행·Event·할당·문서 줄의 생성 Add/Erase, 시간 행의 생성 Get/List를 외부 호출에 닫음
      └ domain (Rove) 도메인 RPC: 트랜잭션, 잠금, 시간 행 규칙, 충돌·순환 검사, Event 기록
        └ Gate     (payday 생성) Add·Patch가 가리키는 행이 호출자에게 보이는지 확인
          └ Audit  (payday 생성) trail 자체의 RPC
            └ Sink (payday 생성) 테넌트 경계 조건, ID 발급(minter), agrees 비교, trail 기록
```

- `domain` 레이어는 생성 쓰기를 `seal` 아래(`Next()`)에서 호출하므로 막히지 않는다. 외부 호출과 batch 안의 호출은 `seal`을 지나므로 막힌다.
- **`domain`을 Gate 위에 두는 이유**: payday 테스트 앱은 자기 레이어를 Gate 아래에 둔다. Rove는 도메인 작업의 내부 쓰기까지 Gate의 엣지 확인을 받게 하려고 위에 둔다. 대가는 쓰기마다 엣지 하나당 읽기 하나가 늘어나는 것이고, Rove의 쓰기량에서는 감당할 수 있다.
- **PostgreSQL에서 확인한 것**: 생성 쓰기는 직접 호출과 batch 안의 호출 모두 `seal`이 거부하고, batch 오류는 몇 번째 작업인지 알려 준다. 두 행을 쓰는 도메인 작업은 함께 커밋되고, 실패하면 trail까지 함께 취소된다. batch 안에서는 도메인 작업의 트랜잭션이 바깥 트랜잭션에 합류하므로, 뒤 작업이 실패하면 앞의 도메인 작업도 취소된다. Gate 위의 도메인 작업이 다른 테넌트로 Add하려 하면 NotFound가 된다.
- **도메인 RPC의 순서**: 트랜잭션 시작 → 잠금 행 갱신(배치 트리는 테넌트 잠금 행, 예약은 `Bookable` 행, 담당은 Asset 행, 여러 개면 id 순서) → 대상 읽기와 검사 → 기존 시간 행 대체 + 새 시간 행 추가 → Asset 현재 상태 갱신 → Event 추가 → 커밋. 모든 쓰기의 trail·outbox 행이 같은 트랜잭션에 들어간다. batch 안에서 호출되면 바깥 트랜잭션에 합류한다.
- **생성 동사를 완성할 때**: 본 행은 아래 서버(`next`)로, 추가 행은 이 레이어를 다시 묶은 것(`at`)으로 쓴다. 그래야 이 레이어의 규칙이 스스로 쓰는 행에도 적용된다. 본 행을 `at`으로 쓰면 자기 자신을 다시 호출하게 된다(payday 서버 가이드).
- 모든 Rove 레이어는 `WithDriver`를 구현한다. 빠뜨리면 트랜잭션을 열 때 그 레이어가 빠진 스택이 만들어진다. `pd doctor`와 컴파일 시점 확인(`enttx.Binder`)을 둘 다 쓴다.

### 9.4 PostgreSQL 전용 DDL과 마이그레이션

payday 마이그레이션 엔진(ent/atlas)과 PostgreSQL 18.6으로 확인한 결과다(2026-10-10, [plan.md](plan.md) 1장).

| ent 스키마 밖의 객체 | 서버 시작 전 검사(`entschema.Check`) | ent 디렉터리에 함께 둘 때 다음 Plan |
| --- | --- | --- |
| `EXCLUDE` 제약 | 통과 | 그 제약을 `DROP`하려 한다 |
| `UNIQUE` 제약, 부분 유일 인덱스 | 통과 | 그 객체를 `DROP`하려 한다 |
| 복합 FK | **거부**(스키마 불일치, 서버가 뜨지 않음) | `DROP`하려 한다 |
| 트리거와 함수 | 통과 | 확인하지 않음(별도 디렉터리에 두면 Plan과 무관) |

- **마이그레이션 디렉터리를 둘로 나눈다**: `migrations/ent`(payday/ent의 Plan으로 생성하고 리뷰 후 커밋)와 `migrations/pg-extra`(직접 작성: `btree_gist` 확장, `EXCLUDE`, 부분 유일 인덱스, 테넌트 일치 트리거). 적용은 ent 다음에 extra 순서이고, 같은 엔진(`entschema.Migrations.Apply`)으로 적용한다.
- **나누는 이유**: Plan은 이미 있는 파일을 dev DB에 재생한 뒤 ent 스키마와 비교하고, 인덱스·제약 삭제도 계획한다. 그래서 같은 디렉터리에 두면 위 표처럼 Plan이 매번 extra 객체를 지우려 한다. 따로 두면 Plan은 ent 디렉터리만 재생하므로 extra 객체를 모른다.
- **복합 FK 대신 트리거**: ent 비교는 FK 삭제를 걸러내지 않으므로, DB에 ent가 모르는 FK가 있으면 서버 시작 전 검사가 실패한다. 그래서 같은 테넌트 확인은 `BEFORE INSERT OR UPDATE` 트리거로 하고, 위반은 FK와 같은 SQLSTATE `23503`으로 알린다.
- **두 디렉터리는 하나의 버전 흐름이다**: 둘이 같은 리비전 테이블(`schema_revisions`)을 쓴다. 그래서 어느 쪽이든 마지막으로 적용된 버전보다 오래된 파일은 "out of order"로 거부된다. 브랜치를 합칠 때 오래된 파일이 생기면 새 시각으로 다시 만든다.
- ent 마이그레이션이 extra 객체가 기대는 열이나 테이블을 바꾸면 적용이 실패한다. 그런 변경은 같은 PR에서 extra도 함께 고친다.

```sql
-- migrations/pg-extra (열 이름은 생성 스키마를 따른다)
CREATE EXTENSION IF NOT EXISTS btree_gist;

-- 자식마다 한 시점에 물리 부모 하나 (대체되지 않은 행끼리)
ALTER TABLE placement ADD CONSTRAINT placement_one_parent EXCLUDE USING gist
  (child_id WITH =, tstzrange(valid_from, valid_to) WITH &&)
  WHERE (superseded_at IS NULL);

-- 독점 자원의 차단 할당은 겹치지 않는다 (반열린 구간이라 맞닿는 것은 허용)
ALTER TABLE allocation ADD CONSTRAINT allocation_exclusive EXCLUDE USING gist
  (resource_id WITH =, tstzrange(begins_at, ends_at) WITH &&)
  WHERE (blocking AND exclusive);

-- 바뀌는 참조도 같은 테넌트 안에서만 (복합 FK 대신)
CREATE FUNCTION rove_asset_parent_same_tenant() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.parent_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM asset p WHERE p.id = NEW.parent_id AND p.tenant_id = NEW.tenant_id
  ) THEN
    RAISE EXCEPTION 'asset.parent is in another tenant' USING ERRCODE = 'foreign_key_violation';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER asset_parent_same_tenant BEFORE INSERT OR UPDATE OF parent_id, tenant_id ON asset
  FOR EACH ROW EXECUTE FUNCTION rove_asset_parent_same_tenant();
```

같은 방식으로 슬롯 점유(`parent, slot`), 랙 U 범위(`int4range(u_from, u_to, '[]')`), 담당 역할(`asset, role`)의 겹침을 막고, Fact에는 `(asset, key, valid_from) WHERE superseded_at IS NULL` 부분 유일 인덱스를 둔다. 트리거는 `custodian`, `type`, `model` 같은 다른 바뀌는 참조에도 둔다.

지금은 모두 `cli/pg.sql`에 있다(마이그레이션마다 다시 실행해도 되는 문장들). `EXCLUDE`는 물리 부모 하나(`placement_one_parent`), 슬롯(`placement_slot_once`, 빈 슬롯 제외), 랙 U 범위(`placement_rack_once`, `u_from > 0`), 담당 역할(`stewardship_one_per_role`), 관계(`link_once`), 독점 할당(`allocation_exclusive_no_overlap`)이고 트랜잭션 끝까지 미룬다. Fact의 부분 유일 인덱스(`fact_once`)는 미룰 수 없으므로 즉시 검사한다. 도메인 레이어가 같은 시점의 값을 먼저 대체한 뒤 새 값을 쓰기 때문에 그 사이에 걸리는 일이 없다. 도메인 레이어를 거치지 않은 SQL이 이것들에 거부되는지는 `TestTheTreeHoldsUnderTheDomainLayer`(할당은 `TestPostgresBackstop`)가 확인한다.

### 9.5 검색

- **검색은 OpenSearch와 한국어 분석기 nori가 답한다(D31, rove#83).** 클러스터는 `app.search.opensearch`로 준다(`server/opensearch`).
  - 색인은 DB의 사본이다. 서버가 처음 뜰 때 없거나 모양(`Version`)이 다르면 루프가 DB로 새로 만들고, 별칭을 한 번에 옮긴다. 손으로는 `rove search reindex`다.
  - 바뀐 것은 쓰기와 같은 트랜잭션에 `SearchStale`로 남고, 한 프로세스의 루프가 그 문서를 DB의 지금 상태로 다시 만든다. 문서는 만든 행들의 최신 시각을 버전으로 가져서, 늦게 도착한 옛 읽기가 새 문서를 덮지 못한다.
  - 호출이 저장한 자산은 그 호출이 답하기 전에 색인에 들어간다(`Indexer.Interceptor`). 저장하고 바로 검색해도 보인다.
  - 글자를 입력한 `Search`는 엔진에 묻고, 받은 ID를 벽을 거쳐 그 순서로 읽는다. 엔진은 누가 무엇을 볼 수 있는지 묻지 않는다.
  - 엔진이 답하지 않으면 Unavailable이고, 데이터베이스로 넘어가지 않는다. 엔진이 답하지 못할 때 큰 표를 LIKE로 훑는 것이 더 나쁘다.
  - 한 낱말은 nori가 나눈 조각을 모두 갖춘 것을 찾고, 여러 낱말 중에서는 어느 것이든 맞으면 찾는다. 모든 낱말이 맞은 것이 먼저이고, 그다음은 드문 낱말이 맞은 것이다.
  - **색인은 자산, 사람, 모델로 셋이다**(rove#91). 별칭도 모양(`Version`)도 따로다. 사람을 찾는 것과 물건을 찾는 것은 묻는 것이 다르고, 점수는 한 색인 안의 낱말 수로 매기므로 사람 이름과 물건 이름이 섞이면 서로의 낱말을 흔하게 만든다. `PartyService.Search`와 `ItemModelService.Search`가 각자의 색인에 묻는다.
    - 사람의 문서는 이름, 코드, 속한 팀(조직) 이름이다. 이름은 두 글자 조각으로도 색인한다. nori는 사람 이름을 한 낱말로 두고 앞부분 일치로는 이름의 뒤쪽을 못 찾는데, 사람은 이름만으로도 찾기 때문이다("민수"로 김민수). 이메일과 전화번호는 넣지 않는다.
    - 모델의 문서는 이름, 제조사, 코드, 유형 이름이다.
    - 이름을 빌려 오는 문서도 함께 다시 만든다. 팀 이름이 바뀌면 그 팀 사람의 문서를, 유형 이름이 바뀌면 그 유형 모델의 문서를 다시 만든다.
- **클러스터가 없으면 데이터베이스가 답한다**(`internal/search`). 개발용 서버와 검색 엔진까지 둘 필요가 없는 작은 배포다.
  - **찾는 대상**: 자산의 이름·태그·시리얼, 그리고 자산을 거쳐 유형 이름, 모델 이름·제조사, 보유자 이름이다. 질의는 열마다 id 집합을 구해 `UNION`으로 합친다. 사람은 이름과 코드를, 모델은 이름, 제조사, 코드를 찾는다.
  - **인덱스는 없다.** 그 워크스페이스의 행을 모두 읽는다. 빠르게 하려고 더 손대지 않는다. pg_bigm과 pg_trgm으로 GIN 인덱스를 두던 것(D22)은 걷어 냈고, 마이그레이션이 그 인덱스를 지운다.
  - 1글자는 태그·이름의 접두 일치이고, 2글자부터는 포함이다. 1글자가 모든 이름의 가운데에서 걸리는 것은 아무도 뜻하지 않은 검색이다.
  - **접어서 비교한다**: 저장된 값과 입력을 모두 `lower(normalize(x, NFKC))`로 접는다(서버는 `internal/search.Fold`). 그래서 전각 `ＡＢＣ`는 `abc`로, 반각 `ｶﾒﾗ`는 `カメラ`로 찾는다. 히라가나↔가타카나, 간체↔번체는 정규형이 아니라 사전의 일이라 하지 않는다. LIKE의 `%`·`_`는 입력 그대로의 글자로 찾는다. SQLite에는 정규형이 없어 입력만 접는다. 검색 엔진에도 같은 방식으로 접어서 넣는다.
- **관리형 DB로 옮길 때**: 필요한 확장은 `btree_gist` 하나다(EXCLUDE 제약, 9.4절). 각 서비스의 공식 문서 기준이며, 2026-10에 확인했다.

| 서비스 | btree_gist | 옮길 때 |
| --- | --- | --- |
| AWS RDS for PostgreSQL 18 | 1.8 | 그대로 옮길 수 있다 |
| Google Cloud SQL | 1.8 (PG 18) | 그대로 옮길 수 있다 |
| Azure Database for PostgreSQL | 1.8 (PG 18) | 그대로 옮길 수 있다 |
| 네이버 클라우드 Cloud DB for PostgreSQL | 사용자 설치 | 설치하면 옮길 수 있다 |
| NHN Cloud RDS for PostgreSQL (14, 17) | 목록에 없음 | DB 방어선을 쓸 수 없다 |

- DB 로케일은 UTF-8이어야 한다.
- 사용자 정의 속성은 JSON(jsonb)으로 저장하고, 생성 List가 아닌 `Search` RPC에서 GIN 인덱스로 거른다. 단순 태그는 payday `labels`(키·값 등식 필터)를 쓴다. SQLite에서는 `LIKE`로 대신한다.

### 9.6 그래프와 성능

- 1~수 단계 탐색은 재귀 CTE와 `(child, superseded_at, valid_from)` 인덱스로 처리한다. 현재 상태는 Asset의 비정규화 열(`parent`, `custodian`)로 생성 List를 쓴다.
- 하위 트리 전체 조회가 잦아지면 현재 상태용 경로(ltree 또는 closure 테이블)를 추가한다. 자유로운 대규모 경로 분석이 필요해지면 그래프 엔진을 다시 평가한다.
- **화면 갱신 신호**: payday watch는 행을 지정해야 하므로 "이 방에 있는 자산"처럼 조건으로 정한 집합을 직접 watch할 수 없다. 내용이 바뀌는 컨테이너(공간·키트의 `content_version`), 예약 자원(`Bookable`의 `schedule_version`), 실사(`InventoryCount`의 `findings_version`)의 행 버전을 같은 트랜잭션에서 올리고, 화면은 그 행을 watch하다가 바뀌면 목록을 다시 읽는다(`ts/src/rpc.tsx`의 `<Live>`). 이 갱신은 잠금도 겸한다. 이동이 아주 잦은 창고 공간에서는 이 행이 경합 지점이 될 수 있으므로 계측한다.
- 테넌트 잠금 행은 배치 트리 변경을 테넌트 단위로 직렬화한다. 사람이 하는 이동량에서는 충분하고, RFID처럼 자동 이동이 많아지면 사업장 단위 잠금으로 나눈다.
- **목록 인덱스는 테넌트로 시작한다**(#122). 벽이 모든 읽기를 그 워크스페이스로 좁히므로, 목록이 걷는 인덱스는 `(tenant, date_created, id)`다.
  - 만든 때로 시작하는 인덱스는 행이 가장 많은 워크스페이스에서만 빠르고, 다른 워크스페이스는 남의 행을 지나며 읽는다. 스테이징에서 작은 워크스페이스의 최신순 자산 목록은 138 ms였고, 행 10만 1천 개를 버렸다.
  - payday의 생성 검사가 이것을 요구하고, `pd gen`이 경고를 보여 준다.
  - 인덱스의 열은 선언한 순서다. 간선을 먼저 선언하면 간선의 열이 먼저 온다. 전에는 생성기가 간선의 열을 늘 뒤에 두었다.
  - 인덱스는 선언한 이름으로 저장된다. 앞선 빌드가 열 이름으로 저장한 인덱스는 마이그레이션 전에 선언한 이름으로 옮긴다(`rename.Indexes`, payday#132). 옛 인덱스를 손으로 지우던 목록(`cli.Replaced`)은 없앴다.

### 9.7 보안

- UI를 포함한 모든 조회·내보내기에 테넌트와 역할 검증을 적용하고, 예약 승인·관리 권한을 분리한다.
- 공개·운영자 진입점을 다른 바이너리로 나눈다(6장). 운영자 진입점은 VPN 같은 내부망에서만 닿게 한다.
- 자체 서버 운영: OS·컨테이너 보안 업데이트를 자동화하고, 방화벽은 80/443만 연다. 디스크와 백업은 암호화하고, 비밀값은 저장소에 넣지 않는다.
- 첨부는 앱이 서명한 짧은 만료의 다운로드 URL로만 주고, 업로드 후 악성 파일 검사를 한다.
- **접속기록(rove#136, payday#111)**: 개인정보를 읽거나 쓰거나 내려받은 호출마다 payday의 `Access`에 한 행을 남긴다. trail은 쓰기만, 접속지 없이, 바뀐 행을 대상으로 기록하므로 이 기록이 될 수 없다.
  - 한 행은 누가(구성원 자격), 어디서(`app.trusted_proxies`를 지난 주소), 어떤 RPC로, 누구의 개인정보를(행의 ID 최대 1,000개와 그 수), 무엇을 했는지(읽기·쓰기·내려받기), 결과(거부 포함)를 담는다.
  - 대상은 `personal` 선언에서 읽는다. 그래서 새 RPC도 따로 적지 않아도 기록된다. 선언이 못 보는 것, 곧 Event 문장에 채운 이름과 CSV의 열은 `access.Saw`로 알린다.
  - 쓰기는 그 트랜잭션에서, 내려받기는 답하기 전에 남긴다. 남기지 못한 내려받기는 거부한다. 읽기는 1초씩 모아 남긴다.
  - 내려받기(`AssetService.Export`, 워크스페이스 내보내기)는 사유를 적어야 하고(`payday-reason-bin`), 사유도 함께 남는다. 아무의 정보도 담지 않은 파일(대량 등록 양식, `AssetService.Form`)은 내려받기가 아니다.
  - 보존은 trail의 장치로 한다(`audit.by.access`, 배포 설정은 `pipa-sensitive` 2년). 워크스페이스 내보내기에 그 워크스페이스의 접속기록이 함께 담긴다.
  - 관리자와 감사자가 읽고 점검한다. `AccessService.Review`가 한 기간(최대 92일)을 새것부터 읽어 준다. 행마다 접속지와 내려받은 이유를 풀고, 누가와 누구의 정보를 지금의 이름으로 붙인다(그 이름을 보여 준 것도 접속기록에 남는다). 구성원·정보주체·종류로 좁힌다. 정보주체는 행의 목록이라 DB가 아니라 읽으면서 고른다.
  - 점검했다는 기록은 `AccessReview`다. 누가, 언제, 어느 기간을, 그때 몇 건(전체·내려받기·거부)이었는지와 점검 결과를 남기고, 고치거나 지우지 않는다. 화면은 설정 › 접속기록에서 한 달씩 보여 준다.
  - 운영자는 `rove access ls|reviews`로 배포 전체를 본다. 이름 없이 ID만 보여 준다.
- 저장 데이터와 전송 구간을 암호화하고, 백업·복구를 정기적으로 테스트하고, 플러그인을 격리한다.
- 공용 기기(현장 태블릿)에서는 클라이언트 복제본(IndexedDB)의 보관 기간을 줄이고 로그아웃할 때 지운다.

### 9.8 API 경계 예시

payday가 생성한 서비스와, 거기에 덧붙이는 도메인 RPC다(예: `AssetService/Move`). "(완성)"은 도메인 레이어가 생성 동사를 완성한 것이다(6장). 생성된 Get/List/Watch는 현재 상태 엔터티에서만 연다.

```text
Assets:       Add(완성: 등록) / AddMany(완성: 대량 등록) / Get / List / Watch / Erase(완성: 오입력 취소) / Search / SetAttributes / ChangeType / Dispose
Placement:    Move / Install / Remove / Correct
Stewardship:  Assign / Unassign / Correct
History:      Timeline / QueryAt / AsOf / Diff / Export
Labels:       Print / Bind / Unbind / Resolve
Custody:      Add(완성: 지급) / Get / List / Watch / Acknowledge / Return / Extend
Domains:      Add / Get / List / Verify / Activate / Retire
Reservations: Availability / Hold / Request / Approve / Reject / Cancel / CheckIn / Override
Inventory:    Receive / Move / Consume / Adjust / ConvertToAssets
Counts:       Start / Scan / Reconcile / Close
WorkOrders:   Open / Schedule / Complete
Retention:    GetPolicy / PreviewExpiry / Export / Hold / ApplyPolicy
```

### 9.9 테넌트 도메인과 QR 라벨

**라벨에는 전역 코드를 넣고, 호스트는 워크스페이스의 활성 도메인이다(D28, rove#155).** QR에는 `https://<호스트>/l/<코드>`를 넣는다. 호스트는 그 워크스페이스에 활성 도메인이 있으면 그 도메인, 없으면 서비스 주소(`app.public_url`)다. 코드는 배포 전체에서 유일한 짧은 무작위 문자열이다(UUID는 36자라 작은 라벨에서 QR이 빽빽해진다). 해석은 로그인한 뒤 앱이 그 코드의 워크스페이스로 보내는 것이고, 그 워크스페이스의 구성원이 아니면 NotFound다. 그래서 alias를 바꾸거나 워크스페이스가 닫혀도 라벨은 깨지지 않고, 다른 워크스페이스로 가지도 않는다. **라벨은 도메인이 없어도 언제나 켜져 있다.** 예전에는 `app.labels.tenant_domains`를 켠 배포(Dedicated, D23)에서 도메인이 없으면 라벨이 꺼졌는데, rove#155가 스위치와 그 조건을 함께 없앴다.

- **코드**: Crockford base32(`0-9A-Z`에서 `I L O U`를 뺀 32자) 8자, 무작위다. 32⁸ ≈ 1.1조라 배포 전체에서 겹칠 일이 드물고, 겹치면 다시 뽑으며 DB의 유일 인덱스(`label.code`)가 최후의 방어선이다. 대문자라 QR의 영숫자 모드에 들어가 작게 인쇄된다. 사람이 읽을 수 있게 라벨 아래에 `7K3M-Q9TX`로 넷씩 끊어 인쇄하고, 입력은 대소문자·`-`·공백을 무시하고 `I`·`L`은 `1`, `O`는 `0`으로 읽는다. 코드 이전에 인쇄한 라벨(라벨 ID의 base32 26자)은 그대로 해석한다.
- **다른 워크스페이스의 라벨**: 스캔은 지금 연 워크스페이스에서 먼저 해석한다(`LabelService.Resolve`, 벽을 통해). NotFound면 앱이 `GET /account/labels/{code}`에 그 코드가 계정의 어느 워크스페이스에 있는지 묻는다. 계정이 구성원인 워크스페이스면 그 alias를, 아니면 없는 코드와 똑같이 404를 답한다. 그래서 어느 코드가 남의 것인지는 알 수 없다. 화면은 그 워크스페이스의 `/scan/<코드>`로 넘어가고, 터미널은 `rove scan`이 같은 순서로 묻는다.
- **라벨 URL**: 어느 호스트로 인쇄했든 `/l/<코드>`는 스캔 화면(`/scan/<코드>`)으로 넘기기만 한다. 그 화면은 라벨을 인쇄한 호스트의 워크스페이스가 있는 곳이다. 그 워크스페이스의 앱 도메인이면 그 도메인, 아무것도 고정하지 않는 그 워크스페이스의 도메인(라벨 도메인, 이전 주소)이면 그 워크스페이스의 앱 도메인(없으면 서비스 주소), 서비스 주소면 서비스 주소다. 코드가 어느 워크스페이스의 것인지는 보지 않는다. 그래서 라벨 URL은 자산에 대해서도, 인쇄된 호스트 너머의 워크스페이스에 대해서도 아무것도 드러내지 않는다.

**도메인**(`TenantDomain`): 어느 워크스페이스든 설정 › 도메인과 라벨에서 둘 수 있다. 무엇을 위한 것인지(`purpose`)는 고르는 것이 아니라 종류가 정한다.

1. **자체 도메인**(예: `assets.acme.co.kr`)은 `app`이다. 그 워크스페이스의 앱 전체가 그 도메인에 있게 되고(rove#155의 다음 PR들), 라벨도 그 도메인으로 인쇄한다. 워크스페이스가 서비스의 대상 호스트(`app.labels.target`)를 가리키는 CNAME과 확인용 TXT 레코드(`_rove-challenge.<도메인>`)를 만든다.
2. **기본 하위 도메인**(예: `acme.l.<기본 도메인>`, `app.labels.suffix`)은 `label`이다. DNS 작업이 필요 없고 라벨만 쓴다. 대신 Rove가 기본 도메인을 영구히 유지해야 한다. 새 워크스페이스에 저절로 주지 않는다. 라벨이 도메인을 기다리지 않으므로 줄 이유가 없다.
3. 예전(D23)의 자체 도메인은 `label`로 남는다. 라벨만 쓰고, 그 주소로 인쇄한 라벨은 계속 열린다.

- **호스트**: 소문자, ASCII(IDNA), 끝의 점 없이 저장한다. IP 주소, `localhost` 아래 이름, 서비스 자신의 호스트(`app.public_url`, `app.app_url`)와 그 아래 이름, 기본 도메인 아래 이름(기본 하위 도메인으로 추가한다)은 받지 않는다. 호스트는 배포 전체에서 유일하다.
- **확인**: spin 루프가 TXT 레코드와 그 도메인이 이 서비스를 가리키는지(CNAME이 `app.labels.target`이거나, CNAME을 펼친 apex처럼 target과 같은 주소를 가짐)를 주기적으로 조회하고, 둘 다 맞으면 `verified_at`을 찍는다(payday `stamped`). 확인하기 전에는 TLS 인증서도 발급하지 않는다.
- **활성**: 워크스페이스마다 활성 도메인은 하나다(목적과 상관없이). 새로 활성화하면 이전 것은 `LEGACY`가 되어 계속 해석되고, 새로 인쇄하는 라벨만 새 도메인을 쓴다. 이전 것이 앱 도메인이면 `app.workspaces.domain_redirect`(기본 90일) 동안 그 페이지(GET, HEAD)를 지금 앱이 있는 곳의 같은 페이지로 보낸다(`redirect_until`). 호출은 보내지 않는다. 라벨은 인쇄할 때 쓴 도메인을 기록한다. 도메인을 중지하려 하면 그 도메인으로 인쇄한 라벨 수를 보여 주고 확인을 받는다.
- **재확인과 중지**(rove#155): 활성 자체 도메인은 한 시간마다 다시 확인한다. TXT 레코드가 없거나 이 서비스를 가리키지 않는다는 답이 여섯 번 연속, 다섯 시간 넘게 이어지면 `lapsed`가 된다.
  - 답이 없는 것(시간 초과, DNS 서버 장애)은 세지 않고, 한 번이라도 맞으면 처음부터 다시 센다.
  - 여섯 번인 이유: DNS 호스트가 레코드와 없는 이름에 주는 기본 TTL은 한 시간에서 몇 시간이다. 이전하다 레코드가 잠깐 빠지거나 없는 이름이 잠깐 캐시돼도 대개 TTL 하나면 돌아오고, 다섯 시간은 그보다 길다. 그러면서도 DNS가 다른 곳으로 옮겨 간 도메인은 그날 안에 로그인을 넘겨받지 못하게 된다.
  - `lapsed`가 되면 핸드오프가 그 도메인으로 코드를 보내지 않고, `/internal/tls-ask`가 인증서를 거절하고, 그 워크스페이스에 묶인 세션이 끝나고, 링크는 서비스 주소로 돌아간다. 그 주소로 인쇄한 라벨은 DNS가 이곳을 가리키는 동안 계속 열린다. 이벤트(`domain.lapse`)를 남기고, 소유자와 관리자에게 알림(`domain.lapsed`, 메일로도)을 보낸다.
  - 다시 쓰려면 TXT 레코드가 다시 확인돼야 한다. `lapsed`는 바로 활성화할 수 없다. 스윕이 일주일 동안 몇 분마다 다시 확인하고, 확인되면 다른 활성 도메인이 없을 때 다시 활성이 된다. 사람은 언제든 설정에서 확인할 수 있다.
  - 이전 주소(`LEGACY`)는 다시 확인하지 않는다. 옮기면서 레코드를 지워도 그 라벨과 페이지 보내기는 그대로다.
- **TLS**: 리버스 프록시가 첫 요청이 올 때 인증서를 받는다(ACME on-demand TLS). 받기 전에 운영자 진입점의 내부 엔드포인트(`/internal/tls-ask`)에 "확인된 테넌트 도메인인가"를 묻고, 아니면 받지 않는다. 그래서 아무 도메인이나 이 서버를 가리켜 인증서를 받아 갈 수 없다.
- **호스트가 정하는 것**(rove#155, `cmd.Hosts`): HTTP 리스너 앞에서 요청의 호스트를 본다.
  - **활성 앱 도메인**이면 그 워크스페이스로 고정한다(`server/apphost`). 호출은 그 워크스페이스의 구성원으로만 받는다. `rove-workspace`가 다른 워크스페이스를 말하거나, 다른 워크스페이스의 `/@alias` 화면, 다른 워크스페이스의 파일을 부르면 다시 가리키지 않고 거부한다. 로그인과 로그아웃은 그 도메인에서 하지 않고 핸드오프로 한다(아래). 워크스페이스 만들기, 계정이 워크스페이스를 넘나드는 호출(초대함), 터미널 로그인은 서비스 주소에서만 한다. `/account`는 그 워크스페이스 하나만 보여 준다.
  - **서비스 주소**(`app.public_url`, `app.app_url`의 호스트)는 지금과 같다. 경로와 헤더로 모든 워크스페이스를 고른다. `app.public_url`이 없는 체크아웃에서는 워크스페이스 도메인이 아닌 모든 호스트가 서비스 주소다.
  - **활성 앱 도메인이 아닌 워크스페이스 도메인**(라벨 도메인, 기본 하위 도메인, 이전 주소가 된 앱 도메인, 확인했지만 쓰지 않는 도메인)은 아무 워크스페이스에도 고정하지 않는다. 그 주소로 인쇄한 라벨(`/l/<코드>`)만 연다.
  - **그 밖의 호스트**는 `/healthz`(프로브)만 답하고 나머지는 거부한다.
- **로그인 핸드오프**(rove#155, `server/session`): 앱 도메인에서는 아무도 로그인하지 않는다. 서비스 주소가 로그인을 확인하고 그 도메인에 세션을 넘긴다.
  1. 세션이 없으면 화면이 `/handoff/start`로 보낸다(`GET /sso`가 그 도메인에서는 `handoff`라고 답한다). 도메인은 state를 만들어 그 호스트만의 짧은 쿠키(`__Host-rove_handoff`, 10분)에 두고, 서비스 주소의 `GET /handoff?to=<호스트>&state=`로 보낸다.
  2. 서비스는 `to`가 활성 앱 도메인일 때만 코드를 만든다. 아니면 거부하고 어디로도 보내지 않으며, 코드는 요청에 적힌 이름이 아니라 저장된 도메인으로 보낸다. 브라우저가 서비스에 로그인하지 않았거나 계정이 약관에 동의하지 않았으면 화면이 로그인·동의를 받은 뒤 돌아온다(`/?next=/handoff?...`). `rove login --allow`로 좁힌 세션과 그 워크스페이스의 구성원이 아닌 계정에는 코드를 주지 않는다. 코드는 32바이트 무작위 값이고 DB에는 SHA-256만 남는다(`Handoff`, 트레일 밖). 1분 동안 한 번, 그 계정·워크스페이스·호스트·state에만 쓰인다.
  3. 도메인의 콜백(`/handoff/callback`)은 코드를 먼저 소비한다. 동시에 두 요청이 같은 코드를 가져와도 하나만 로그인되고, 거부되는 코드도 그 자리에서 쓰인 것이 된다. 그다음 state를 쿠키와 대조하고, 코드의 호스트·워크스페이스·state·시간과 구성원 자격을 확인한 뒤 세션을 발급한다. 실패는 이유를 가리지 않고 `/?signin=failed`로 보낸다. 로그에는 이유만 남고 코드는 남지 않는다.
  - **도메인의 세션**은 그 호스트 전용 `__Host-` 쿠키(Secure, HttpOnly)이고 그 워크스페이스에 묶인다(`Session.workspace`, payday가 frame의 테넌트와 대조한다). 다른 워크스페이스를 부르면 거부하고, 서비스 주소나 다른 도메인에서는 아무도 아니다. 계정 자신의 일(약관 동의, 워크스페이스 만들기, 초대함, 터미널 승인)은 하지 못한다.
  - 그 세션은 넘겨받은 서비스 주소의 세션(`Session.parent`)이 살아 있는 동안만 산다. 그 세션의 수명을 넘지 않고, 쓰이는 동안 그 세션이 유휴로 끝나지 않게 붙잡는다. 서비스 주소에서 로그아웃하면 함께 끝나고, 도메인에서 로그아웃하면(`POST /handoff/logout`) 두 세션을 함께 끝낸 뒤 서비스 주소의 로그아웃이 보낼 곳(SSO로 들어왔으면 발급자의 세션 종료, 아니면 서비스 첫 화면)으로 보낸다. roster의 "모든 곳에서 로그아웃"은 계정의 세션을 모두 끝내므로 둘 다 끝난다.
  - 도메인의 DNS를 쥔 쪽은 그 도메인으로 가는 코드를 받을 수 있다. 그래서 코드와 세션을 그 워크스페이스에 묶는다. 그렇게 받은 것은 그 워크스페이스 안의 그 계정에서 더 나아가지 못한다.
- **링크**(rove#155, `apphost.Links`): 이 배포가 만드는 링크의 호스트는 한 곳이 정하고, 다른 코드는 호스트를 직접 만들지 않는다.
  - 워크스페이스로 가는 링크(알림 메일의 링크, 새로 인쇄하는 라벨, 첨부의 서명된 내려받기 주소)는 그 워크스페이스의 활성 앱 도메인으로 만든다. 핸드오프가 코드를 보내는 콜백도 같은 곳에서 만든다.
  - 앱 도메인이 없거나 활성이 아니면(확인만 됐거나, 이전 주소가 됐거나, 은퇴했으면) 서비스 주소로 만든다. 페이지는 `app.app_url`, 라벨과 파일은 `app.public_url`이다. 활성 라벨 도메인은 라벨에만 쓴다.
  - 어느 한 워크스페이스의 것이 아닌 링크는 언제나 서비스 주소다. 초대 메일의 링크도 그렇다. 계정은 아직 그 워크스페이스의 구성원이 아니고, 초대함에서 자기 자신으로 답하기 때문이다. 로그인과 터미널도 그렇다.
  - 내보내기 링크는 화면의 상대 주소라 화면이 있는 호스트(그 도메인이든 서비스 주소든)에서 열린다.

### 9.10 신원: roster

사람이 누구인지, 어느 테넌트에 속하는지, 어떻게 로그인하는지는 [roster](https://github.com/lesomnus/roster)가 정한다(D26). roster는 사람을 갖는 payday 앱이다. 비밀번호를 잠금·2단계 인증과 함께 확인하고, 외부 IdP(Entra, Google)의 계정을 사람에 잇고, 디렉터리의 SCIM 프로비저닝을 받고, Ory Hydra를 앞에 두고 OIDC issuer가 된다. Rove는 이것을 다시 만들지 않는다. 같은 구조를 shale이 먼저 쓰고 있다(shale `internal/identity`, `docs/10-security.md` 33.1절).

**두 층의 테넌트(D27)**: roster의 테넌트와 Rove의 테넌트는 다른 것이다.

- **roster의 테넌트는 계정 디렉터리다.** 누가 있고 어떻게 로그인하는지를 담는다. SaaS는 테넌트 하나에 계정 전체가 있고, 누구나 Google·Microsoft·GitHub로 가입한다(roster의 `login.enrol: enrolling`). Dedicated 고객은 자기 테넌트를 갖고, 거기에 회사 IdP와 SCIM이 붙는다. CE는 내장 roster다.
- **Rove의 테넌트는 워크스페이스다.** 데이터의 경계이고 payday의 테넌트 경계가 지킨다(6장). 한 계정이 워크스페이스를 여러 개 만들거나 여러 곳에 초대받는다.
- **Holder는 둘을 잇는 구성원 자격이다.** 워크스페이스마다 자기 ID를 갖고, roster의 계정을 `account`(roster의 테넌트와 holder의 ID)로 가리킨다. 역할(`role`)은 Holder에 있다.

```text
roster   테넌트 @rove (SaaS의 계정 디렉터리)   kim(Google), lee(GitHub), ...
Rove     테넌트 = 워크스페이스                  acme: kim(owner), lee(member)
                                                kim의 개인 워크스페이스: kim(owner)
```

워크스페이스를 Rove의 site(필드 3)나 roster의 팀으로 두지 않는다. 그러면 고객 사이의 격리가 payday의 테넌트 경계에서 손으로 짠 정책으로 내려간다. 그런데 그 경계 하나에 다른 회사에 보이면 안 되는 정보가 걸려 있다. 직원의 이름과 연락처, 누가 무엇을 갖고 있는지, 예약, 구매가가 그렇다. 계약·legal hold·내보내기·삭제·감사 기록·과금도 모두 테넌트 단위다. site는 한 워크스페이스 안의 사업장을 위해 남겨 둔다.

**두 가지 모드**: 같은 코드가 붙는 리스너만 다르다. Rove의 나머지는 어느 모드인지 모른다.

| | 외부(기본) | 내장 |
| --- | --- | --- |
| roster | 따로 배포한다. `auth.roster.addr` | Rove 프로세스 안. `auth.roster.addr`가 비어 있으면 이것 |
| 연결 | gRPC + TLS(`auth.roster.ca_file`) | `bufconn`. 프로세스 밖에서는 붙을 수 없다 |
| Rove가 내미는 자격 | 배포 키 `rk_`와 `roster-at`, 또는 테넌트 키 `rt_` | 없다. payday Plain으로 호출자가 밝힌 신원을 믿는다. 밖에서 닿지 않으니 성립한다 |
| roster의 DB | roster의 것 | 따로 둔다. SQLite면 Rove DB 옆의 `roster.db`, PostgreSQL이면 같은 서버의 다른 DB |
| SSO | roster의 issuer로. SaaS는 이것만 쓴다 | 없다. 비밀번호만 |
| 계정과 워크스페이스 만들기 | SaaS: 계정은 roster에 가입하고, 워크스페이스는 Rove에서 만든다. Dedicated: roster 운영자가 고객의 테넌트를 만든다(`roster tenant add`, `roster app install rove`) | `rove init`, `rove holder add`가 내장 roster에 |

- 외부 roster에서 Rove의 모양은 roster `docs/apps.md`의 A(여러 테넌트를 섬기는 인스턴스 하나, `rk_`)가 기본이다. 테넌트는 그 키를 지명(nominate)한 곳이고, 요청마다 `roster-at`으로 테넌트를 밝힌다. 한 조직이 직접 띄우는 Rove는 C(`rt_`)다. 키의 접두사로 구별하므로 설정은 키 하나다.
- 내장은 배포를 간편하게 하려는 것이다. 서버 한 대(9.2절)에 roster를 따로 띄우지 않는다. `unix` 소켓은 roster를 다른 프로세스(사이드카)로 뺄 때에만 필요하다. CLI(`rove init` 등)는 내장 roster의 DB를 직접 연다.
- 내장에서 지킬 것(shale에서 확인됨):
  1. **frame을 넘기지 않는다.** 두 앱이 payday의 frame 타입을 같이 쓰므로, Rove 요청의 context를 그대로 넘기면 roster가 Rove의 행위자를 자기 사람으로 읽는다. roster에 묻는 context에서는 frame을 뗀다(마감 시각은 가져간다).
  2. **배포의 문.** 테넌트마다 Rove의 holder(`rove`)와 그 역할을 쓰는 일은 roster의 경계 없는 서버(`Ungated`)로 한다. 이 문은 Rove 프로세스 밖으로 나가지 않는다.
  3. roster의 spin 루프(정리 작업)는 Rove의 serve가 함께 돌린다.

**Rove에 남는 것과 roster로 가는 것**

- roster로: 사람(계정) 만들기, 비밀번호와 그 재설정, 로그인 중지, 2단계 인증, IdP 연결, SCIM. Rove의 `Credential` 엔터티와 비밀번호 코드(`server/password`)는 없어진다.
- Rove에: 워크스페이스(`Tenant`)와 구성원 자격(`Holder`)과 그 `role`, 초대, `Party`와 `Party.account → Holder`, 세션(`Session`, DB에 둔다 — report 결정 3), 계약·법적 보존·보존 기간, 가명화.

**행을 roster에 잇는 방법(D27이 D26을 고친다)**: D26은 Rove의 `Tenant`·`Holder`를 roster의 것과 같은 ID로 두었다. 이제 `Holder`는 자기 ID를 갖고 roster의 계정을 `account`로 가리키며, 워크스페이스(`Tenant`)의 ID와 alias도 Rove의 것이다. 제품군과 상관없이 같은 모양이다. roster의 테넌트가 워크스페이스 하나와 같은 Dedicated·CE도 같은 코드로 돈다. trail의 행위자는 Holder의 ID이고, 계정은 `account`로 찾는다. M2 1단계의 "처음 로그인하면 roster의 ID로 테넌트와 Holder를 만든다"는 "가입하면 개인 워크스페이스와 그 소유자 Holder를 만든다"(9.11절)로 바뀐다. 워크스페이스가 처음 생길 때 테넌트 설정(유형 템플릿 복사, 6장)을 한다.

**로그인**

- **비밀번호**(CE의 옵션): `POST /session {tenant, alias, password}`를 받으면 Rove가 roster에 묻고(`VouchService.Verify`), 맞으면 Rove의 세션 쿠키를 준다. Rove는 비밀번호도 그 검증값도 갖지 않는다. SaaS에서는 꺼져 있다.
- **SSO**(외부 roster, SaaS는 이것만): Rove의 HTTP 리스너가 roster issuer의 relying party가 된다(roster `docs/relying-party.md`의 `itself` 모양). `GET /sso/login` → issuer → `GET /sso/callback`(state·nonce·PKCE, id_token 검증) → 계정 단위의 세션 쿠키. 워크스페이스는 요청의 경로가 고르고, 그 계정의 Holder가 그 워크스페이스에 있어야 한다(9.11절). `auth.sso_only`는 비밀번호 로그인을 끈다.
- **세션**은 Rove의 DB에 둔다. 지금 당장 끊을 수 있어야 하기 때문이다(report 결정 3). 다만 roster에서 사람이 중지·삭제되거나 "모든 곳에서 로그아웃"되면 Rove의 세션도 끝나야 한다. 그래서 세션을 쓰는 호출마다 roster에 그 사람을 묻고(`HolderService.Get`, 짧게 캐시), 물을 수 없으면 거부한다(fail closed). 외부 roster면 그 위에 `SyncService/Watch`를 열어 두고 roster가 말하는 대로 세션 행을 바로 지운다(`server/lifecycle`, M2 5단계). 모든 곳에서 로그아웃이면 그 전에 시작된 세션을, 중지·삭제면 그 계정의 세션 전부를 지운다. roster가 디렉터리를 닫으면(roster#105) 스트림은 그 디렉터리의 사람마다 `date_closed`를 실은 이벤트를 보내고, Rove는 중지처럼 그 계정의 세션을 모두 지운다. 닫힌 디렉터리에는 Rove의 키도 들어가지 못하므로 호출마다 묻는 확인도 실패하고(fail closed), 디렉터리를 복구하면 다시 들어온다. 닫기는 아무것도 지우지 않으므로 떠나는 중으로 표시하지 않는다. 스트림은 끊긴 사이의 일을 다시 보내지 않으므로 다시 열 때는 캐시해 둔 답을 모두 버린다. 스트림이 거부되면(역할에 Watch가 없거나 roster에 broker가 없으면) 오류를 한 번 남기고 드물게 다시 청한다. 그동안에도 세션은 다음 호출에서 끝난다.
- **계정의 탈퇴**: roster에서 계정이 삭제되면 그 계정의 구성원 자격은 모든 워크스페이스에서 '떠나는 중'이 된다(`Holder.date_leaving`, roster가 삭제한 시각). 바로 가명화하지 않는 것은 roster의 삭제에 유예가 있기 때문이다(`holder.forget_after`, 그동안 `roster restore`로 되살린다). 바로 가명화하면 되살린 계정이 아무도 아닌 사람으로 돌아온다. 유예가 지나도 roster가 그 계정을 되살리지 않았으면 모든 워크스페이스에서 그 계정의 Party를 가명화하고 Holder를 끝낸다(`app.accounts.forget_after`, roster의 유예와 같게 둔다). 워크스페이스마다 하는 일은 관리자의 '개인정보 삭제'와 같다: Party는 ID만 남기고, Holder의 이름, 그 사람에게 보낸 초대의 주소, trail의 사본을 지운다. 이력은 가명으로 읽힌다. 그 계정이 마지막 소유자였던 워크스페이스는 남은 사람 중 역할이 가장 높은 사람(관리자 > 매니저 > 구성원), 같으면 먼저 들어온 사람에게 넘긴다. 감사자와 떠나는 중인 사람은 받지 않는다. 받을 사람이 없으면 소유자 없이 남는다(워크스페이스 닫기, D29). roster의 `restore`와 `forget`은 roster의 서버를 거치지 않고 써서 스트림에 나오지 않는다. 그래서 떠나는 중 표시를 보고 무언가를 하기 전에는 roster에 다시 묻고, 되살린 계정은 그때 표시가 지워진다. 스트림은 끊긴 사이의 일을 다시 보내지 않으므로, 그동안 삭제된 계정은 서버가 하루에 한 번(`app.accounts.reconcile`) 모든 계정을 roster에 다시 물어 찾고 지금부터 떠나는 중으로 표시한다. roster가 하나도 모른다고 답하면 디렉터리나 키가 잘못된 것으로 보고 아무것도 표시하지 않는다. 운영자는 같은 일을 `rove identity reconcile`로 미리 보고 바로 한다. 바로 잊어 달라는 요청은 `rove identity depart`로 유예 없이 처리한다. 계정을 지우는 화면은 roster의 계정 페이지에 있다(`MeService.Leave`, roster#106, 디렉터리의 `TenantConfig.leave`가 켜져 있을 때). Rove는 `auth.roster.account_url`이 있으면 내 정보 화면에서 그 페이지의 `#leave`로 보낸다.

**Rove의 holder가 roster에서 하는 일**: 비밀번호 확인, 사람·테넌트 읽기, 사람이 가진 권한 묻기(`HolderService/Reaches`), 내장에서는 사람 만들기. shale의 `AgentMethods`와 같은 목록에서 시작한다. 외부 roster에서는 사람을 roster에서 만들므로 더 적게 든다.

**roster에 물을 수 없을 때**: 로그인과 세션을 쓰는 호출이 `UNAVAILABLE`로 거부된다. 실패는 잠깐(5초) 기억해 호출마다 묻지 않는다. 외부 roster의 가용성이 Rove의 가용성이 된다는 뜻이고, 서버 한 대 배포가 내장을 쓰는 이유이기도 하다.

**기존 배포**: roster 이전에 만든 배포는 업그레이드 뒤 `rove identity migrate`를 한 번 돌린다. 테넌트와 사람이 같은 ID로 내장 roster에 들어가고, 비밀번호는 새로 발급해 한 번 보여 준다(shale `identity migrate`와 같다). 비밀번호 검증값은 옮기지 않는다. 두 앱의 형식이 같다는 보장이 없고, 검증값을 들여오는 길은 무엇이든 들여오는 길이 된다.

**테스트**: 내장 roster는 진짜 roster이므로 테스트는 가짜 대신 내장 roster를 띄운다. 외부 모드는 같은 roster를 TCP 리스너와 키로 띄워 같은 테스트를 돈다.

**정한 것**(2026-10-11, plan 10장): 역할은 Rove의 `Holder.role`로 둔다. 외부 roster의 테넌트는 roster 운영자가 만들고(셀프 가입 없음), 테넌트의 첫 사람이 처음 로그인할 때 Rove에 테넌트가 생기며 그 사람이 소유자가 된다. 탈퇴는 Rove 내보내기 → Rove 전체 삭제 → roster의 테넌트 정리 순서다. 사람을 지우는 것은 roster의 일이고, Rove의 '개인정보 삭제'는 Rove 쪽만 지운다. 다만 내장 roster는 Rove만 쓰므로, Rove에서 로그인이 끝나면(로그인 중지, 개인정보 삭제, 테넌트 전체 삭제) roster의 `forget`으로 그 사람을 바로 파기한다. roster의 삭제 유예는 복구(`roster restore`)를 위한 것인데 Rove는 복구를 열지 않으므로, 유예는 지연일 뿐이다. 기존 데이터는 `rove identity migrate`로 옮긴다.

**SaaS에서 바뀐 것**(D27~D29, 2026-10-11): 셀프 가입을 둔다. 계정은 SaaS의 roster 테넌트에 가입하고, 워크스페이스는 Rove에서 만든다. 첫 사람이 소유자가 되는 대신 만든 사람이 소유자다. 탈퇴는 워크스페이스 닫기(8.4절)이고, 계정의 탈퇴는 그 계정이 속한 모든 워크스페이스에서의 가명화다. 내장 roster에서 사람을 파기하는 것도 계정이 탈퇴할 때로 옮긴다. 한 워크스페이스에서 구성원 자격이 끝나는 것은 계정을 지우는 것이 아니다.

### 9.11 워크스페이스와 주소 (SaaS, D28)

- **로그인은 한 곳에서 한다.** 모든 사람이 앱 호스트(예: `rove.io`)의 로그인 화면에서 Google·Microsoft·GitHub 중 하나로 로그인한다. 흐름은 roster의 issuer(예: `id.rove.io`)와 그 IdP의 화면을 잠깐 거쳐 앱 호스트로 돌아온다. SaaS에는 Rove의 비밀번호 로그인이 없고, 2단계 인증은 각 IdP의 것이다. 기업 IdP는 Phase 3에서 roster의 `Connection`·`MailDomain`·SCIM으로 붙인다.
- **터미널도 같은 계정이다.** 모든 기능은 API이고 터미널에서도 쓴다(`rove api`). 터미널은 브라우저와 같은 주소로 붙고, 로그인은 이미 로그인한 브라우저에서 승인한다(`rove login`). 터미널이 세션 키를 스스로 만들어 해시만 보내고, 승인하면 그 해시로 그 계정의 세션이 생긴다. 그래서 키는 서버에 남지 않고, 세션은 브라우저의 것과 똑같이 권한 표·roster 확인·유지 시간을 따른다. 자동화용 토큰은 나중이다.
- **터미널은 좁혀서 로그인할 수 있다**(D33). `rove login --allow`로 부를 RPC를 적으면, 승인 화면이 그 목록을 보여 주고 세션이 그 목록만 허락한다(`Session.grant`, payday `frame.Grant`). 권한 표가 정한 것을 더 좁힐 뿐 넓히지 않고, 서버가 모든 호출에서 검사하므로 세션 키를 가진 누구도 목록 밖은 하지 못한다. 좁힌 세션은 RPC 옆의 HTTP에서 읽기만 한다. 다른 터미널을 승인하면 좁힌 것이 소용없고, 내보내기는 워크스페이스 전체를 읽기 때문이다.
- **워크스페이스는 경로로 고른다.** 주소는 `rove.io/@<alias>/...`이다. 하위 도메인을 쓰지 않으므로 인증서·DNS·쿠키가 호스트 하나로 끝난다. 세션은 계정 단위이고, 요청마다 경로의 워크스페이스와 그 계정의 Holder로 frame을 만든다.
- **약관과 개인정보 처리방침에 먼저 동의한다**(2026-10-12 결정). 배포가 문서마다 시행 중인 버전과 게시 주소를 정하고(`app.consent`), 계정은 처음 들어올 때와 버전이 바뀔 때 동의 화면을 본다. 동의는 계정의 것이다. 워크스페이스마다가 아니라 한 번 한다. 동의하지 않은 계정의 세션은 모든 RPC가 거부되고(resolver, `FailedPrecondition`), 새 워크스페이스도 만들지 못한다. 동의 자체는 RPC 옆의 HTTP다(`GET`/`POST /account/consents`, 터미널은 `rove consent`). 가입 때의 개인 워크스페이스도 동의한 뒤에 만든다. 동의 전에는 Rove에 그 사람의 기록을 만들지 않기 위해서다. 자기 워크스페이스의 데이터 내보내기는 동의와 상관없이 된다. 문서 내용과 시행일은 운영자가 정한다. 버전을 정하지 않은 배포(직접 운영하는 팀)는 아무것도 묻지 않는다.
  - **기록은 roster의 동의 원장에 둔다**(`ConsentService`, D37). 어느 문서의 어느 버전이 시행 중인지는 Rove의 설정이고, 계정이 어느 문서의 어느 버전에 언제, 어느 경로(화면/터미널)로 동의했는지는 roster가 행으로 남기며 고치지 않는다. 원장은 부른 앱(roster에서의 Rove의 holder)을 행마다 적고 그 앱의 것만 읽어 준다. 배포 전체의 문서는 빈 범위(scope)에 둔다. 워크스페이스의 정책이 생기면 같은 행에 그 워크스페이스를 범위로 둔다. 동의는 roster의 trail에 남는다.
  - **보존과 파기도 roster가 한다.** 계정이 roster에서 삭제되어도 동의는 증거로 남고, roster의 `consent.keep`이 지나면 roster가 지운다. 내장 roster는 `auth.roster.consent_keep`이 그 기간이다. 정하지 않으면 지우지 않는다(기간은 법률 검토 뒤, rove#35). Rove는 계정이 떠날 때 동의에 아무것도 하지 않는다.
  - **호출마다 roster에 묻는다.** 동의했다는 답은 roster가 계정에 대해 하는 말처럼 30초 믿는다(`identity.HeldTtl`). 동의하지 않았다는 답은 매번 다시 묻는다. 다른 서버에서 동의했을 수 있기 때문이다. roster에 물을 수 없으면 동의했는지 알 수 없으므로 거부한다(`Unavailable`). 이때 5초(`identity.HeldRetry`) 동안은 다시 묻지 않는다. 문서를 정하지 않은 배포는 원장을 쓰지 않아서, 그 권한이 없는 roster 역할로도 된다.
- **가입하면 개인 워크스페이스가 하나 생긴다.** 계정이 소유한 워크스페이스는 N개까지 무료이고, 그 이상은 결제가 필요하다(N과 요금은 11장). 초대받아 들어간 워크스페이스는 세지 않는다.
- **alias**: 만들 때 무작위로 준다. 영문 소문자와 숫자 7자이고(헷갈리는 i·l·o·0·1 제외, 첫 글자는 영문), 7자가 붐벼 열 번 연달아 겹치면 저절로 한 글자 길어진다. 사용자는 바꿀 수 없고 운영자만 바꾼다(2026-10-12 결정, `rove tenant alias`). 유료로 바꾸게 할지는 결제를 붙일 때 다시 본다. 예약어(`www`, `api`, `admin`, `l`, `sso`, `login` 등)는 쓸 수 없다. 바꾸기 전 alias는 한동안 새 주소로 넘겨 주면서 다른 워크스페이스가 쓰지 못하게 보류한다(8.4절). 라벨은 alias를 쓰지 않는다(9.9절).
- **초대**(D30): 워크스페이스의 소유자·관리자가 이메일과 역할로 초대하고, 자기 역할보다 넓은 역할로는 초대하지 못한다. 초대장은 그 워크스페이스의 행(`Invitation`)이다. 받는 사람이 이미 가입했든 아니든 같은 메일이 간다. 가입하지 않았다면 링크를 열고 가입하면 된다. 초대하는 사람은 상대가 회원인지 알 수 없다.
  - **누구의 초대장인가**: roster가 그 주소를 확인된 것으로 갖고 있는 계정의 초대함에 보인다. 확인되지 않은 주소로는 보이지 않는다. 어느 디렉터리의 관리자든 자기 사용자에게 아무 주소나 적을 수 있고, 공급자는 그것을 그대로 넘긴다(nOAuth). 주소가 확인되지 않은 계정(예: `xms_edov`가 없는 Microsoft 계정)은 메일에 실린 1회용 링크로 그 초대장을 자기 것으로 받는다. 그 메일함을 읽었다는 것이 증명이다.
  - **수락하면** 그 역할의 Holder와 사람 기록(Party)이 생긴다. 초대가 이미 있는 사람 기록을 가리키면 그 기록이 계정과 이어져, 지급·예약 이력이 그대로 그 사람의 것이 된다. 이미 구성원이면 수락하지 않는다(역할은 관리자가 바꾼다).
  - **만료와 한도**: 기다리는 초대는 30일 뒤 만료된다. 워크스페이스마다 기다리는 초대는 50개, 보내는 초대는 하루 100개까지다. 모두 설정(`app.invitations`)이고, 메일은 SMTP 설정(`app.mail`)이다.
  - **계정으로 처리되는 호출**: 받는 쪽의 `InvitationService.List`(피초대자 필터)·`Claim`·`Accept`·`Deny`는 구성원 자격이 아니라 계정으로 처리한다. 받는 사람은 아직 그 워크스페이스의 구성원이 아니어서, 테넌트 경계가 그 행을 보여 주지 않기 때문이다. 이 호출의 frame에는 테넌트가 없고, 정책은 이 메서드만 허락하며, 어떤 행을 읽고 쓰는지는 도메인 레이어가 정한다(받는 계정 본인의 초대장만, 워크스페이스 쪽 목록은 그 워크스페이스의 관리자만). "모든 호출은 워크스페이스 하나의 구성원으로 처리한다"의 유일한 예외다.
  - **roster에 두지 않는다**: 초대는 워크스페이스와 역할에 묶여 있고, roster는 워크스페이스를 모른다. roster의 `login.enrol: expected`는 운영자가 디렉터리에 들어올 사람을 미리 넣는 것이라 Dedicated의 것이다. 다른 앱도 같은 것이 필요해지면 "주소 증명과 메일 발송"만 roster로 옮긴다.

### 9.12 비서 (MCP, D32)

- **비서는 터미널 명령으로 붙는다.** `rove mcp`는 표준 입출력으로 말하는 MCP 서버다. Claude Code나 Claude Desktop 같은 클라이언트가 이 명령을 띄운다(`cli/mcp.go`).
- **로그인한 사람으로 읽는다.**
  - 도구는 `rove api`처럼 `rove login`의 세션으로, 브라우저와 같은 주소에 RPC를 부른다.
  - 권한 표와 벽이 그대로 걸린다. 비서는 그 사람이 볼 수 있는 것만 본다.
  - 서버는 MCP를 모르고, 새 입구도 새 인증도 없다.
  - 로그인은 호출마다 다시 읽는다. 다른 계정으로 로그인하면 다음 호출부터 그 계정이다.
- **읽는 도구**는 자산 찾기, 자산 하나, 이력, 어느 시점의 모습, 최근 일, 지급과 대여, 예약, 사람과 팀 찾기, 모델 찾기, 그리고 재고·작업 지시·실사·구매의 목록이다. 답에는 사람이 읽는 이름을 넣고, ID는 다른 도구에 다시 넘기는 데 쓴다.
- **쓰기는 미리보기와 적용의 두 단계다**(D33, rove#94).
  - `plan_*`는 바뀔 것을 이름으로 적은 문장과 토큰을 돌려주고, 아무것도 바꾸지 않는다. 참조를 읽어 이름으로 바꾸므로, 없는 자산이나 사람은 여기서 거부된다.
  - `apply`는 토큰과 그 문장을 받아 RPC를 부른다. 문장이 인자라서 클라이언트의 승인 창에 무엇을 하는지가 보이고, 다른 문장이면 거부한다.
  - 미리보기가 op를 정해 두므로 적용을 다시 보내도 한 번이다. 토큰은 그 프로세스에서 10분, 한 번만 쓰인다.
  - 누가 무엇을 할 수 있는지는 적용할 때 권한 표와 도메인이 정한다. 터미널을 `--allow`로 좁혔으면 미리보기 단계에서 알린다. 이력에는 행위자만 남는다.
  - 구성원과 매니저의 작업을 연다: 자산, 지급과 반납, 예약, 재고, 실사, 작업 지시, 구매, 모델, 라벨(`plan_*` 아홉). 관리자와 소유자의 작업은 열지 않는다.
- **답은 비서를 제공하는 쪽에 간다.** 그래서 답에는 필요한 것만 넣는다. 사람의 이메일과 전화번호는 넣지 않는다.
- **원격 MCP는 나중이다.** 배포가 HTTP로 열면 설치 없이 쓸 수 있다. 다만 MCP의 인증(OAuth 2.1)을 위해 roster의 Hydra를 인가 서버로 두어야 한다.

### 9.13 가져오기와 복구 (D34)

**무엇을 가져오나.** 가져오기의 목적은 다섯이고, 목적마다 맞는 입력과 자산을 알아보는 기준이 다르다. 그래서 시트 가져오기 하나로 감당하지 않는다.

| 목적 | 정한 것 |
| --- | --- |
| 복구·이전 | 지금 만든다. 서명된 내보내기 zip을 다시 넣는다 |
| 신규 대량 등록(입고) | 지금 만든다. rove가 정한 양식으로 새 자산만 만든다 |
| 온보딩(다른 시스템에서 처음 옮기기) | 보류. 엑셀을 받지 않고, 외부 도구마다 연동을 만든다 |
| 외부 시스템 동기화(HR, MDM, ERP) | 보류. 연동과 같은 일이다 |
| 일괄 수정 | 보류. 아이디어만 둔다(아래) |

**없애는 것: 시트로 고치는 가져오기**(`AssetService.Import`). 태그로 기존 자산을 찾아 고치고, 없으면 등록하던 기능이다.

- 정체성을 사람이 바꾸는 값(태그)에 맡긴다. 다른 워크스페이스의 시트를 넣으면 태그가 같은 다른 물건을 고치고, 시트에서 태그를 고치면 새 자산이 생긴다.
- 빈 칸이 "그대로 둠"이라 값을 지울 수 없고, 태그 없는 행은 다시 보내면 중복된다.
- 행마다 자산 작업을 반복해 느리다(1.2절).
- 시트 내보내기(현재 대장)는 읽기용으로 남긴다.

**신규 대량 등록**

- **새 자산만 만든다.** 이미 있는 태그는 고치지 않고 오류로 답한다. 그래서 기존 행과 비교하지 않는다.
- **API는 생성 동사 `AssetService.AddMany`를 도메인이 완성한 것이다**(D24). 항목은 등록 요청이고, 미리보기(`dry_run`)·작업 ID(`op`)·사유를 요청에 덧붙인다. 외부 도구 연동도 같은 API를 부른다.
  - 항목은 유형·모델을 ID 대신 이름으로도 가리킨다(`type_name`, `model_name`, `maker`). 이름은 대소문자를 가리지 않고, 같은 이름이 여럿이면 먼저 만든 것이다.
  - rove 양식의 CSV를 항목으로 읽는 것은 `AssetService.ReadSheet` 한 곳이다. 화면과 `rove register`가 그것을 불러서, 둘이 같은 파일을 같게 읽는다. 양식(머리글만 있는 CSV)은 `AssetService.Export`가 `template`으로 쓰고, 열마다 적는 것은 화면이 보인다.
- **양식**: 이름(필수), 종류, 유형, 모델·제조사, 시리얼, 태그(비우면 자동), 위치태그, 취득일, 기준일(입고일), 설명, 속성.키.
  - **CSV만 받는다**(UTF-8, 또는 한글 엑셀이 기본으로 저장하는 CP949). 엑셀 파일(.xlsx, .xls)은 첫 바이트(zip, OLE)로 알아보고, 열지 않은 채 거절한다.
    - 서버는 모든 워크스페이스가 함께 쓰는 프로세스이고, 계정이 있으면 누구나 자기 워크스페이스에서 시트를 올릴 수 있다.
    - 엑셀 파일은 XML 여러 개를 묶은 zip이고, 옛 형식과 암호를 건 것은 OLE 컨테이너다. 그것을 읽는 라이브러리(excelize)는 수식 계산과 복호화까지 하는 큰 코드다.
    - 2026-10에 조작된 파일 하나로 읽기가 끝나지 않거나 메모리를 수 GiB 잡게 하는 공지가 한꺼번에 나왔다(GO-2026-6684, 6685, 6688, 6689). 그중 끝나지 않는 읽기와 메모리 고갈은 recover로 막을 수 없어서, 한 사람이 올린 파일 하나로 배포 전체가 멈출 수 있다.
    - CSV는 표준 라이브러리가 읽고, 해석할 구조가 없다.
  - 엑셀은 셀 서식이 텍스트가 아닌 긴 숫자를 CSV에 보이는 대로(3.57951E+14) 쓴다. 시리얼·태그·속성에서 그런 값은 숫자를 잃은 것이므로, 그 행의 문제로 알린다.
  - 내보내기(대장)도 CSV만 쓴다. 그래서 rove에는 엑셀 파일을 읽거나 쓰는 코드와 라이브러리가 없다. 엑셀로 열면 긴 숫자와 앞의 0은 엑셀이 보이는 방식대로 보이지만, 파일에는 적힌 그대로 있다.
  - 위치는 이미 있는 공간의 태그로만 받는다. 경로로 공간을 만들지 않는다. 등록이 공간까지 만들면 오타 하나가 새 공간이 된다.
  - 사용자(지급)는 받지 않는다. 지급은 등록과 다른 작업이다.
  - 없는 이름의 유형·모델은 미리보기에서 "새로 만듦"으로 보이고, 확정할 때 만든다. 유형을 만드는 것은 관리자의 일이므로(권한 표), 관리자가 아닌 사람의 시트에 없는 유형이 있으면 그 행의 문제로 알린다. 모델은 매니저도 만든다.
- **검사는 세 겹이고, 어느 것도 행마다 묻지 않는다.**
  - 역할(권한 표): 호출에 한 번.
  - 벽: 항목이 가리키는 위치·유형·모델이 호출자에게 보이는지, 서로 다른 대상마다 한 번(payday가 트랜잭션 안에서 기억한다). 간선은 읽기이므로 이 검사는 빼지 않는다(D11).
  - 행 검증: 필수값, 태그 중복(시트 안, 그리고 기존 자산을 한 번에 조회), 낱말과 날짜(미래 불가). 메모리 안에서 한다.
- **묶어 쓴다.** 자산, 첫 Fact, 배치, Event를 수백 행씩 한 문장으로 쓴다(AddMany). 만든 뒤 잰 것은 자산 하나에 SQL 2.1문장이다(하나씩 등록하는 재고 변환은 11.2). 자산이 10만 개 있는 스테이징 워크스페이스에서 5,000행 등록은 10초, 미리보기는 0.1초였다(1,000행은 2.9초). 서버 제한 시간(30초)의 1/3이므로, 한 번에 받는 행 수는 5,000으로 둔다(rove#99).
- **이벤트**는 부모 "대량 등록: N개"(누가, 무슨 사유로) 하나와 자산마다 자식 "등록"이다(아래 원인). 누가 무엇을 입고했는지 부모에서 찾고, 자산마다 그 자산의 이력에 남는다.

**이벤트의 원인**

- Event에 원인(`cause`, 부모 Event)을 둔다. 여러 대상에 걸친 작업은 부모 하나와, 영향을 받은 대상마다 자식 하나를 남긴다.
- 자식의 주 대상은 그 대상이고, 종류는 같은 일을 하나씩 했을 때와 같다(`asset.add`, `asset.move` 등). payload에는 그 대상에서 바뀐 것을 담는다. 시간 행은 자식을 가리킨다. 바뀐 것이 없는 대상은 자식을 남기지 않는다.
- 그래서 자산의 이력(`AssetService.Timeline`)과 주 대상으로 거른 목록(`EventService.List`)에 그 자산에 일어난 일이 그 자산의 Event로 보이고, 부모를 따라가면 어떤 작업이었는지 안다. 종류가 같으므로 Event에 반응하는 곳(알림, 색인)은 하나씩 한 것과 묶어 한 것을 가리지 않는다.
- 피드(`EventService.Recent`)는 기본으로 부모만 보인다.
- 대량 등록, 지급(지급 문서가 부모), 예약, 키트 이동, 실사에 쓴다. 지금은 지급된 자산의 이력에 지급 Event가 없고, 여러 자산을 예약하면 첫 자산에만 남는다. 이 빈틈이 같이 사라진다.

**서명된 내보내기**

- 워크스페이스 내보내기(zip)의 manifest에 형식 버전과 모든 파일의 SHA-256을 적고, manifest에 배포의 키로 서명한다. 서명은 감사 기록 체크포인트와 같은 서명자(`crypto.Signer`: 키 파일, TPM, HSM, 클라우드 KMS)가 한다.
- 형식은 계약이다. 나중 버전도 옛 버전을 읽는다. 운영을 시작하기 전에 정한다. 서명과 버전 없이 나간 내보내기는 나중에 복구할 수 없기 때문이다.
- 서명은 위조를 막을 뿐 내용을 숨기지 않는다. 개인정보가 들어 있으므로 암호화는 나중의 선택지로 둔다.
- **한 시점의 워크스페이스이고, 크기와 상관없이 읽는다**(rove#120).
  - 읽기 전용 스냅샷 트랜잭션 하나에서 읽는다(PostgreSQL은 REPEATABLE READ). 쓰는 중에 내보내도 자산 없는 Fact처럼 복구가 거절할 파일이 되지 않는다.
  - 표마다 목록과 같은 순서(만든 때, 그다음 키)로 천 행씩 읽어 바로 쓴다. 그래서 메모리는 워크스페이스 크기와 상관없다. 감사 기록은 그 워크스페이스의 행, 그 사람들이 다른 곳에서 한 일, 다른 곳에서 그 워크스페이스에 한 일의 세 갈래로 각자의 인덱스를 타서 읽고, 쓴 순서로 합친다.
  - 소유자의 내보내기는 임시 파일에 다 쓴 다음 보낸다. 브라우저가 천천히 받는 동안 스냅샷을 붙들지 않고, 실패하면 깨진 zip 대신 오류로 답한다.

**복구**

- 서명된 내보내기만 받는다. 서명과 모든 파일의 해시를 검증한다. 믿는 키는 그 배포의 키(교체 전의 옛 키 포함)와, 운영자가 신뢰 목록에 넣은 다른 배포의 공개 키다.
- **빈 새 워크스페이스로, 원래 ID가 그 배포에 없을 때만 넣고, ID를 지킨다.** 파기된 워크스페이스를 되살리거나 다른 배포로 옮기는 경우다.
  - 같은 배포에 원본이 있으면 ID가 겹치므로 거부한다. 오래된 내보내기로 지금 데이터를 되돌리는 일도 이것으로 막힌다.
  - ID를 지키므로 붙여 둔 라벨과 링크가 계속 열린다.
- 전부 한 트랜잭션이다. 한 행이라도 들어가지 못하면 아무것도 남기지 않는다. 파일은 커밋 전에 넣고, 커밋하지 못하면 다시 지운다.
- 복구할 테이블과 필드는 스키마에서 읽는다(파기의 순서가 그렇듯). 나중에 생긴 엔터티와 필드도 따로 적지 않아도 복구되고, 모든 테이블의 모든 열에 갈 곳이 있는지는 테스트가 본다. 이 빌드가 지운 테이블(`offboard.Dropped`, 예: 7장의 `UsageSnapshot`)의 행은 되살리지 않고, 두고 온 수를 복구 이벤트에 남긴다. 마이그레이션이 그 테이블을 지우므로 둘 곳도 없다. 그 밖에 이 빌드에 없는 테이블의 행이 있으면 거부한다. 빠뜨린 채로 다 되살렸다고 말하게 되기 때문이다.
- **감사 기록은 새 배포의 체인에 섞지 않고, 배포 안에 따로 두지도 않는다.**
  - 섞으면 체인과 체크포인트가 깨진다.
  - 서명된 사본을 배포 안에 두면, 나중에 그 안의 사람을 개인정보 삭제할 때 서명을 깨지 않고는 사본에서 지울 수 없다.
  - 내보내기 파일이 그 기록이고, 원래 서명으로 검증한다(`rove tenant verify`). 복구 이벤트에 그 파일의 manifest 해시와 서명 키 지문을 남겨, 어느 파일에서 복구했는지 맞춰 본다.
- **계정**: 구성원(Holder)은 roster 계정을 가리킨다. 이 roster에 있는 계정이면 그대로 잇는다. 없으면(다른 배포의 계정, 또는 파기 때 함께 지운 계정) 계정 없이 복구한다. `rove identity migrate`가 그들에게 계정을 준다(D27로 옮길 때와 같은 길). 이메일로 다시 잇기는 나중이다.
- **주소**(alias)는 원래 것을 쓴다.
  - 그 워크스페이스가 떠날 때 걸린 보류는 복구가 푼다. 그 워크스페이스를 위해 잡아 둔 주소이기 때문이다.
  - 다른 워크스페이스의 보류는 운영자가 풀고(`--reuse`), 그 사실을 복구 이벤트에 남긴다.
  - 다른 워크스페이스가 쓰고 있으면 새 주소를 받는다. 운영자가 정할 수도 있다(`--alias`).
- **은퇴한 워크스페이스는 그 배포에 같은 ID로 되살리지 않는다**(payday#115). 그 배포의 trail이 그 조직을 떠난 조직으로 기억해서, 되살린 워크스페이스가 쓰는 trail을 보존 패스가 지우고 마지막 바닥값이 지나면 키까지 지우기 때문이다. 다른 배포에는 되살릴 수 있다. 전부 지운(purge) 워크스페이스는 그런 기록을 남기지 않아 그 배포에도 되살아난다. payday가 떠난 조직을 되돌리는 길을 갖추면 이 제한은 없어진다.
- **닫힌 채로 내보낸 워크스페이스는 열린 상태로 돌아온다.** 복구는 되찾으려고 하는 것이다. 복구 기간이 지난 채 닫혀 있으면 다음 파기 주기에 다시 파기된다.
- 복구는 운영자의 일로 시작한다(`rove tenant import`, 내보내기·파기와 같은 자리. `rove tenant restore`는 닫힌 워크스페이스를 되돌리는 명령이라 이름을 달리한다). 파기처럼 `--yes` 없이는 미리 보기만 한다. 이벤트(`workspace.import`)는 아무도 가리키지 않는다. 워크스페이스 안의 누가 한 일이 아니기 때문이다. 소유자가 직접 하는 길은 나중이다.

**일괄 수정 (아이디어, 정하지 않음)**: 사람이나 AI가 쓴 프로그램을 서버가 작업(job)으로 돌려 바뀔 것(diff)을 보여 주고, 사람이 확정한다. 확정할 때 대상의 버전이 미리보기 때와 같은지 다시 본다. 프로그램은 샌드박스에서 결정적으로 돈다(wasm, 또는 CEL). AI는 데이터가 아니라 스키마와 사람의 말만 보고 프로그램을 쓰므로 고객 데이터가 AI 제공자에게 가지 않는다. rove가 제공하는 AI는 과금 후보다.

## 10. 구현 단계 및 검증 기준

**Phase 0 스파이크 — 완료(2026-10-10)**: (1) 마이그레이션은 디렉터리를 나누고 복합 FK 대신 트리거를 쓴다(9.4절). (2) `seal`과 도메인 트랜잭션은 batch 안에서도 동작하며, 도메인 레이어는 Gate 위에 둔다(9.3절). (3) Gate의 엣지 확인과 `agrees`는 다른 테넌트로의 참조를 거부한다(6장). (4) 관리형 DB의 확장 지원을 조사했다. 지금은 self-host이고, 표는 관리형으로 옮길 때 참고한다(9.5절). (5) 테넌트 잠금 행 없이는 동시 순환이 실제로 생긴다(3.2절). 방법과 근거는 [plan.md](plan.md) 1장에 있다.

**Phase 0 — 기반**: payday 앱 골격, roster 신원과 세션(9.10절), 역할 Policy, 공개·운영자 진입점, `Asset`·`AssetType`·`ItemModel`·`Party`, 시간 행(`Placement`·`Stewardship`·`Fact`·`Link`)과 `Event`, 도메인 작업(자산 `Add`·`Erase` 완성, Move, Install, Remove, Assign, SetAttributes, Correct), `Timeline`·`QueryAt`·`AsOf`·`Diff`, `Label`, `Attachment`, 두 갈래 마이그레이션. *검증*:
- 교차 테넌트: 모든 RPC에 다른 테넌트의 ID를 넣으면 NotFound, `agrees` 위반 엣지는 거부.
- 시간 오라클: 무작위 이동·정정 시퀀스를 생성해 `QueryAt(T)`·`AsOf(T, K)`가 단순 재생 구현의 결과와 같은지 속성 기반 테스트로 확인.
- 동시성: 같은 자산의 동시 이동 N건 중 하나만 성공, 동시 순환 생성 시도는 거부, 같은 슬롯 동시 장착은 하나만 성공.
- PostgreSQL과 SQLite 모두 통과, 1.2절의 성능 목표 측정.

**Phase 1 — MVP**: 웹 UI(자산 목록·상세·타임라인·시점 슬라이더·Diff), 공간 계층, 테넌트 라벨 도메인(등록·확인·교체)과 QR 라벨 인쇄·스캔(PWA), 과거 시각을 포함한 CSV/Excel 가져오기·내보내기, Custody(지급·인수 확인·반납), 검색, 기본 뷰 플러그인 2~3종(랙 U, 하드웨어 구성). *검증*: 가져오기 직후 과거 시점 재현, 지급 → 반납 → 정정 시나리오의 `AsOf` 결과, 일반 직원 셀프서비스(스캔 → 인수 확인), 가설 고객 파일럿.

**Phase 2 — 예약과 운영**: `Bookable`·`Allocation`·단건 예약(홀드, 승인, buffer, override), 공간 배타 그룹, 키트, 풀 자원 예약, 실사(오프라인 큐 포함), 재고, 작업 지시·정비 블록, 구매, 알림, 기본 리포트, 반복 예약, 도메인 모듈 구조. *검증*: 같은 자원에 대한 동시 예약 100건에서 겹침 0, 키트 구성품 충돌 감지, 재고와 대여 수량의 일관성, 늦게 도착한 오프라인 스캔의 처리, 일괄 작업의 롤백·재시도.

**Phase 3 — SaaS 고도화**: 기업 IdP·SCIM 연결(roster의 기능을 켜는 일), 사업장 단위 권한(필드 3), API 토큰·Webhook, 캘린더 연동(ICS 발행 → 양방향), 보존·삭제 잡과 개인정보 삭제, 사용량 원장·Entitlement·과금, 서드파티 플러그인, RFID/GPS 연동, 회계 내보내기·감가상각. *검증*: 사용량 재계산, 보존 정책 dry-run, 과금 정책 시뮬레이션, 복구 및 법적 보존 예외, 개인정보 삭제 후 trail·아카이브 확인.

**과금 도입 게이트**: (1) 미터 정의와 신뢰성 (2) 고객이 보는 사용량과 백엔드 청구값의 일치 (3) 계획 변경 및 정정 (4) 내보내기·고지·유예 (5) 삭제와 백업 만료 정책 (6) 약관·법률 검토가 모두 끝난 뒤에 제한을 활성화한다.

## 11. 추후 결정할 정책

1. 무엇을 `billable asset`으로 셀지: kind(실물·공간·키트·그룹), 일시 중지·폐기 자산, 수량형 재고의 기준.
2. 워크스페이스가 정하는 보존 기간의 범위와 배포의 유예 기간(`app.retention.grace`, 지금 기본 30일), 고지와 법적 보존 예외.
3. 결제 실패·계약 종료·다운그레이드 시 신규 기록 생성, 조회, 보존 및 삭제 정책.
4. GB vs GiB, 청구 대상 데이터(원본·플러그인), 무료 포함량, 평균/최대 사용량 중 과금 방식.
5. 예약 세부: 반복 예약을 미리 만들어 둘 범위, no-show 판정 기준, 요청 만료 시간, 외부 캘린더 동기화 방향.
6. 사업장 단위 권한 모델, 그리고 자산이 사업장 사이를 옮길 때 이력을 어느 범위까지 보여 줄지.
7. 플러그인 런타임·배포 정책, 서드파티의 권한 심사와 저장 공간 계약.
8. 오프라인 실사의 충돌 처리: 같은 자산을 두 기기가 다르게 스캔했을 때의 우선순위.
9. 국가별 법정 보존·삭제 요구, 고객 데이터 내보내기와 탈퇴 절차.
10. 무료로 소유할 수 있는 워크스페이스 수(N)와 그 이상의 요금, alias를 바꾸는 것의 요금(9.11절).

## 12. 결정 기록

- **제품**: Rove, IT 특화가 아닌 범용 실물 자산+공간 운영 SaaS.
- **차별화**: 시간에 따라 복원 가능한 자산 그래프, 플러그인으로 제공되는 유형별 특수 뷰, QR 중심의 실제 업무.
- **과금**: 지금 가격을 정하지 않는다. 일상 사용은 관대하게 허용하고, 장기 이력·기업 감사·대규모 저장을 수익화 후보로 둔다.

| # | 결정 | 이유 |
| --- | --- | --- |
| D1 | MVP는 시간 복원 레지스트리 + 공간 계층 + QR + 지급·반납. 예약은 Phase 2 | 차별점을 먼저 검증하고, 범위가 큰 예약을 그 위에 얹는다 |
| D2 | 장소 계층은 공간 Asset의 Placement 트리 하나. `Location` 엔터티 없음 | 계층이 둘이면 위치 조회·권한·실사·재고가 모두 두 경로를 다뤄야 한다 |
| D3 | 공통 기준점은 `Asset`(kind로 실물·공간·키트·그룹 구분). 사람·조직은 `Party` | 관계·QR·첨부·플러그인이 한 종류의 엣지로 붙고, 개인정보는 한 엔터티에 모인다 |
| D4 | 배치·관계·담당·속성은 이원 시간 행. 행은 쓰고 나면 바꾸지 않고, 바꿀 때는 대체 표시 후 새로 쓴다 | 소급 기록이 일상이고, 감사에는 "그때 시스템이 보여 준 것"이 필요하다. 보존 경계도 자연스럽게 생긴다 |
| D5 | 속성 이력은 before/after가 아닌 Fact(단언) | 과거 구간만 바꾸는 변경을 표현할 수 있다 |
| D6 | "누가 갖고 있나"는 `Stewardship(CUSTODIAN)` 하나. Custody는 그것을 열고 닫는 업무 문서 | 같은 사실을 관계·대여·예약 세 곳에 두지 않는다 |
| D7 | 물리 배치 모드(놓임·장착·구성품)는 Placement 하나의 속성 | "부모 하나" 규칙이 관계 타입을 넘나들지 않게 한다 |
| D8 | 예약 충돌은 Allocation 행 + 자원 행 잠금 + PostgreSQL `EXCLUDE` | 키트·상위 공간·정비·buffer·반복 예약을 한 가지 제약으로 막는다 |
| D9 | 배치 트리 변경은 테넌트 잠금 행으로 직렬화 | 자식·부모 행 잠금만으로는 동시 순환을 놓친다 |
| D10 | 구현 기반은 payday. 생성 CRUD와 테넌트 경계를 쓰고, 쓰기는 도메인 RPC로만 | 테넌트 경계·trail·watch·클라이언트를 직접 만들지 않고, payday의 원칙이 Rove의 "도메인 작업 우선"과 같다 |
| D11 | 테넌트 격리는 payday 경계 + Gate의 엣지 확인 + `agrees` + PostgreSQL 트리거. RLS·복합 FK는 쓰지 않음 | Gate는 여러 테넌트를 보는 호출자와 Gate 아래의 쓰기를 못 막는다. 복합 FK는 payday의 서버 시작 전 검사가 거부하고, RLS는 운영자 스택·정책 모델과 맞지 않는다 |
| D12 | 운영은 PostgreSQL, SQLite는 테스트·데모. PostgreSQL 전용 DDL은 ent와 다른 마이그레이션 디렉터리에 두고 같은 리비전 흐름으로 적용 | 같은 디렉터리에 두면 Plan이 매번 지우려 한다. 규칙은 두 DB에서 같게 두고, DB 제약은 마지막 방어선으로 쓴다 |
| D13 | 개인정보는 Party에만 두고 이력에는 ID만. 삭제는 가명화 + trail 정리. trail에는 personal 필드의 값을 남기지 않는다(payday#92) | 불변 이력에 복사된 개인정보는 나중에 지우기 어렵다 |
| D14 | 제품 이력(테넌트별 보존)과 payday trail(배포 단위 보존)을 분리 | trail에는 테넌트별 보존이 없고, 값 스냅샷이 제품 삭제를 무력화하지 않게 한다 |
| D15 | OIDC 로그인은 Phase 0, SAML/SCIM은 Phase 3. 역할은 `gate.Policy`. **로그인 부분은 D26이 대체한다** | 직원 셀프서비스에는 처음부터 SSO가 필요하고, 기업 SSO는 유료 기능이다 |
| D16 | 계측은 Phase 0에 일 단위 스냅샷만, 원장·Entitlement는 과금 직전. **D35가 대체한다** | 원천에서 다시 계산할 수 있으므로 미리 만들 필요가 없다 |
| D17 | 관계 종류는 Phase 2까지 시스템이 정한 값. 전역 엔터티 없음 | 제약을 정적으로 걸 수 있고, 전역 행의 쓰기 권한 문제를 피한다 |
| D18 | 자산 번호는 별도 필드(한글 허용), 처분은 상태, Erase는 오입력 취소용 | payday alias 문법과 맞지 않고, 처분한 자산의 번호를 재사용하지 않는다 |
| D19 | 공간 예약은 캘린더 밖 공간과 장비 중심. 회의실 대체는 비목표 | 회의실은 기존 캘린더가 권위다 |
| D20 | 도메인 레이어는 payday Gate 위에, `seal`은 그보다 위에 둔다 | 도메인 작업의 내부 쓰기까지 Gate의 엣지 확인을 받고, 생성 쓰기는 batch 안에서도 닫힌다 |
| D21 | 클라우드 없이 self-host로 시작한다. 서버 한 대에 Docker Compose, PostgreSQL은 pg_bigm을 넣은 자체 이미지. **PostgreSQL 이미지는 D31 이후 공식 이미지다** | 운영을 직접 통제하고, 확장 지원을 관리형 서비스에 기대지 않는다. 관리형으로 옮길 때의 선택지는 9.5절 표에 남긴다 |
| D22 | 한국어 검색은 pg_bigm, 1글자 질의는 접두 일치. **D31이 대체한다**: 검색은 OpenSearch가 답하고, 데이터베이스의 검색은 인덱스 없이 남는다 | 2음절 이름이 흔하고, pg_trgm은 3글자 미만에 인덱스를 쓰지 못한다 |
| D23 | QR 라벨 도메인은 테넌트마다 설정한다. 바꾼 뒤에도 이전 도메인은 계속 해석한다. ~~확인된 도메인이 없으면 라벨 기능을 끈다~~ — **rove#155가 없앴다(2026-10-22)**: 활성 도메인이 있으면 그것으로, 없으면 서비스 주소로 인쇄한다(D28) | 인쇄한 라벨의 호스트는 바꿀 수 없으므로, 그 결정을 테넌트에게 맡기고 이전 라벨이 깨지지 않게 한다. 도메인이 없을 때의 주소는 D28의 서비스 주소가 있으니 라벨을 끌 이유가 없다 |
| D24 | 생성 동사가 뜻하는 일은 도메인 레이어가 그 동사를 완성하고, 새 RPC는 생성 동사가 이름 붙이지 않는 일에만 만든다 | payday가 권하는 방식이고, "제대로 하기"가 "하기" 옆의 두 번째 이름이 되지 않는다 |
| D25 | 첨부는 저장소 인터페이스 뒤에 두고 로컬 파일시스템으로 시작한다. 서버를 나눌 때 S3 호환 저장소로 옮긴다. MinIO는 쓰지 않는다 | 서버 한 대에서는 별도 저장소가 필요 없다. MinIO 커뮤니티판은 배포를 멈추고 보관 처리되었다 |
| D26 | 사람·테넌트·로그인은 roster의 것이다. 외부 roster를 기본으로 지원하고, 서버 한 대 배포에서는 같은 프로세스에 내장해 `bufconn`으로 붙인다. Rove는 roster가 확인한 사람에게 자기 세션을 주고, Tenant·Holder 행을 roster의 ID에 고정해 처음 들어올 때 만든다. 역할은 Rove가 정한다. **ID를 같게 두는 부분은 D27이 대체한다** | 비밀번호·잠금·2단계 인증·IdP 연결·SCIM·SSO를 제품마다 다시 만들지 않고, 같은 roster를 쓰는 다른 제품(shale)과 사람이 하나다. 내장은 배포를 간편하게 하고, 같은 코드가 리스너만 바꿔 붙으므로 Rove의 나머지는 어느 모드인지 모른다 |
| D27 | 제품군은 SaaS·Dedicated·CE이고 지금은 SaaS만 만든다. 테넌트는 두 층이다. roster의 테넌트는 계정 디렉터리(SaaS는 하나, Dedicated는 고객마다)이고, Rove의 테넌트는 워크스페이스다. Holder는 워크스페이스의 구성원 자격이며 자기 ID와 roster 계정(`account`)을 갖는다 | 워크스페이스마다 roster 테넌트를 두면 가입자가 테넌트를 만드는 길과 테넌트를 넘는 계정이 roster에 필요하다. 워크스페이스를 site나 roster 팀으로 두면 고객 사이의 격리가 payday의 테넌트 경계에서 손으로 짠 정책으로 내려가고, 테넌트 단위인 계약·hold·내보내기·삭제·감사 기록·과금을 다시 만들어야 한다 |
| D28 | SaaS는 SSO만 쓴다. Google·Microsoft·GitHub로 roster의 issuer를 거쳐 앱 호스트 한 곳에서 로그인하고, 2단계 인증은 IdP의 것이다. 워크스페이스는 경로(`/@<alias>`)로 고른다. 가입하면 개인 워크스페이스가 생기고, 소유한 워크스페이스는 N개까지 무료다. 라벨에는 워크스페이스와 무관한 전역 코드를 넣는다. 라벨의 호스트는 워크스페이스에 활성 도메인이 있으면 그것, 없으면 서비스 주소다(rove#155에서 D23과 하나로 합침) | 비밀번호·2단계 인증·계정 복구를 만들고 지키지 않는다. 경로 기반이면 인증서·DNS·쿠키가 호스트 하나로 끝난다. 라벨이 alias와 워크스페이스의 수명에 묶이지 않는다 |
| D29 | 워크스페이스는 닫기 → 복구 기간(기본 30일) → 파기로 닫는다. legal hold는 파기를 멈춘다. 법정 보존은 법이 정한 항목만 분리해 보관한다. alias는 무작위로 시작해 바꿀 수 있고, 예약어가 있으며, 풀린 alias는 보류한다(기본 1년). 기간은 배포 설정과 계약으로 바꾼다 | 실수와 분쟁을 되돌릴 시간을 주고, 남길 의무가 없는 데이터는 남기지 않는다. 예전 주소로 찾아오는 사람을 노린 사칭을 막는다 |
| D30 | 초대는 Rove의 것이고 이메일로만 한다. 초대장은 roster가 확인한 주소를 가진 계정의 것이고, 확인되지 않은 계정은 메일의 1회용 링크로 받는다. 받는 쪽 호출은 구성원 자격이 아닌 계정으로 처리한다. 메일은 SMTP 설정이다 | 초대는 워크스페이스와 역할에 묶여 있어 roster가 모르는 것이다. 확인되지 않은 주소로 맞추면 남의 초대를 가져갈 수 있다(nOAuth). 받는 사람은 아직 구성원이 아니라 테넌트 경계가 그 행을 보여 주지 않는다. 메일 서비스는 정하지 않았다 |
| D31 | 자산 검색은 OpenSearch와 한국어 분석기 nori가 답한다. 설정에 클러스터가 없으면 데이터베이스가 답하고(D22), 그 검색은 더 키우지 않는다. 색인은 DB의 사본이다. 바뀐 것은 쓰기와 같은 트랜잭션에 남긴 행을 백그라운드 루프가 보내고, 검색은 ID만 받아 벽을 거쳐 행을 읽는다 | 붙여 쓴 합성어, 조사, 오타를 형태소 분석이 처리하고, 일부만 맞은 결과는 드문 낱말이 맞은 것을 앞에 둔다. 합성 자산 100만 건과 노드 셋으로 Typesense와 비교했다(rove#83) |
| D32 | 비서(MCP)는 터미널 명령 `rove mcp`로 붙고, `rove login`한 사람으로 RPC를 불러 읽는다. 도구는 읽기만 한다. 쓰는 도구와 원격 MCP(OAuth 2.1)는 나중이다 | 권한 표와 벽이 브라우저와 같아서 새로 지킬 것이 없고, 서버에 새 입구나 인증을 열지 않는다. 쓰기는 사람이 확인하는 흐름이 먼저다(rove#91) |
| D33 | 비서의 쓰기는 미리보기와 적용의 두 단계다. 미리보기는 바뀔 것을 이름으로 적고, 적용은 그 문장을 인자로 받는다. 이력에는 행위자만 남는다. 구성원과 매니저의 작업을 연다. 비서를 좁히는 것은 터미널 로그인의 allow이고, 세션이 그 목록만 허락한다 | 사람이 무엇을 승인하는지 이름으로 보고, 클라이언트의 승인 창에도 그것이 보인다. 무엇을 할 수 있는지는 이미 권한 표가 정하므로, 더 좁히는 것은 사람이 자기 터미널에 고르게 한다(rove#94) |
| D34 | 가져오기는 복구와 신규 대량 등록 둘이다. 시트로 고치는 가져오기는 없애고, 온보딩·외부 동기화는 외부 도구 연동으로, 일괄 수정은 나중으로 미룬다. 신규 대량 등록은 rove 양식으로 새 자산만 만들고, API는 도메인이 완성한 `AddMany`다. 워크스페이스 내보내기는 형식 버전과 배포의 서명을 갖고, 복구는 서명을 검증해 빈 새 워크스페이스에 ID를 지켜 넣는다. 여러 대상에 걸친 작업은 부모 Event 하나와 대상마다 자식 Event를 남긴다 | 태그로 맞춰 고치면 사람이 바꾸는 값이 정체성이 되고(태그가 같은 다른 물건을 고친다), 빈 칸의 뜻이 모호하며, 행마다 작업을 반복해 느리다. 목적마다 맞는 입력이 다르다. 복구할 파일을 손으로 고칠 수 있으면 이력을 위조할 수 있다. 자산의 이력에는 그 자산에 일어난 일이 그 자산의 Event로 있어야 한다 |
| D35 | 청구할 숫자는 Rove 밖의 작업이 원천(DB 읽기 복제본, 첨부 저장소의 목록)에서 계산한다. Rove는 계량을 위한 행을 쓰지 않는다. `UsageSnapshot`은 지웠고 도메인 번호 20은 비워 둔다. 미터는 보존 작업과 파기가 원천을 지우기 전에 잰다(7장) | 원천에서 다시 계산할 수 있는 값을 한 벌 더 두면 어긋날 수 있는 사본만 늘어난다. 모니터링 메트릭은 빠짐과 겹침을 알 수 없고 다시 계산할 수 없다 |
| D36 | 보존 기간은 워크스페이스의 설정이다. 이력(`keep_days`), 접속기록(`access_keep_days`), 닫은 뒤 복구 기간(`restore_days`)을 소유자가 `TenantService.Configure`로 정한다. 줄이면 배포의 유예(`app.retention.grace`)가 지난 뒤에 적용하고, 저장하기 전에 무엇이 언제 지워지는지 말한다(`TenantService.Preview`). 조회 창은 없앴다. 계약에는 운영자의 몫(`purge_floors`, 이름, 효력일)만 남는다 | 소유자의 모델은 사용량 과금과 고객이 정하는 수명 주기다(S3처럼). 보관하는데 보이지 않는 이력은 고객이 볼 수 없는 것에 값을 치르게 하고, 이력을 읽는 모든 곳에 창을 지키는 코드를 둬야 했다 |
| D37 | 동의 기록은 roster의 동의 원장(`ConsentService`)에 둔다. Rove는 어느 문서의 어느 버전이 시행 중인지만 정하고, 계정이 동의했는지는 roster에 묻는다. Rove의 `Consent`(도메인 44는 비워 둔다)와 그 보존 sweep, `app.consent.keep`은 지웠다. 계정이 삭제된 뒤의 보존 기간은 roster의 `consent.keep`이다(내장 roster는 `auth.roster.consent_keep`). 이전 기록은 옮기지 않고, 계정이 한 번 더 동의한다 | 같은 동의를 두 곳에 두면 어긋날 수 있는 사본이 하나 늘고 지울 곳도 하나 는다. 동의는 계정의 것이고 계정은 roster의 것이라, 계정이 삭제된 뒤의 보존과 파기도 roster가 한다. 옮겨 적으면 roster가 보지 않은 동의를 roster가 받은 지금의 시각으로 적게 된다. 출시 전이라 다시 동의받는 비용이 없다 |
