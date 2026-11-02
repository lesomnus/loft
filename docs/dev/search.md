# 검색 엔진 (OpenSearch)

설정에 클러스터가 없으면 검색은 데이터베이스가 답합니다(인덱스 없이 그 워크스페이스의 행을 모두 읽습니다). 개발용과 작은 배포에는 그것으로 충분합니다. `app.search.opensearch` 로 클러스터를 주면 OpenSearch와 한국어 분석기 nori가 답합니다. nori는 "웹서버"를 웹 · 서버로, "의자를"을 의자로 나눠서 찾습니다(D31).

자산(`AssetService.Search`)과 사람·팀·거래처(`PartyService.Search`), 모델(`ItemModelService.Search`)을 이렇게 찾습니다. 색인은 셋이 따로이고, 별칭도 `<index>-assets`, `<index>-parties`, `<index>-models` 로 따로입니다. 엔진은 `server/opensearch` 입니다(HTTP API를 직접 부르는 클라이언트와, 색인마다의 `Index`).

## 어떻게 찾나

무엇을 입력했는지에 따라 이렇게 찾습니다.

- **낱말:** 붙여 쓴 합성어(웹서버)와 조사가 붙은 낱말(의자를)도 낱말로 찾습니다. 쳐 넣은 한 낱말은 그 조각을 모두 갖춘 것만 찾습니다. 그래서 없는 태그(E2E-ABC12)는 글자 몇 개가 겹치는 다른 자산을 끌어오지 않습니다.
- **두 낱말:** 둘 다 맞은 것을 먼저 보여 주고, 그다음은 드문 낱말이 맞은 것부터입니다. "GPU 서버"면 GPU서버, NVIDIA GPU, 웹서버 순입니다.
- **태그와 시리얼:** 통째로도, 일부로도 찾습니다.
- **이름:** 입력하는 중인 앞부분으로도, 한 글자 틀린 것으로도 찾습니다.
- **사람:** 이름의 두 글자 이상 일부로도 찾습니다("민수"로 김민수). 이름 전체가 맞은 사람이 먼저입니다. 팀 이름으로 찾으면 팀이 먼저 나오고, 그 팀의 사람이 뒤에 나옵니다. 이메일과 전화번호는 색인에 넣지 않습니다.
- **모델:** 이름, 제조사, 코드, 유형 이름으로 찾습니다.

- 저장한 것은 그 응답이 오기 전에 색인에 들어갑니다(`Indexer.Interceptor`, gRPC 체인). 저장하고 바로 검색해도 보입니다. 다른 사람이나 다른 프로세스가 바꾼 것은 1초쯤 늦을 수 있습니다.
- 아무것도 입력하지 않은 목록(필터만)은 그대로 데이터베이스가 답합니다. 클러스터가 답하지 않으면 검색은 "지금은 검색할 수 없습니다"로 답하고, 데이터베이스로 몰래 넘어가지 않습니다.
- 클러스터가 찾은 것은 ID일 뿐이고, 행은 벽을 거쳐 그 순서대로 읽습니다. 색인은 사본이라 누가 무엇을 볼 수 있는지 묻지 않고, 사람의 이메일과 전화번호를 담지 않습니다. 브라우저는 OpenSearch에 닿지 않습니다.

## 색인을 만들고 따라가는 법

색인은 서버가 만듭니다. 처음 뜰 때 색인이 없거나, 색인의 모양이 바뀐 빌드가 뜨면 루프가 DB로 새로 만든 뒤 별칭을 옮깁니다. 그동안 검색은 옛 색인이 답하고, 옛 색인이 없으면 "지금은 검색할 수 없습니다"입니다. 색인마다 모양(`assets.json`, `parties.json`, `models.json`)과 `Version` 이 있고, 모양을 바꾸면 `Version` 을 올립니다.

- **바뀐 것이 색인에 닿는 길:**
  - 자산, 유형, 모델, 사람이 바뀌면 같은 트랜잭션에 "다시 색인할 것"(`searchstale`)이 남습니다(`opensearch.Recorder`).
  - 배포에서 한 서버의 루프(`loft.search.index`, `opensearch.Indexer`)가 1초쯤 안에 그 문서를 DB의 지금 상태로 다시 만듭니다. 이름을 빌려 오는 문서도 함께입니다: 모델 이름이 바뀌면 그 모델의 자산, 유형 이름이 바뀌면 그 유형의 모델, 팀 이름이 바뀌면 그 팀의 사람입니다.
  - 개인정보 삭제로 바뀐 이름도, 지운 자산도, 파기한 워크스페이스도 이 길로 색인에서 사라집니다. 워크스페이스의 파기는 행을 지우며 기록기를 지나지 않으므로, 그 트랜잭션에서 자기 행을 따로 씁니다(`offboard.Deployment.Unindex`). 복구는 그 반대입니다(`Reindex`).
