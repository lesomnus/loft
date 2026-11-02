# PostgreSQL

배포 데이터베이스는 PostgreSQL이고, 공식 이미지를 그대로 씁니다. SQLite는 테스트와 체험용입니다(D12).

- SQLite에는 없는 안전장치(예약·위치가 겹치면 DB가 거부하는 EXCLUDE 제약, 테넌트 사이의 참조를 막는 트리거 등)가 마이그레이션 때 함께 설치됩니다(`cli/pg.sql`). 필요한 확장은 `btree_gist` 하나입니다.
- 검색은 [검색 엔진](search.md)(OpenSearch)이 답합니다. 클러스터를 설정하지 않으면 데이터베이스가 인덱스 없이 그 워크스페이스의 행을 모두 읽어서 답합니다.
- 내장 roster도 같은 서버의 다른 데이터베이스를 씁니다(`auth.roster.db`).

## 로컬에서 띄우기

```sh
docker run -d --name loft-pg -e POSTGRES_USER=loft -e POSTGRES_PASSWORD=loft -p 5432:5432 postgres:18
docker exec loft-pg createdb -U loft roster
export LOFT_DB_DRIVER=pgx LOFT_DB_DSN='postgres://loft:loft@localhost:5432/loft?sslmode=disable'
export LOFT_AUTH_ROSTER_DB_DRIVER=pgx LOFT_AUTH_ROSTER_DB_DSN='postgres://loft:loft@localhost:5432/roster?sslmode=disable'
go run ./cmd/loft init --demo
go run ./cmd/loft serve
```

PostgreSQL로 띄우면 내장 roster를 쓰는 배포가 운영처럼 보이므로 `serve` 가 경고합니다([내장 roster](identity.md#내장-roster)). 체험용이면 그대로 두어도 됩니다.

## 마이그레이션

- 체크아웃의 기본(`db.migrate: true`)은 `serve` 가 시작할 때 테이블을 맞춥니다.
- 운영에서는 서버가 테이블을 직접 바꾸지 않습니다(`db.migrate: false`). 새 빌드를 띄우기 전에 `loft migrate` 로 테이블을 맞추고, 모양이 다르면 `serve` 는 시작하지 않습니다. 순서는 [운영자 안내서](../guide/operator.md#새-버전-올리기)에 있습니다.
- 판마다 마이그레이션이 한 번 하는 일(옮기는 행, 짓는 인덱스, 멈추는 경우)은 [판을 올릴 때](../runbook/upgrade.md)에 있습니다. 행이 많은 표에 인덱스를 미리 짓는 법(`CREATE INDEX CONCURRENTLY`)과, 짓다 만 인덱스(`INVALID`)가 있으면 `loft migrate` 가 멈추는 것도 거기에 있습니다.

```sh
loft migrate     # 이 빌드의 모양으로 테이블을 맞추고 끝남 ("the database is in this build's shape")
```

## 여러 대로 띄우려면

PostgreSQL이면 서버를 여러 대 띄울 수 있습니다. 상태는 데이터베이스에 있고, 다음만 맞추면 됩니다.

- `watch.broker: postgres`: 다른 서버에서 일어난 변경이 실시간 화면에 전해집니다(LISTEN/NOTIFY).
- `spin.lease: postgres`: 예약 만료·메일·탈퇴 처리·워크스페이스 파기 같은 백그라운드 작업을 한 대만 돌립니다(advisory lock, payday#54). 그 서버가 죽으면 다른 서버가 이어받습니다. 요청만 받는 서버는 `none`.
  - LISTEN과 세션 잠금은 트랜잭션 모드의 PgBouncer 같은 풀러를 거치면 안 됩니다. 풀러를 쓴다면 `watch.dsn` 과 `spin.dsn` 에 DB로 바로 가는 주소를 줍니다.
  - "정확히 한 번"은 아닙니다. 장애가 넘어가는 순간에는 두 서버가 잠깐 같은 작업을 할 수 있고, 작업은 그래도 괜찮게 짜여 있습니다.
- `app.signing_key`: 모든 서버에 같은 값을 줍니다. 다운로드 링크를 서명하고, 세션의 키도 이 키에서 만든 키로 보관합니다([세션의 키](security.md#세션의-키)). 비워 두면 서버마다 다른 키라, 다른 서버가 만든 다운로드 링크가 열리지 않고 다른 서버의 세션도 알아보지 못합니다. 그래서 `spin.lease` 가 `local` 이 아니거나 `watch.broker` 가 `memory`·`none` 이 아닌데 키가 비어 있으면 `serve` 가 시작하지 않습니다.
- 로그인 제한은 데이터베이스에서 세므로 서버가 몇 대든 한 번에 셉니다([로그인 제한](security.md#로그인-제한)).
- 첨부 파일(`app.files`)은 아직 로컬 디렉터리뿐이라, 여러 서버가 같은 디렉터리를 공유해야 합니다(NFS 등).
- 여러 대의 배포는 roster도 따로 운영합니다([roster를 따로 운영할 때](identity.md#roster를-따로-운영할-때)).

설정할 수 있는 모든 값은 `go run ./cmd/loft config env` 로 볼 수 있습니다. 기본값은 [`loft.yaml`](../../loft.yaml)에 있습니다.

## 테스트

PostgreSQL에서 테스트를 돌리는 법은 [테스트](testing.md)에 있습니다.
