# 관측: 무엇을 보고 무엇에 경보를 거나 (운영자)

Loft가 내보내는 것과, 그중 무엇을 대시보드에 두고 무엇에 경보를 거는지입니다. 경보와 대시보드를 무엇으로 만들지(Grafana, ClickHouse, Loki 등)는 배포가 정합니다(loft#38). 여기에는 무엇을 볼지와 그 기준만 적습니다.

## 무엇이 나가나

`otel:`(`deploy/k8s/config.yaml`)이 OTLP로 수집기에 보내는 것입니다. 서버(`loft serve`)와 운영자 진입점(`loft-admin serve`)이 같은 설정으로 보내고, 둘 다 `service.name`이 `loft`입니다.

| 신호 | 내용 |
| --- | --- |
| 로그 | 요청마다 `gRPC in`과 `gRPC out` 두 줄. 속성은 `rpc.service`, `rpc.method`, `rpc.grpc.status`, `server.elapsed_ns`이고, 인증된 요청은 `authenticated`(`actor.id`, `actor.tenant`)도 남깁니다. 그 밖에 아래 표의 경고 |
| 추적 | 요청마다 스팬 하나와 그 안의 것. 예시 설정은 10%만 보냅니다(`sampler`) |
| 지표 | `rpc.server.call.duration`(초 단위 히스토그램, 메서드별). 60초마다 보냅니다. 감사 기록의 증인 `log:`를 둔 배포는 체크포인트의 번호 `payday.trail.checkpoint`(게이지)도 보냅니다 |

- 로그에 사람의 이름, 이메일, 전화번호는 남기지 않습니다. 남는 것은 식별자(`actor.id`)입니다.
- 내장 roster 호출도 같은 서버의 요청으로 남습니다(`rpc.service`가 `roster.…`, 주소는 `bufconn`). 요청 수를 셀 때는 빼고 셉니다.

## 경보

**로그 문구는 그대로 찾습니다.** 문구를 바꾸는 변경은 이 표도 함께 바꿉니다.

| 무엇이 보이면 | 뜻 | 할 일 |
| --- | --- | --- |
| `` `trail verify` ``(백틱까지)를 담은 경고 | 감사 기록 보관소가 DB의 기록이나 체크포인트와 맞지 않음 | [감사 기록 위변조 감지 대응](trail-integrity.md) |
| 게이지 `payday.trail.checkpoint`가 지난 30일의 최댓값보다 작음 | 감사 기록의 체크포인트 번호를 누가 되감음(증인 `log:`) | [체크포인트의 증인](trail-integrity.md#체크포인트의-증인)의 `unkept`, `rewritten` |
| `trail: a checkpoint, heard`에 같은 `k`가 서로 다른 `digest`로 두 줄 | 같은 번호의 체크포인트를 다시 씀(증인 `log:`) | 같은 곳 |
| 게이지 `payday.trail.checkpoint`가 패스 두 번(`audit.every`의 두 배)보다 오래 그대로. 패스마다 체크포인트를 남기는 배포만 | 증인이 듣지 못하게 막힘 | 같은 곳의 `unheard` |
| `trail: a witness did not hear a checkpoint, and the next pass tells it again` | 증인 하나가 체크포인트를 듣지 못함. 작업은 계속하고 다음 패스가 다시 알려 줌 | 그 증인의 볼륨·디스크·권한을 봅니다. 이어지면 `verify`가 `unheard`를 찾습니다 |
| `spin failed` | 백그라운드 루프 하나가 오류로 끝나서 **프로세스가 멈춤**. 서버가 함께 내려가고 쿠버네티스가 다시 띄웁니다(재시작 횟수가 늘어남). `spin=`이 어느 루프인지, `error=`가 왜인지 말합니다 | 원인을 고칩니다. 고치지 않으면 다시 떠도 같은 자리에서 또 멈춥니다 |
| `tidy`(경고, `spin=`과 `error=`가 붙음) | 주기 작업(세션·로그인 정리, 워크스페이스 파기, 계정 대조 등) 한 번이 실패함. 프로세스는 그대로이고 다음 주기에 다시 합니다 | 같은 `spin=`으로 이어지면 원인을 봅니다 |
| `spin lost its lease`가 이어짐, `spinpg: the lease's database cannot be asked` | DB에 닿지 않아 백그라운드 작업이 멈춤 | DB와 그 연결(NetworkPolicy, 비밀번호)을 봅니다 |
| `roster: … Unavailable`, `session: a sign-in nobody could check`, 요청이 `Unavailable`로 끝남(`rpc.grpc.status`, 화면에는 503 `cannot say just now`) | 계정을 물을 roster에 닿지 않아, 로그인한 요청을 받지 못함 | 외부 roster면 그쪽을 봅니다. 내장 roster인 #100 이전 빌드면 프로세스를 다시 띄웁니다([백업과 복구](restore.md)의 "알려진 것") |
| `search: the index is not kept current; what changed waits` | 검색 클러스터에 닿지 않음. 검색은 "지금은 검색할 수 없습니다"로 답함. 1분에 한 번씩 남습니다 | 검색 클러스터와 Loft의 사용자·role을 봅니다. 돌아오면 쌓인 것을 따라잡습니다 |
| `mail: a notification was given up on after every try`, `invitation: the mail did not go` | 메일이 나가지 않음 | SMTP 서버와 `app.mail`을 봅니다 |
| `csp: a page did what the policy does not allow` | 화면이 콘텐츠 보안 정책을 어김. `report-only`라 막지는 않았음 | 무엇을 어겼는지 봅니다. 조용해야 `enforce`로 바꿀 수 있습니다(README의 "콘텐츠 보안 정책") |
| `admin: a request without an operator's key` | 운영자 진입점에 키 없는 요청 | 누가 port-forward를 열었는지 봅니다 |
| `lifecycle: a workspace's last owner left, …`, `lifecycle: only auditors are left …` | 돌볼 사람이 없는 워크스페이스 | 그 워크스페이스에 소유자를 정합니다 |

**지표와 요청 로그로:**

| 기준 | 뜻 |
| --- | --- |
| `rpc.server.call.duration`의 p95: 단건 조회·목록(`Get`, `List`) 300ms 초과, `AssetService/QueryAt` 1초 초과(5분 동안) | design 1.2절의 목표를 넘음 |
| `rpc.grpc.status`가 `Internal`, `Unknown`, `Unavailable`, `DeadlineExceeded`인 요청이 전체의 1% 초과(5분 동안) | 서버 쪽 실패 |
| `DeadlineExceeded`가 `AssetService/Import`에서 | 큰 시트가 호출 제한(`server.timeout`)에 걸림(loft#103) |

**Loft 밖에서 보는 것.** Loft는 이것들을 로그로 남기지 않으므로, 클러스터와 노드 쪽 지표로 봅니다.

| 기준 | 어디서 |
| --- | --- |
| WAL 보관이 실패함, 마지막 베이스 백업이 26시간보다 오래됨 | CNPG의 `ContinuousArchiving` 조건, `pg_stat_archiver.failed_count`, `Backup` 상태 |
| 볼륨 복사 Job이 실패함 | Kubernetes Job 상태 |
| 인증서가 14일 안에 만료됨 | cert-manager의 `certmanager_certificate_expiration_timestamp_seconds` |
| 노드 디스크 사용률 80% 초과 | 노드 지표(hostmetrics 등). 볼륨이 노드 디스크라면 그 사용률이 곧 볼륨의 것입니다 |
| 파드가 다시 시작되거나 준비되지 않음 | Kubernetes 파드 상태 |

## 백그라운드 작업이 어디서 도나

`spin.lease: postgres`이면 루프마다 PostgreSQL의 advisory lock(리스)을 쥔 프로세스 하나에서만 돕니다. 지금 누가 쥐고 있는지는 DB가 말합니다(payday `spin/spinpg`).

```sql
SELECT a.pid, a.client_addr, a.backend_start, count(*) AS leases
FROM pg_locks l JOIN pg_stat_activity a USING (pid)
WHERE l.locktype = 'advisory' AND l.classid = 1885631344 AND l.objsubid = 2 AND l.granted
GROUP BY 1, 2, 3;
```

- 리스는 프로세스마다 연결 하나(`application_name = payday.spin`)에 모입니다. `client_addr`은 그 파드의 주소입니다(서버와 운영자 진입점은 한 파드라 주소가 같습니다).
- `leases`는 그 프로세스가 돌리는 루프의 수입니다. 행이 하나도 없으면 아무 루프도 돌지 않는 것입니다. DB가 돌아오기를 기다리는 동안이 그렇습니다(`spin lost its lease`).
- 행이 둘이면 루프가 두 프로세스에 나뉘어 있습니다. 장애 조치 직후의 잠깐을 빼면 같은 루프가 두 곳에서 돌지는 않습니다.

## 대시보드

- 요청 수, 오류율, p95 지연. 메서드별로 봅니다(`rpc.service`, `rpc.method`).
- 백그라운드 작업: 위 표의 경고 수.
- 백업: 마지막 베이스 백업 시각, WAL 보관 실패 수, 볼륨 복사 Job.
- 검색: `loft search status`가 말하는 색인의 문서 수와 DB의 행 수. 대시보드에는 경고 수를 둡니다.

## 예: Loki

로그가 Loki에 가면(`service_name="loft"`) 이렇게 셉니다.

```logql
# 감사 기록 경보
sum(count_over_time({service_name="loft"} |= "`trail verify`" [5m])) > 0

# 백그라운드 작업 실패
sum(count_over_time({service_name="loft"} |= "spin failed" [15m])) > 0

# 서버 쪽 실패 비율 (roster 호출은 뺌)
sum(count_over_time({service_name="loft"} |= "gRPC out" | rpc_service!~"roster.*" | rpc_grpc_status=~"Internal|Unknown|Unavailable|DeadlineExceeded" [5m]))
  / sum(count_over_time({service_name="loft"} |= "gRPC out" | rpc_service!~"roster.*" [5m])) > 0.01
```

`rpc_service` 같은 로그 속성은 Loki의 색인 라벨이 아니라 구조화된 메타데이터로 들어가므로, `{…}` 안이 아니라 `|` 뒤에서 거릅니다.