- **클러스터가 답하지 않으면** 바뀐 것은 큐에서 기다립니다. 로그에는 시작할 때, 이어지는 동안 1분에 한 번, 다시 될 때 남습니다.

## 운영 구성에서 켜기

운영 구성에서 켜는 법은 `deploy/compose/README.md` 의 "검색을 켜려면"과 `deploy/k8s/README.md` 의 9에 있습니다.

- **이미지:** [`deploy/opensearch`](../../deploy/opensearch/Dockerfile)입니다. 공식 이미지에 nori를 넣었고, 쓰지 않는 플러그인은 뺐습니다. TLS와 인증을 맡는 security 플러그인은 남겼습니다.
- **설정:**

  ```yaml
  app:
    search:
      opensearch:
        addrs: [https://search-0:9200]   # 노드 여럿. 답하지 않는 노드는 건너뜁니다
        username: loft
        password: env:LOFT_SEARCH_PASSWORD   # file:/run/secrets/..., 또는 값 그대로
        ca_file: /etc/loft/opensearch-ca.pem
        index: loft   # 인덱스 이름의 앞부분. 별칭 loft-assets가 loft-assets-<시각>을 가리킵니다 (parties, models도 같음)
  ```

  환경변수로는 `LOFT_APP_SEARCH_OPENSEARCH_ADDRS=https://search-0:9200,https://search-1:9200` 처럼 씁니다. 한 클러스터를 여러 배포가 나눠 쓰면 `index` 를 배포마다 다르게 줍니다.
- **Loft 계정의 role:** 클러스터 권한은 bulk 쓰기 하나이고, 인덱스 권한은 자기 이름(`<index>-*`)에만 줍니다. 색인 목록도 클러스터의 상태가 아니라 색인의 설정으로 읽으므로 그 이상은 필요 없습니다. CI가 이 role로 테스트를 돌립니다.

  ```sh
  curl -u admin --cacert root-ca.pem -X PUT https://search-0:9200/_plugins/_security/api/roles/loft \
    -H 'Content-Type: application/json' \
    -d '{"cluster_permissions":["indices:data/write/bulk"],"index_permissions":[{"index_patterns":["loft-*"],"allowed_actions":["indices_all"]}]}'
  ```

- `loft`, `loft-admin`, 마이그레이션이 모두 같은 검색 설정을 받습니다. 운영자가 워크스페이스를 파기한 것도 색인에 닿아야 하기 때문입니다.

## 명령

```sh
loft search reindex          # 지금 다시 만들기: 색인마다 DB로 새 인덱스를 다 채운 뒤, 별칭을 옮기고 옛 인덱스를 지움
loft search status           # 색인마다 별칭이 가리키는 인덱스, 문서 수와 DB의 행 수
loft search analyze 웹서버    # 그 글을 어떤 낱말로 나누는지: 색인할 때와 찾을 때
loft search remove --yes     # 이 배포가 만든 인덱스와 별칭을 모두 지움
```

- reindex 동안에도 별칭은 다 채워진 인덱스만 가리킵니다. 옛 인덱스에서 새 인덱스로 한 번에 넘어갑니다.
- reindex는 한 번에 하나만 돌립니다. 둘째는 첫째가 쓰는 중인 인덱스를 지웁니다.
- 인덱스 이름의 시각은 UTC입니다.
- 클러스터가 없는 배포에서는 `app.search.opensearch.addrs names no cluster` 로 끝납니다.

## 개발용 클러스터

보안 플러그인을 끄고 loopback에만 띄웁니다.

```sh
docker build -t loft-opensearch deploy/opensearch
docker run -d --name loft-os -p 127.0.0.1:9200:9200 -e discovery.type=single-node \
  -e DISABLE_SECURITY_PLUGIN=true -e DISABLE_INSTALL_DEMO_CONFIG=true \
  -e OPENSEARCH_JAVA_OPTS='-Xms512m -Xmx512m' loft-opensearch
export LOFT_APP_SEARCH_OPENSEARCH_ADDRS=http://127.0.0.1:9200
go run ./cmd/loft search reindex
```

클러스터가 필요한 테스트는 [테스트](testing.md)에 있습니다.
