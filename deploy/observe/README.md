# 관측: Grafana와 대시보드 (운영자)

Rove가 내보낸 것과 플랫폼이 말하는 것을 **이미 있는 ClickHouse와 Loki 위에서** 보는 구성입니다(rove#38). 저장소를 새로 두지 않습니다. 데이터는 OpenTelemetry Collector의 `clickhouse` 내보내기가 쓰는 표준 테이블에 있고, 여기서는 그것을 Grafana로 그립니다. 경보는 아직 없습니다. 무엇에 걸지는 [`docs/runbook/observe.md`](../../docs/runbook/observe.md)에 있습니다.

```
Rove (otel:) ─ OTLP ─┐
                     ├─> 노드의 수집기 ─> ClickHouse (로그·추적·지표), Loki (로그) ─> Grafana
이 수집기 ─── OTLP ──┘
  (CNPG, cert-manager, 쿠버네티스)
```

## 무엇이 있나

| | |
| --- | --- |
| `grafana.yaml` | Grafana 하나와 그 볼륨, Service, NetworkPolicy. 데이터 소스와 대시보드는 시작할 때마다 파일에서 읽습니다(화면에서 바꾼 것은 남지 않음) |
| `datasources.yaml` | 데이터 소스의 예. 오버레이가 자기 주소로 바꿉니다 |
| `dashboards/` | `gen.py`가 쓰는 대시보드 둘: **Rove — 서비스**, **Rove — 플랫폼** |
| `collector.yaml` | 클러스터 안 수집기. CNPG와 cert-manager의 지표를 긁고, 쿠버네티스의 상태(재시작, Job, 파드)를 읽어 OTLP로 보냅니다 |
| `cnpg-monitoring.yaml` | DB 쪽: CNPG의 지표에 Rove의 것을 더하는 쿼리(리스를 쥔 루프 수, 검색 대기열). DB의 namespace에 둡니다 |

## 대시보드

- **Rove — 서비스:**
  - 요청 로그(`gRPC out`)에서 초당 요청, 서버 쪽 실패 비율, p95 지연을 봅니다. 메서드별 요청·지연·상태와 요약 표도 있습니다.
  - 백그라운드 작업: 경고·오류 로그, 리스를 쥔 루프의 수, 검색 대기열, Loki의 WARN·ERROR 로그입니다.
- **Rove — 플랫폼:**
  - 백업: 마지막 베이스 백업과 WAL 보관의 경과, 보관 실패, 복구할 수 있는 가장 이른 시점
  - DB: 크기, 연결, 커밋
  - 인증서: 만료까지 남은 날
  - 쿠버네티스: 재시작한 컨테이너, Running이 아닌 파드, 실패한 Job
  - 노드: 디스크·메모리·CPU. 노드 수집기의 `hostmetrics`에서 옵니다
- 테이블 이름은 변수 `db`(기본 `otel`)와 `suffix`로 정합니다. ClickHouse 노드가 여럿이면 분산 테이블(`_dist`), 하나면 접미사가 없습니다.
- 노드는 ClickHouse 매크로 `nickname`으로 나눕니다(`getMacro('nickname')`). 매크로가 없는 ClickHouse라면 노드 패널의 그 식을 바꿉니다.
- 고칠 때는 `gen.py`를 고치고 다시 씁니다: `python3 deploy/observe/dashboards/gen.py deploy/observe/dashboards`. JSON을 직접 고치지 않습니다.

## 필요한 것

- **ClickHouse:** 수집기의 `clickhouse` 내보내기가 쓰는 데이터베이스(`otel`)가 있어야 합니다. 읽기만 하는 계정도 필요합니다(`readonly=2`: SELECT와 플러그인이 쓰는 SET만 됨). 관리자 계정을 Grafana에 주지 않습니다.
- **노드마다 수집기:** OTLP gRPC(4317)를 받습니다. 없으면 수집기 Service의 주소를 `OTLP_ENDPOINT`에 줍니다.
- **Loki(선택):** 로그 패널 하나만 씁니다.
- **인터넷:** Grafana가 시작할 때 ClickHouse 플러그인을 grafana.com에서 받습니다(`GF_PLUGINS_PREINSTALL_SYNC`). 닫힌 망이면 플러그인을 이미지에 넣습니다.

## 오버레이가 가져올 것

```yaml
resources:
  - github.com/lesomnus/rove//deploy/observe?ref=<커밋>
  - ./ingress.yaml
namespace: observe
configMapGenerator:
  - name: grafana-datasources
    behavior: replace
    files:
      - datasources.yaml
patches:
  - target: { kind: Deployment, name: grafana }
    patch: |-
      - op: replace
        path: /spec/template/spec/containers/0/env/0/value
        value: https://grafana.example.com/
```

1. **namespace.** `restricted` Pod Security를 통과합니다.
2. **Secret `grafana`:**

   | 키 | 내용 |
   | --- | --- |
   | `admin-password` | Grafana의 `admin` 비밀번호(줄바꿈 없이) |
   | `clickhouse-password` | ClickHouse 읽기 계정의 비밀번호 |

3. **`datasources.yaml`:** ClickHouse와 Loki의 주소, 읽기 계정의 이름.
4. **주소:** `GF_SERVER_ROOT_URL`을 사람들이 여는 주소로 바꿉니다. 인그레스와 TLS도 정합니다.
5. **NetworkPolicy 좁히기:** `grafana`의 `from`을 인그레스 컨트롤러로 좁힙니다(deploy/k8s의 README 5와 같음).
6. **클러스터 이름:** 수집기의 `CLUSTER_NAME`. 지표에 `k8s.cluster.name`으로 붙습니다.
7. **DB 쪽:**
   - `cnpg-monitoring.yaml`을 DB의 namespace에 적용하고, CNPG `Cluster`의 `spec.monitoring.customQueriesConfigMap`에 넣습니다. DB를 다시 시작하지 않습니다.
   - DB의 NetworkPolicy가 수집기에서 오는 9187(지표)을 받게 합니다.

cert-manager가 없는 클러스터면 `cert-manager` 긁기는 실패만 남기므로, 오버레이에서 그 `scrape_configs` 항목을 뺍니다.

## 무엇이 아닌가

- **경보:** 아직 없습니다(`GF_UNIFIED_ALERTING_ENABLED=false`). 걸 때는 [`docs/runbook/observe.md`](../../docs/runbook/observe.md)의 표가 기준입니다.
- **로그·지표를 쌓는 곳:** ClickHouse와 Loki는 이 구성에 들어 있지 않습니다. 보관 기간과 백업도 그쪽의 일입니다.
- **사용자:** `admin` 하나입니다. 사람이 늘면 Grafana의 사용자나 SSO를 정합니다.
