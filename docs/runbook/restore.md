# 백업과 복구 (운영자)

Kubernetes 참조 배포(`deploy/k8s`)를 무엇으로 백업하고, 잃었을 때 어떤 순서로 되살리고, 되살린 것을 어떻게 확인하는지입니다. 예는 PostgreSQL을 CloudNativePG(CNPG)로, 백업을 S3 호환 저장소로 하는 구성입니다. 다른 도구를 써도 순서와 확인은 같습니다.

목표는 design 1.2절에 있습니다: **RPO 1시간, RTO 4시간, 분기마다 복구 리허설.** 리허설에서 잰 값도 그 절에 적습니다.

## 무엇을 백업하나

| 대상 | 들어 있는 곳 | 백업 | 주기 |
| --- | --- | --- | --- |
| Rove의 DB와 내장 roster의 DB | PostgreSQL | WAL 보관과 베이스 백업(CNPG의 barman-cloud 플러그인 → S3) | WAL은 계속, 베이스 백업은 매일 |
| 첨부 | `rove-files` 볼륨 | S3로 복사 | 매시 |
| 감사 기록 보관소 | `rove-audit` 볼륨 | S3로 복사, 바뀐 파일은 거부 | 매시 |
| 그 체크포인트 | `rove-checkpoints` 볼륨 | S3로 복사, 바뀐 파일은 거부. 아래 "체크포인트는 따로"도 봅니다 | 매시 |
| 감사 기록 서명 키 | Secret `rove-checkpoints`의 `checkpoints.pem` | 비밀 금고. **버킷에 넣지 않습니다** | 바꿀 때마다 |
| 나머지 비밀 | Secret `rove`, `rove-admin`, `rove-search` | 비밀 금고 | 바꿀 때마다 |

- **백업하지 않는 것:** 검색 색인. 색인은 DB의 사본이라 서버가 DB로 다시 만듭니다. 세션도 DB에 있으므로 따로 백업하지 않습니다.
- **WAL:** `archive_timeout`을 둡니다(예: `5min`). 그래야 쓰기가 적은 시간에도 WAL 조각이 그 간격 안에 버킷에 갑니다. DB를 잃었을 때 잃는 것은 이 간격이 상한입니다.
- **볼륨은 `copy`이지 `sync`가 아닙니다.** 여기서 지운 파일도 버킷에 남습니다. 그래서 다음 백업이 이전 백업에 있던 것을 지우지 않습니다.
- **감사 기록과 체크포인트는 한 번 쓰고 바꾸지 않는 파일입니다.** 사본과 내용이 달라진 파일은 덮어쓰지 않고 실패로 끝냅니다(rclone의 `--immutable`). 그것은 백업할 일이 아니라 발견입니다: [감사 기록 위변조 감지 대응](trail-integrity.md).

  ```sh
  rclone copy /data/files       s3:<버킷>/volumes/files
  rclone copy --immutable /data/audit       s3:<버킷>/volumes/audit
  rclone copy --immutable /data/checkpoints s3:<버킷>/volumes/checkpoints
  ```

