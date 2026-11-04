# 관측: Grafana와 대시보드, 로그·지표를 쌓는 곳 (운영자)

Loft가 내보낸 것과 플랫폼이 말하는 것을 ClickHouse에 쌓고 Grafana로 보는 구성입니다(loft#38). 데이터는 OpenTelemetry Collector의 `clickhouse` 내보내기가 쓰는 표준 테이블에 있습니다. 경보는 아직 없습니다. 무엇에 걸지는 [`docs/runbook/observe.md`](../../docs/runbook/observe.md)에 있습니다.

쌓는 곳(ClickHouse)과 노드마다 수집기는 **선택**입니다. 자기 ClickHouse와 수집기가 있는 배포는 둘을 빼고 Grafana와 대시보드만 씁니다(아래 "자기 저장소가 있는 배포").

```
Loft·levyan (otel:) ─ OTLP ─┐
                            ├─> 노드 수집기 (node/, 노드마다) ─> ClickHouse (clickhouse/) ─> Grafana
클러스터 수집기 ──── OTLP ──┘
  (CNPG, cert-manager, 쿠버네티스)
```

## 무엇이 있나

| | |
| --- | --- |
| `grafana.yaml` | Grafana 하나와 그 볼륨, Service, NetworkPolicy. 데이터 소스와 대시보드는 시작할 때마다 파일에서 읽습니다(화면에서 바꾼 것은 남지 않음) |
| `datasources.yaml` | 데이터 소스. 기본은 `clickhouse/`의 ClickHouse(`clickhouse:9000`, 읽기 계정 `grafana`). 자기 ClickHouse가 있으면 오버레이가 바꿉니다 |
| `dashboards/` | `gen.py`가 쓰는 대시보드 둘: **Loft — 서비스**, **Loft — 플랫폼** |
| `collector.yaml` | 클러스터 안 수집기. CNPG와 cert-manager의 지표를 긁고, 쿠버네티스의 상태(재시작, Job, 파드)를 읽어 OTLP로 그 노드의 수집기(`HOST_IP:4317`)에 보냅니다 |
| `cnpg-monitoring.yaml` | DB 쪽: CNPG의 지표에 Loft의 것을 더하는 쿼리(리스를 쥔 루프 수, 검색 대기열). DB의 namespace에 둡니다 |
| `clickhouse/` (선택) | ClickHouse 하나와 그 볼륨, Service, NetworkPolicy. Grafana와 같은 namespace에 둡니다 |
| `node/` (선택) | 노드마다 수집기(DaemonSet). 노드의 4317·4318에서 OTLP를 받아 ClickHouse에 씁니다. namespace를 따로 둡니다 |

## ClickHouse (`clickhouse/`, 선택)

- **이미지:** `clickhouse/clickhouse-server` 26.8(장기 지원판), 셸이 없는 distroless. digest로 고정합니다. root가 아니고 권한 없이(`restricted`) 돕니다.
- **크기:** 메모리 요청 512 MiB, 한도 1.5 GiB. 4 GB 노드에 다른 것과 함께 둘 수 있는 크기입니다.
  - 서버는 한도의 3/4(약 1.1 GiB)까지만 쓰고, 그 위로 가는 쿼리는 거절합니다(`config.xml`).
  - 캐시는 작게 둡니다(마크 캐시 128 MiB 등). 이미지의 기본값은 수십 GB 메모리의 기계에 맞춘 것입니다.
  - 쿼리 하나는 512 MiB까지입니다. Grafana의 쿼리는 스레드 4개, 60초까지입니다.
  - 노드 하나에서 30일 동안 쌓을 디스크는 PVC `data-clickhouse-0`(20 GiB)로 잡았습니다. local-path는 이 크기를 지키지 않으므로, 디스크를 묶는 것은 아래 TTL입니다.
- **계정**(`users.xml`):

  | 계정 | 무엇을 | 비밀번호 |
  | --- | --- | --- |
  | `otel` | 노드 수집기. `otel` 데이터베이스와 그 테이블을 만들고 씁니다(`otel.*`에 CREATE DATABASE·TABLE·VIEW, INSERT, SELECT) | Secret `clickhouse`의 `otel-password` |
  | `grafana` | 읽기만 합니다(`readonly=2`, `otel.*`에 SELECT) | Secret `grafana`의 `clickhouse-password`. Grafana가 쓰는 것과 같은 값입니다 |
  | `default` | 파드 안에서만(localhost). 운영자가 `kubectl exec`로 `clickhouse client`를 열 때 씁니다 | 없음 |

- **테이블:** 노드 수집기가 처음 뜰 때 만듭니다(`create_schema`). 이름은 수집기의 기본값입니다.
  - `otel_logs`, `otel_traces`(추적 ID로 찾는 `otel_traces_trace_id_ts`와 그것을 채우는 뷰), `otel_metrics_gauge`, `otel_metrics_sum`, `otel_metrics_histogram`, `otel_metrics_exponential_histogram`, `otel_metrics_summary`.
  - 로그·추적의 속성은 JSON입니다(`LogAttributes.rpc.service`). 지표의 속성은 Map입니다(`Attributes['namespace']`).
- **보관(TTL): 30일.** 행은 날짜별 파티션에 들어가고, 30일이 지난 날의 파티션이 통째로 지워집니다.
  - 30일인 까닭: [런북](../../docs/runbook/observe.md)의 경보 중 가장 멀리 보는 것이 30일입니다(체크포인트 번호가 지난 30일의 최댓값보다 작아짐). 요청이 거의 없는 스테이징에서 하루 약 65 MB(대부분 지표), 30일이면 2 GB 남짓입니다.
  - TTL은 수집기의 `ttl`(`node/config.yaml`)이 테이블을 **만들 때** 씁니다. 이미 있는 테이블은 바꾸지 않으므로, 바꿀 때는 수집기 설정과 함께 테이블마다 고칩니다.

    ```sql
    -- 로그·추적: Timestamp, 지표: TimeUnix, otel_traces_trace_id_ts: Start
    ALTER TABLE otel.otel_logs MODIFY TTL toDateTime(Timestamp) + toIntervalDay(14);
    ALTER TABLE otel.otel_metrics_gauge MODIFY TTL toDateTime(TimeUnix) + toIntervalDay(14);
    ```

  - ClickHouse 자신의 기록은 쿼리·파트 기록 7일, 비동기 insert 기록 3일입니다. 프로파일러와 서버 지표의 기록처럼 작은 노드에서 가장 많이 쓰는 것은 끕니다.
- **노드:** 컨트롤 플레인이 아닌 노드를 고릅니다(선호). local-path 볼륨은 처음 뜬 노드에 묶이므로, 오버레이가 노드를 정합니다(`nodeSelector`).
- **NetworkPolicy:** Grafana(같은 namespace)와 노드 수집기(namespace `observe-node`)만 9000에 닿습니다. 클러스터 수집기는 ClickHouse에 직접 쓰지 않습니다.
- **백업은 없습니다.** 지난 30일의 원격 측정이라 사본도 복원도 두지 않습니다. 잃으면 빈 채로 다시 쌓습니다.

## 노드마다 수집기 (`node/`, 선택)

- **이미지:** `otelcol-contrib` 0.164.0, digest 고정. 클러스터 수집기와 같은 것입니다.
- **어디서:** DaemonSet이라 노드마다 하나입니다. 컨트롤 플레인을 포함해 파드가 도는 모든 노드에 둡니다.
- **받는 것:** OTLP gRPC 4317, HTTP 4318. 노드의 포트(hostPort)라서, 파드는 `HOST_IP:4317`(deploy/k8s의 `config.yaml`)로 보내면 자기 노드의 수집기에 닿습니다.
- **노드의 것:** `host_metrics`가 30초마다 노드의 CPU·메모리·디스크 사용률을 읽습니다. 노드의 `/`를 읽기 전용으로 `/hostfs`에 붙여 읽습니다. 지표에 `host.name`(노드 이름)을 붙이고, 대시보드는 이것으로 노드를 나눕니다.
- **처리:** `memory_limiter`(200 MiB. 컨테이너 한도는 256 MiB), `batch`(5,000개 또는 5초)를 거쳐 ClickHouse에 씁니다. ClickHouse가 잠깐 닿지 않으면 5분까지 다시 시도합니다.
- **시작할 때** ClickHouse에 닿아야 합니다(테이블을 확인함). 닿지 않으면 다시 시작하면서 기다리고, 그동안 그 노드의 파드는 보내지 못합니다.
- **namespace:** hostPort와 노드 파일 읽기(hostPath)는 Pod Security `privileged`에서만 됩니다. 그래서 수집기만 두는 namespace를 따로 만듭니다(예: `observe-node`). 컨테이너 자체는 root가 아니고, 권한(capability)이 없고, 노드의 파일은 읽기만 합니다.
- **NetworkPolicy:** 클러스터 안의 파드가 보낸 것만 받습니다. hostPort는 노드가 붙은 모든 네트워크에 열리지만, 클러스터 밖에서 온 것은 받지 않습니다. 오버레이가 보내는 namespace로 좁힙니다.
- **앱 쪽 NetworkPolicy:** 앱이 나가는 것(Egress)을 막는다면, 노드 주소의 4317이 아니라 수집기 파드(namespace `observe-node`, `app.kubernetes.io/name: otel-node-collector`)의 4317로 나가는 길을 엽니다. `HOST_IP:4317`로 보내도 정책은 노드가 넘겨준 뒤의 주소, 즉 수집기 파드를 봅니다(k3s의 kube-router에서 확인). deploy/k8s의 정책은 들어오는 것만 막으므로 할 것이 없습니다.

## 대시보드

- **Loft — 서비스:**
  - 요청 로그(`gRPC out`)에서 초당 요청, 서버 쪽 실패 비율, p95 지연을 봅니다. 메서드별 요청·지연·상태와 요약 표도 있습니다.
  - 백그라운드 작업: 경고·오류 로그(내용별 수와 최근 100줄), 리스를 쥔 루프의 수, 검색 대기열입니다.
- **Loft — 플랫폼:**
  - 백업: 마지막 베이스 백업과 WAL 보관의 경과, 보관 실패, 복구할 수 있는 가장 이른 시점
  - DB: 크기, 연결, 커밋
  - 인증서: 만료까지 남은 날
  - 쿠버네티스: 재시작한 컨테이너, Running이 아닌 파드, 실패한 Job
  - 노드: 디스크·메모리·CPU. 노드 수집기의 `host_metrics`에서 오고, `ResourceAttributes['host.name']`으로 노드를 나눕니다
- 테이블은 ClickHouse 노드 하나의 것을 읽습니다(`otel.otel_logs` 등). 데이터베이스 이름은 변수 `db`(기본 `otel`)입니다.
- 고칠 때는 `gen.py`를 고치고 다시 씁니다: `python3 deploy/observe/dashboards/gen.py deploy/observe/dashboards`. JSON을 직접 고치지 않습니다.

## 오버레이가 가져올 것

오버레이는 둘입니다. Grafana와 ClickHouse의 것, 노드 수집기의 것. 노드 수집기는 namespace가 달라 한 오버레이에 넣지 못합니다.

```yaml
# observe/kustomization.yaml
resources:
  - github.com/lesomnus/loft//deploy/observe?ref=<커밋>
  - github.com/lesomnus/loft//deploy/observe/clickhouse?ref=<같은 커밋>
  - ./ingress.yaml
namespace: observe
patches:
  - target: { kind: Deployment, name: grafana }
    patch: |-
      - op: replace
        path: /spec/template/spec/containers/0/env/0/value
        value: https://grafana.example.com/
  # local-path면 ClickHouse의 볼륨이 있을 노드
  - target: { kind: StatefulSet, name: clickhouse }
    patch: |-
      - op: add
        path: /spec/template/spec/nodeSelector
        value: { kubernetes.io/hostname: worker-1 }
```

```yaml
# observe-node/kustomization.yaml
resources:
  - ./namespace.yaml
  - github.com/lesomnus/loft//deploy/observe/node?ref=<같은 커밋>
namespace: observe-node
```

```yaml
# observe-node/namespace.yaml
apiVersion: v1
kind: Namespace
metadata:
  name: observe-node
  labels:
    pod-security.kubernetes.io/enforce: privileged
```

1. **namespace.** Grafana와 ClickHouse의 것(`observe`)은 `restricted` Pod Security를 통과합니다. 노드 수집기의 것(`observe-node`)은 `privileged`입니다.
   - 다른 이름을 쓰면 두 곳을 고칩니다: 노드 수집기의 `CLICKHOUSE_ENDPOINT`(`tcp://clickhouse.<namespace>.svc:9000`)와 ClickHouse NetworkPolicy의 `namespaceSelector`.
2. **Secret.** `apply -k` 전에 넣습니다. 비밀번호는 줄바꿈 없이 씁니다.

   | namespace | Secret | 키 | 내용 |
   | --- | --- | --- | --- |
   | `observe` | `grafana` | `admin-password` | Grafana의 `admin` 비밀번호 |
   | | | `clickhouse-password` | ClickHouse 읽기 계정(`grafana`)의 비밀번호 |
   | `observe` | `clickhouse` | `otel-password` | ClickHouse 쓰기 계정(`otel`)의 비밀번호 |
   | `observe-node` | `otel-node-collector` | `clickhouse-password` | 같은 쓰기 계정의 비밀번호(`otel-password`와 같은 값) |

3. **주소:** `GF_SERVER_ROOT_URL`을 사람들이 여는 주소로 바꿉니다. 인그레스와 TLS도 정합니다.
4. **NetworkPolicy 좁히기:**
   - `grafana`의 `from`을 인그레스 컨트롤러로 좁힙니다(deploy/k8s의 README 5와 같음).
   - `otel-node-collector`의 `from`을 보내는 namespace(Loft, roster, levyan, 그리고 클러스터 수집기의 `observe`)로 좁힙니다.
5. **클러스터 이름:** 클러스터 수집기의 `CLUSTER_NAME`. 지표에 `k8s.cluster.name`으로 붙습니다.
6. **DB 쪽:**
   - `cnpg-monitoring.yaml`을 DB의 namespace에 적용하고, CNPG `Cluster`의 `spec.monitoring.customQueriesConfigMap`에 넣습니다. DB를 다시 시작하지 않습니다.
   - DB의 NetworkPolicy가 클러스터 수집기에서 오는 9187(지표)을 받게 합니다.
7. **인터넷:** Grafana가 시작할 때 ClickHouse 플러그인을 grafana.com에서 받습니다(`GF_PLUGINS_PREINSTALL_SYNC`). 닫힌 망이면 플러그인을 이미지에 넣습니다.

cert-manager가 없는 클러스터면 `cert-manager` 긁기는 실패만 남기므로, 오버레이에서 그 `scrape_configs` 항목을 뺍니다.

## 자기 저장소가 있는 배포

ClickHouse와 노드마다 수집기를 이미 운영하면 `clickhouse/`와 `node/`를 넣지 않습니다.

- `datasources.yaml`을 자기 ClickHouse의 주소와 읽기 계정으로 바꿉니다.

  ```yaml
  configMapGenerator:
    - name: grafana-datasources
      behavior: replace
      files:
        - datasources.yaml
  ```

- 그 ClickHouse의 `otel` 데이터베이스에 수집기의 `clickhouse` 내보내기가 쓰는 테이블이 있어야 합니다. 로그·추적은 `json: true`로 씁니다. 대시보드가 속성을 JSON 경로로 읽기 때문입니다.
- 노드 패널은 `ResourceAttributes['host.name']`으로 노드를 나눕니다. 노드의 수집기가 `host.name`을 붙이게 합니다(`resourcedetection`의 `system` 등).
- ClickHouse가 여러 노드의 클러스터면 분산 테이블을 읽게 대시보드를 다시 씁니다: `python3 gen.py <디렉터리> --suffix _dist`. 그 파일로 오버레이에서 `grafana-dashboards`를 바꿉니다.
- 클러스터 수집기는 그대로 `HOST_IP:4317`로 보냅니다. 노드마다 수집기가 없으면 `OTLP_ENDPOINT`를 수집기 Service의 주소로 바꿉니다.
- Grafana에는 관리자 계정을 주지 않습니다. 읽기만 하는 계정(`readonly=2`: SELECT와 플러그인이 쓰는 SET만 됨)을 씁니다.

## 무엇이 아닌가

- **경보:** 아직 없습니다(`GF_UNIFIED_ALERTING_ENABLED=false`). 걸 때는 [`docs/runbook/observe.md`](../../docs/runbook/observe.md)의 표가 기준입니다.
- **고가용성:** ClickHouse는 하나입니다. 그 노드가 내려가 있는 동안의 것은 수집기의 재시도(5분)를 넘으면 버려집니다.
- **백업:** ClickHouse의 데이터는 백업하지 않습니다.
- **사용자:** Grafana의 `admin` 하나입니다. 사람이 늘면 Grafana의 사용자나 SSO를 정합니다.
