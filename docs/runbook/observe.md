# 관측: 무엇을 보고 무엇에 경보를 거나 (운영자)

Rove가 내보내는 것과, 그중 무엇을 대시보드에 두고 무엇에 경보를 거는지입니다. 경보와 대시보드를 무엇으로 만들지(Grafana, ClickHouse, Loki 등)는 배포가 정합니다(rove#38). 여기에는 무엇을 볼지와 그 기준만 적습니다.

## 무엇이 나가나

`otel:`(`deploy/k8s/config.yaml`)이 OTLP로 수집기에 보내는 것입니다. 서버(`rove serve`)와 운영자 진입점(`rove-admin serve`)이 같은 설정으로 보내고, 둘 다 `service.name`이 `rove`입니다.

| 신호 | 내용 |
| --- | --- |
| 로그 | 요청마다 `gRPC in`과 `gRPC out` 두 줄. 속성은 `rpc.service`, `rpc.method`, `rpc.grpc.status`, `server.elapsed_ns`이고, 인증된 요청은 `authenticated`(`actor.id`, `actor.tenant`)도 남깁니다. 그 밖에 아래 표의 경고 |
| 추적 | 요청마다 스팬 하나와 그 안의 것. 예시 설정은 10%만 보냅니다(`sampler`) |
| 지표 | `rpc.server.call.duration`(초 단위 히스토그램, 메서드별). 60초마다 보냅니다 |

- 로그에 사람의 이름, 이메일, 전화번호는 남기지 않습니다. 남는 것은 식별자(`actor.id`)입니다.
- 내장 roster 호출도 같은 서버의 요청으로 남습니다(`rpc.service`가 `roster.…`, 주소는 `bufconn`). 요청 수를 셀 때는 빼고 셉니다.

## 경보

**로그 문구는 그대로 찾습니다.** 문구를 바꾸는 변경은 이 표도 함께 바꿉니다.

| 무엇이 보이면 | 뜻 | 할 일 |
| --- | --- | --- |
| `` `trail verify` ``(백틱까지)를 담은 경고 | 감사 기록 보관소가 DB의 기록이나 체크포인트와 맞지 않음 | [감사 기록 위변조 감지 대응](trail-integrity.md) |
| `spin failed` | 백그라운드 작업(보존 패스, 정리, 메일 등)이 오류로 끝남. `spin=`이 어느 작업인지 말합니다 | 오류를 보고 원인을 고칩니다. 다음 주기에 다시 돕니다 |
| `spin lost its lease`가 이어짐, `spinpg: the lease's database cannot be asked` | DB에 닿지 않아 백그라운드 작업이 멈춤 | DB와 그 연결(NetworkPolicy, 비밀번호)을 봅니다 |
| `roster: … Unavailable`, `session: a sign-in nobody could check`, 요청이 `Unavailable`로 끝남(`rpc.grpc.status`, 화면에는 503 `cannot say just now`) | 계정을 물을 roster에 닿지 않아, 로그인한 요청을 받지 못함 | 외부 roster면 그쪽을 봅니다. 내장 roster인 #100 이전 빌드면 프로세스를 다시 띄웁니다([백업과 복구](restore.md)의 "알려진 것") |
| `search: the index is not kept current; what changed waits` | 검색 클러스터에 닿지 않음. 검색은 "지금은 검색할 수 없습니다"로 답함. 1분에 한 번씩 남습니다 | 검색 클러스터와 Rove의 사용자·role을 봅니다. 돌아오면 쌓인 것을 따라잡습니다 |
| `mail: a notification was given up on after every try`, `invitation: the mail did not go` | 메일이 나가지 않음 | SMTP 서버와 `app.mail`을 봅니다 |
| `csp: a page did what the policy does not allow` | 화면이 콘텐츠 보안 정책을 어김. `report-only`라 막지는 않았음 | 무엇을 어겼는지 봅니다. 조용해야 `enforce`로 바꿀 수 있습니다(README의 "콘텐츠 보안 정책") |
| `admin: a request without an operator's key` | 운영자 진입점에 키 없는 요청 | 누가 port-forward를 열었는지 봅니다 |
| `lifecycle: a workspace's last owner left, …`, `lifecycle: only auditors are left …` | 돌볼 사람이 없는 워크스페이스 | 그 워크스페이스에 소유자를 정합니다 |

**지표와 요청 로그로:**

| 기준 | 뜻 |
| --- | --- |
| `rpc.server.call.duration`의 p95: 단건 조회·목록(`Get`, `List`) 300ms 초과, `AssetService/QueryAt` 1초 초과(5분 동안) | design 1.2절의 목표를 넘음 |
| `rpc.grpc.status`가 `Internal`, `Unknown`, `Unavailable`, `DeadlineExceeded`인 요청이 전체의 1% 초과(5분 동안) | 서버 쪽 실패 |
| `DeadlineExceeded`가 `AssetService/Import`에서 | 큰 시트가 호출 제한(`server.timeout`)에 걸림(rove#103) |

**Rove 밖에서 보는 것.** Rove는 이것들을 로그로 남기지 않으므로, 클러스터와 노드 쪽 지표로 봅니다.

| 기준 | 어디서 |
| --- | --- |
| WAL 보관이 실패함, 마지막 베이스 백업이 26시간보다 오래됨 | CNPG의 `ContinuousArchiving` 조건, `pg_stat_archiver.failed_count`, `Backup` 상태 |
| 볼륨 복사 Job이 실패함 | Kubernetes Job 상태 |
| 인증서가 14일 안에 만료됨 | cert-manager의 `certmanager_certificate_expiration_timestamp_seconds` |
| 노드 디스크 사용률 80% 초과 | 노드 지표(hostmetrics 등). 볼륨이 노드 디스크라면 그 사용률이 곧 볼륨의 것입니다 |
| 파드가 다시 시작되거나 준비되지 않음 | Kubernetes 파드 상태 |

## 대시보드

- 요청 수, 오류율, p95 지연. 메서드별로 봅니다(`rpc.service`, `rpc.method`).
- 백그라운드 작업: 위 표의 경고 수.
- 백업: 마지막 베이스 백업 시각, WAL 보관 실패 수, 볼륨 복사 Job.
- 검색: `rove search status`가 말하는 색인의 문서 수와 DB의 행 수. 대시보드에는 경고 수를 둡니다.

## 예: Loki

로그가 Loki에 가면(`service_name="rove"`) 이렇게 셉니다.

```logql
# 감사 기록 경보
sum(count_over_time({service_name="rove"} |= "`trail verify`" [5m])) > 0

# 백그라운드 작업 실패
sum(count_over_time({service_name="rove"} |= "spin failed" [15m])) > 0

# 서버 쪽 실패 비율 (roster 호출은 뺌)
sum(count_over_time({service_name="rove"} |= "gRPC out" | rpc_service!~"roster.*" | rpc_grpc_status=~"Internal|Unknown|Unavailable|DeadlineExceeded" [5m]))
  / sum(count_over_time({service_name="rove"} |= "gRPC out" | rpc_service!~"roster.*" [5m])) > 0.01
```

`rpc_service` 같은 로그 속성은 Loki의 색인 라벨이 아니라 구조화된 메타데이터로 들어가므로, `{…}` 안이 아니라 `|` 뒤에서 거릅니다.