- **서명 키를 버킷에 넣지 않는 까닭:** 버킷을 쓸 수 있는 사람이 키까지 가지면 체크포인트를 새로 만들어 서명할 수 있습니다. 키는 버킷과 다른 사람이 지킵니다. 잃으면 이전 체크포인트를 검증할 수 없습니다.
- **감사 기록의 보존 기간(`audit.retain`, `audit.by`, 또는 `audit.profile`)이 하나도 없으면 체크포인트가 남지 않습니다.** 보존 패스는 기간이 정해져야 돌고, 체크포인트는 그 패스가 남깁니다. 보관소를 둔 배포는 기간을 정합니다(rove#35). 지우지 않는 기간도 됩니다. 예를 들어 `retain: 2160h`만 두면 90일 뒤 보관소로 옮기기만 합니다.

## 체크포인트는 따로

노드를 통째로 잃으면 그 노드의 볼륨 셋을 함께 잃고, 남는 것은 마지막 복사입니다. 체크포인트 사본이 최신 체크포인트보다 오래되면 복원한 DB는 저장소에 없는 체크포인트까지 번호를 매긴 상태가 됩니다. 복원한 서버가 시작할 때 보존 패스는 그 번호를 건너뛰어 다음 체크포인트를 남기고, `trail verify`는 건너뛴 번호를 `unkept`로 찾습니다(payday#80). 사본에 없던 것이 맞는지 확인한 뒤 `accept --why`합니다. 그 기간의 체크포인트는 잃은 것으로 남습니다.

그래서 체크포인트는 노드와 함께 사라지지 않는 곳에 둡니다. 삭제를 거부하는 저장소이면 더 좋습니다(`docs/runbook/trail-integrity.md`의 "막기"). 그런 곳이 없으면 체크포인트 볼륨의 복사 주기를 보존 패스(`audit.every`, 기본 하루) 직후로 맞춥니다. 그러면 사본이 최신 체크포인트를 놓치는 시간이 짧아집니다.

## 복원

**순서가 중요합니다.** DB, 볼륨, 그다음에 Rove입니다. 준비는 사고 전에 합니다: 복원 매니페스트와 이 순서를 미리 만들어 두고, 리허설로 확인해 둡니다.

### 1. DB

새 클러스터로 시점 복원합니다. 남아 있는 WAL을 모두 쓰거나, 시각을 정합니다(`targetTime`).

```yaml
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: rove-db
spec:
  # 원래 클러스터와 같은 이미지, 저장소, 자원
  bootstrap:
    recovery:
      source: origin
      database: rove
      owner: rove
      # recoveryTarget: { targetTime: "2026-10-16 16:27:30+00" }
  externalClusters:
    - name: origin
      plugin:
        name: barman-cloud.cloudnative-pg.io
        parameters:
          barmanObjectName: <ObjectStore 이름>
          serverName: <원래 클러스터 이름>
```

- 내장 roster의 DB도 같은 클러스터에 있으므로 함께 돌아옵니다.
- 되살린 클러스터가 WAL을 보낼 곳은 **원래와 다른 이름**으로 정합니다(`plugins`의 `serverName`). 같은 경로에 두 클러스터가 쓰면 두 역사가 한곳에 섞입니다.
- 되살린 클러스터의 app Secret(`<클러스터>-app`)의 비밀번호로 Secret `rove`의 `db-dsn`과 `roster-db-dsn`을 새로 만듭니다.
- 정한 시각까지의 WAL이 버킷에 없으면 복원은 실패합니다(PostgreSQL이 목표 시각에 닿지 못했다고 멈춥니다). 그때는 시각을 앞당기거나 남은 WAL을 모두 씁니다.

### 2. 볼륨

**Rove 파드가 뜨기 전에** 볼륨 셋을 채웁니다. 빈 볼륨으로 뜬 서버는 시작할 때 보존 패스를 돌려 자기 체크포인트를 남기고, 그 위로 복원한 체크포인트를 덮게 됩니다.

1. 오버레이를 `replicas: 0`으로 적용합니다. PVC만 만들어집니다.
2. 같은 PVC를 붙인 Job으로 버킷에서 복사해 옵니다. 볼륨이 노드 하나의 것(local-path 등)이면 Job과 Rove를 같은 노드에 둡니다.

   ```sh
   for v in files audit checkpoints; do rclone copy s3:<버킷>/volumes/$v /data/$v; done
   ```

3. Job이 끝나면 `kubectl scale deploy/rove --replicas=1`.

### 3. Rove

- 같은 `signing-key`, 같은 `checkpoints.pem`, 같은 운영자 키를 씁니다.
- `public_url`은 바꾸지 않습니다. 인쇄한 라벨의 QR이 그 주소를 담고 있습니다. 새 클러스터로 옮겼다면 인그레스와 DNS를 그쪽으로 돌립니다.

### 4. 확인

- [ ] `/healthz`가 `ok`이고, 로그인이 됩니다.
- [ ] 사고 직전에 쓴 것이 보입니다. 마지막으로 보이는 쓰기의 시각과 사고 시각의 차이가 실제 RPO입니다.
- [ ] 첨부 하나를 내려받아 봅니다. 해시까지 맞춰 보면 더 좋습니다.
- [ ] `rove trail verify --full`이 깨끗하거나, 복원으로 설명되는 것만 찾습니다: `behind`, `unaccounted`, `missing`, `emptied`, 그리고 체크포인트 사본이 오래되었다면 건너뛴 번호의 `unkept`. 그때는 [DB를 백업에서 복원했을 때](trail-integrity.md#db를-백업에서-복원했을-때)를 따라 `accept --why`합니다. 다른 종류가 나오면 그 런북의 4로 갑니다.
- [ ] 검색: `rove search status`로 색인의 문서 수와 DB의 행 수를 봅니다. 이전 시각으로 되돌렸다면 `rove search reindex`를 실행합니다. 색인에는 그 시각 뒤의 문서가 남아 있기 때문입니다.
- [ ] 백업이 다시 돕니다: 새 클러스터의 WAL 보관(`pg_stat_archiver`)과 볼륨 복사 Job.

## RPO와 RTO

- **DB의 RPO**는 `archive_timeout`이 상한입니다. CNPG가 정상적으로 멈춘 경우(fencing 등)는 멈추기 전에 마지막 WAL까지 보관되어 잃는 것이 없었습니다(리허설에서 확인). 노드가 갑자기 죽으면 마지막으로 보낸 WAL 조각 뒤의 것을 잃습니다.
- **볼륨의 RPO**는 복사 주기입니다. 노드를 통째로 잃으면 마지막 복사 뒤에 올린 첨부는 DB에 행만 남고 파일이 없습니다. 감사 기록과 체크포인트도 같습니다(위의 "체크포인트는 따로").
- **RTO**는 대부분 DB 복원 시간입니다. 베이스 백업을 내려받는 시간과 WAL을 다시 적용하는 시간이라, DB가 클수록, 베이스 백업이 오래될수록 깁니다. 리허설로 재고, 베이스 백업 주기를 RTO에 맞춥니다.

## 리허설 (분기마다)

1. **운영을 건드리지 않습니다.** 다른 namespace에 위 순서대로 복원하고, 다른 주소로 띄웁니다. 검색 클러스터는 운영의 색인을 쓰지 않도록 빼거나 다른 `index`를 줍니다.
2. 사고 시각을 정하고, 단계마다 시각을 적습니다: DB 준비, 볼륨 복원, Rove 준비, 첫 로그인.
3. 위 확인 목록을 모두 합니다. 시각을 정한 복원도 한 번 해서 `trail verify`의 `behind`와 `accept`까지 가 봅니다.
4. 결과(RPO, RTO, 걸린 것)를 design 1.2절과 이슈에 남깁니다.
5. 리허설 namespace를 지웁니다.

운영 DB를 잠시 멈춰서 사고를 흉내 낸다면, CNPG fencing(`cnpg.io/fencedInstances`)은 정상 종료입니다. 그래서 마지막 WAL까지 보관되고 RPO는 0으로 나옵니다. 갑작스러운 장애의 RPO는 `archive_timeout`으로 봅니다.

## 알려진 것

- **내장 roster:** #100 이전의 빌드는 DB가 잠깐이라도 끊기면 그 뒤로 로그인한 모든 요청에 503으로 답합니다(`roster: ... Error while dialing: closed`). 프로세스를 다시 시작할 때까지 그렇고, `/healthz`는 그동안에도 `ok`입니다. 그런 빌드라면 DB가 돌아온 뒤 `kubectl rollout restart deploy/rove`를 실행합니다.
- **체크포인트 사본이 오래된 복원:** 이 빌드 이전의 payday는 건너뛴 체크포인트 번호를 보고하지 않았습니다(payday#80). 위의 "체크포인트는 따로"를 봅니다.
- **처음 배포한 뒤의 `emptied`:** 이 빌드 이전에는 첫 보존 패스가 행이 없는 체크포인트를 남기면 다음 패스가 `emptied`를 잘못 찾았습니다(payday#81). 지금은 찾지 않습니다.
