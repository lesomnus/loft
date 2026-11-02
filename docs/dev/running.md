# 로컬에서 실행하기

체크아웃에서 Loft를 띄우고, 설정을 읽고, 화면을 고치는 법입니다. 운영 배포는 [`deploy/compose`](../../deploy/compose/README.md)와 [`deploy/k8s`](../../deploy/k8s/README.md), 운영자가 하는 일은 [운영자 안내서](../guide/operator.md)에 있습니다.

## 필요한 것

- Go 1.27
- Node 22.12 이상. UI를 빌드할 때만 필요합니다. CI와 이미지는 Node 26을 씁니다.
- 데이터베이스는 기본이 SQLite 파일이라 따로 설치할 것이 없습니다. PostgreSQL로 띄우려면 [PostgreSQL](postgres.md)을 봅니다.

## 처음 띄우기

```sh
# UI 빌드 (한 번)
cd ts && npm install && npm run build && cd ..

# 첫 워크스페이스와 소유자, 그리고 체험용 데이터(5개 팀 50명, 공간, 자산 140여 개)
go run ./cmd/loft init --tenant loft --name "우리 회사" --login admin --password admin1234 --demo

# 서버
go run ./cmd/loft serve
```

<http://localhost:8080> 에서 `admin` / `admin1234` 로 로그인합니다.

- `--demo` 로 만든 사람들은 모두 비밀번호 `demo1234` 로 로그인할 수 있습니다. `init` 이 역할별 예시 아이디를 출력합니다(매니저 `minjun-kim`, 관리자 `gunwoo-lee`, 감사자 `naeun-park`, 구성원 `haeun-lee` 등). 체험용 데이터에 무엇이 있는지는 [안내서의 첫머리](../guide/README.md)에 있습니다.
- 체험용 데이터 없이 실제로 쓰려면 `--demo` 를 뺍니다. `--password` 를 빼면 비밀번호를 만들어 출력합니다.
- 소유자의 이름은 `--person`(기본 `관리자`)으로 정합니다. 사람 기록과 roster 계정에 같은 이름이 들어갑니다. 그래서 그 계정이 나중에 화면에서 만드는 워크스페이스에서도 그 이름으로 불립니다.
- `--tenant` 를 빼면 주소는 `loft`, `--login` 을 빼면 `admin` 입니다.
- 처음부터 다시 하려면 `data/` 디렉터리를 지웁니다.

계정은 Loft 프로세스 안에서 함께 뜨는 roster에 있습니다. roster의 DB는 Loft의 SQLite 파일 옆 `data/roster.db` 입니다([계정과 로그인](identity.md)).

## 설정

설정은 `loft.yaml` 과 `LOFT_*` 환경변수로 줍니다. 환경변수가 파일보다 앞섭니다. 기본값과 설명은 저장소 루트의 [`loft.yaml`](../../loft.yaml)에 있습니다.

```sh
go run ./cmd/loft config env          # 설정이 읽는 모든 환경변수
go run ./cmd/loft config env --set    # 그 가운데 지금 정해진 것 (값은 말하지 않음)
go run ./cmd/loft config              # 읽은 설정 전체와 각 값이 어디서 왔는지 (비밀값은 가림)
go run ./cmd/loft --config 다른.yaml serve
```

- `--config` 를 주지 않으면 지금 디렉터리의 `loft.yaml` 을 읽습니다. 체크아웃 밖에서 띄울 때는 `--config` 로 파일을 정하고, DB와 파일의 경로도 그 파일에 적습니다.
- 키를 비워 둔 체크아웃은 시작할 때마다 서명 키를 새로 만듭니다. 그래서 다시 시작하면 다시 로그인합니다. 로그인을 이어 가려면 키를 하나 줍니다(`LOFT_APP_SIGNING_KEY`, [세션의 키](security.md#세션의-키)).
- 메일 서버(`app.mail`)를 비워 두면 메일을 보내지 않고 메일 내용과 링크를 서버 로그에 남깁니다. 초대 링크는 그 로그에서 읽습니다.

## UI 개발

```sh
go run ./cmd/loft serve          # :8080
cd ts && npm run dev             # :5173, API 호출은 Vite가 :8080으로 넘김
```

- 스캔한 라벨이 개발 서버로 열리게 하려면 `LOFT_APP_APP_URL=http://localhost:5173` 을 주고 서버를 띄웁니다.
- 오프라인으로 여는 것(서비스 워커)은 빌드한 UI(`npm run build`)를 서버가 줄 때만 동작합니다. `npm run dev` 에서는 등록하지 않습니다.
- 타입 검사는 `cd ts && npm run check` 입니다.

## 휴대폰으로 써 보려면

브라우저는 **HTTPS 이거나 localhost 일 때만** 카메라를 열어 줍니다. 그래서 PC의 `http://localhost:8080` 은 그 PC에서만 카메라 스캔이 됩니다.

- 같은 PC에서 USB/블루투스 바코드 스캐너를 쓰면 스캔 화면의 입력칸에 그대로 입력됩니다(QR 주소와 태그 모두 읽습니다).
- 휴대폰에서 쓰려면 서버를 HTTPS로 열어야 합니다. 예를 들어 [Caddy](https://caddyserver.com)를 앞에 두고(`reverse_proxy localhost:8080`, 사내망이면 `tls internal`) `app.public_url` 을 그 주소에 맞춥니다.
- 라벨 QR은 `<public_url>/l/<코드>` 로 인쇄되므로(D28), 휴대폰 기본 카메라로 찍어도 바로 그 자산이 열립니다.
- 프록시를 앞에 두면 `app.trusted_proxies` 에 프록시 주소를 적습니다. 왜 그래야 하는지는 [믿는 프록시](security.md#믿는-프록시)에 있습니다.

라벨의 코드와 워크스페이스의 도메인은 [라벨과 워크스페이스 도메인](domains.md)에 있습니다.

## 터미널로 써 보기

띄운 서버에 터미널로 로그인하는 법은 [터미널과 비서](../guide/terminal.md)에 있습니다. `loft api` 는 서버의 설정을 읽지 않고, 자기 로그인 정보(`LOFT_CLIENT_CONFIG`)만 읽습니다.

## 예전 체크아웃

roster 이전에 만든 `data/` 는 로그인이 Loft의 DB에만 있어서 업그레이드한 뒤에는 그대로 로그인할 수 없습니다. `loft identity migrate` 로 한 번 옮깁니다([roster 이전의 배포](identity.md#roster-이전의-배포)).
