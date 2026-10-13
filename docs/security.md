# 보안 점검 (rove#40)

운영을 시작하기 전에 한 번, 그리고 큰 변경(새 엔티티, 새 HTTP 경로, 배포 방식) 뒤에 다시 봅니다. 2026-10-13에 처음 점검했습니다. 항목마다 그것을 지키는 코드와 테스트를 적어 두었습니다. 테스트가 있는 항목은 `go test ./...`와 `./scripts/cluster.sh`가 매번 다시 확인합니다.

## payday 체크리스트 (권한 가이드 13절)

| 항목 | 결과 | 어디서 지키나 |
| --- | --- | --- |
| `auth.Plain`을 아무도 닿는 곳에 두지 않는다 | 문제없음 | 공개 서버는 세션 쿠키로 읽습니다(`cmd.Build`가 `Server.Auth`를 채움). Plain은 내장 roster를 프로세스 안에서(bufconn) 부를 때만 씁니다(`internal/identity`) |
| 벽 밖(`global: {}`)의 엔티티 목록을 읽는다 | 7개, 모두 RPC로 열려 있지 않음 | Session, CliLogin, AliasHold, Consent, SignInFailure, Outbox, Archived. 권한 표에 이들의 RPC가 하나도 없고, 생기면 `TestNothingBeyondTheWallIsInTheTable`(새로)이 실패합니다 |
| 서빙하는 서버는 벽이 있는 것이고, 벽 없는 것은 서빙하지 않는다 | 문제없음 | `app.RegisterServer(g, s.Walled)`, batch도 `s.Walled`(`cmd/serve.go`) |
| `gate.Interceptor`가 `auth.Interceptor` 뒤에 있다 | 문제없음 | `Server.Grpc`의 순서: auth → 호출 제한 → gate → watch → 닫힌 워크스페이스 |
| 정책이 gate와 batch의 Guard 둘 다에 간다 | 문제없음 | `gate.Interceptor(s.Policy)`, `c.Server.Guard(s.Policy)`. 서빙되는 스택이 표대로인지는 `TestEveryMethodIsAsTheTableSaysForEveryRole`이 봅니다 |
| 발급하는 자격증명의 Grant가 의도한 것이다 | 의도한 것 | 브라우저와 터미널 세션은 `frame.Whole()`입니다. 좁히는 것은 권한 표와 벽입니다 |
| field 3을 선언했다면 `pd.Grouped`가 있다 | 해당 없음 | field 3을 선언한 엔티티가 없습니다 |
| 두 번째 엣지가 같은 테넌트여야 하면 `agrees:`를 쓴다 | 문제없음 | 17개 엔티티가 `agrees:`를 씁니다. 바꿀 수 있는 엣지에는 쓸 수 없어서(payday는 바뀌지 않는 경로만 비교) 아래 셋은 gate가 엣지마다 벽을 거쳐 읽는 것으로 지킵니다 |

`agrees:`를 쓸 수 없는 엣지 셋은 작업 지시서의 업체, 구매의 업체, 구매 품목의 모델입니다.
- 다른 워크스페이스의 것을 넣으면 `NotFound`이고, 자기 것은 됩니다(`TestAVendorOrAModelIsTheWorkspacesOwn`, 새로).
- 이 보장은 지금의 호출자가 모두 워크스페이스 하나의 구성원이라서 성립합니다. 여러 워크스페이스를 한꺼번에 보는 호출자가 생기면 이 셋을 다시 봐야 합니다.

## Rove 항목

| 항목 | 결과 | 어디서 지키나 |
| --- | --- | --- |
| 운영자 진입점은 운영자만 닿는다 | 문제없음 | 아래 "운영자 진입점" 참고 |
| 운영자마다 키가 따로 있다 | 문제없음 | `admin.keys`, 32자 이상(`admin.MinKey`) |
| 공개 바이너리에 운영자 코드가 없다 | 문제없음 | `TestThePublicBinaryLinksNoOperatorCode`(새로)가 `go list -deps`로 확인합니다 |
| 보안 헤더와 CSP | 문제없음 | 아래 "보안 헤더와 CSP" 참고 |
| 로그인과 터미널 로그인 제한 | 문제없음 | 아래 "로그인 제한" 참고 |

- **운영자 진입점.**
  - K8s: Service가 없고, NetworkPolicy가 8081을 막습니다. 들어오는 길은 `kubectl port-forward` 하나입니다.
  - `scripts/cluster.sh`가 클러스터에서 확인합니다. NetworkPolicy를 좁히는 patch를 빼면 이 확인이 실패하는 것도 확인했습니다.
- **보안 헤더와 CSP.**
  - 모든 응답에 `guarded`(`cmd/guard.go`)가 붙입니다: nosniff, Referrer-Policy, X-Frame-Options, `frame-ancestors 'none'`, HSTS(https일 때), CSP report-only(`app.csp`).
  - 인그레스를 거친 응답에 붙는지는 `scripts/cluster.sh`가 봅니다.
  - CSP를 enforce로 바꾸는 것은 운영에서 들어오는 보고를 본 뒤입니다(rove#35).
- **로그인 제한.**
  - 같은 주소에서 같은 아이디로 15분에 10번, 한 주소에서 터미널 로그인은 10분에 20번까지입니다. DB에서 세므로 서버가 여러 대여도 한 번에 셉니다(`cmd/limits_test.go`).
  - 계정 잠금은 roster가 합니다.
  - 이 제한은 `app.trusted_proxies`가 맞아야 맞게 셉니다(`deploy/k8s/README.md`).

## 이번 점검에서 고친 것

1. **로그인 CSRF.** `POST /session`이 Content-Type을 보지 않았습니다.
   - 위험: 다른 사이트의 폼이 `text/plain`으로 JSON 본문을 보내면, 방문자의 브라우저가 공격자가 고른 계정으로 로그인됩니다. 그 사람이 이어서 넣는 것은 공격자의 것이 됩니다.
   - 고침: 이제 JSON만 받습니다(415). 다른 사이트는 이 서버의 허락(CORS) 없이 JSON을 보낼 수 없습니다(`TestASignInAFormOfAnotherSiteCouldPostIsRefused`).
   - 다른 쓰기는 이미 막혀 있었습니다. 쿠키로 인증하는 쓰기는 쿠키가 `SameSite=Lax`라 다른 사이트의 POST에 실리지 않고, `/account`와 `/cli`는 원래 JSON만 받았습니다.
2. **공개 주소가 없는 배포.** 여러 프로세스로 도는데 `app.public_url`이 없으면 `http://localhost:8080`으로 동작했습니다. 세션 쿠키에 Secure가 빠지고, 링크와 라벨도 아무도 열지 않는 주소를 가리켰습니다. 이제 서버가 시작하지 않습니다(`TestADeploymentIsToldWhereItIs`).
3. **의존성 취약점** (govulncheck, 이 코드가 실제로 부르는 것): 19개에서 0개로 줄었습니다.
   - Go 1.27.2: 표준 라이브러리 12개. HTTP/2 서버 크래시와 메모리 고갈, 요청 해석 어긋남, Range 헤더, MIME, TLS, html/template입니다. `go.mod`의 `toolchain`과 `Dockerfile`이 같은 릴리스를 가리킵니다.
   - gRPC 1.83.2: 서버 패닉과 메모리 고갈, 2개.
   - OpenTelemetry SDK 1.45.0: 내보내기 설정이 로그에 주소를 남기는 문제, 1개.
   - excelize 2.11.0: 3개. 가져오기로 누구나 올리는 엑셀 파일을 읽는 라이브러리라서, 메모리 고갈이나 패닉이 실제로 일어날 수 있었습니다.
   - 마지막 1개였던 GO-2026-6615(OTel 로그 SDK)는 OTel을 v1.47로 올려 고쳤습니다. 로그 API가 v1이 되면서 mkot, otx, payday도 함께 바꿨습니다(rove#77).
   - CI `vuln` 작업이 새로 생겼습니다. 이 코드가 부르는 취약점이 새로 보이면 실패합니다. 예외(`accepted`)는 지금 없습니다.
4. **유휴 연결.** 두 HTTP 서버(`rove`, `rove-admin`)가 요청 사이에 비어 있는 연결을 2분 뒤 닫습니다. 요청 헤더 제한(10초)은 원래 있었습니다.
5. **K8s 배포**(rove#39와 함께).
   - `restricted` Pod Security, NetworkPolicy, 서비스 계정 토큰 없음, 서비스 링크 변수 없음.
   - Secret 없는 base, 마이그레이션은 볼륨 없이.

## 확인했고 그대로 둔 것

- **첨부 다운로드.**
  - 링크는 HMAC 서명(상수 시간 비교)과 10분 만료로 지킵니다.
  - 응답에 `nosniff`와 `sandbox` CSP가 붙습니다.
  - 보여 줄 형식만 inline이고, SVG는 내려받기입니다.
- **세션 쿠키.** `__Host-` 접두사, Secure(https일 때), HttpOnly, `SameSite=Lax`입니다.
- **CORS.** `server.http.origins`가 비어 있어서 다른 출처의 페이지는 API를 부를 수 없습니다.
- **요청 크기.** gRPC는 32 MiB, 로그인·계정·터미널 로그인 JSON은 4 KiB까지입니다.
- **로그.** 비밀값을 남기는 곳이 없습니다. CSP 보고는 쿼리 없이, 분당 30줄까지 남깁니다.
- **TypeScript 의존성.** `npm audit` 결과 0개입니다(개발 의존성 포함).

## 클러스터와 운영자가 할 것

이 저장소 밖의 것이라 여기서는 확인하지 못했습니다.

- namespace에 `pod-security.kubernetes.io/enforce: restricted`를 붙입니다.
- NetworkPolicy의 `from`을 인그레스 컨트롤러로 좁히고, 그와 맞게 `app.trusted_proxies`를 씁니다.
- `pods/portforward`와 `pods/exec` 권한은 운영자에게만 줍니다. exec는 파드 안의 DB 접속 정보를 보는 것과 같습니다(`rove init`, 운영자 셸 명령).
- etcd에서 Secret을 암호화하고, 저장소의 Secret은 SOPS 등으로 암호화합니다.
- 노드는 보안 업데이트를 자동으로 받게 하고, 디스크와 백업은 암호화합니다.
- 이미지는 digest로 고정하고, 레지스트리 접근을 제한합니다.
- 인그레스는 TLS 1.2 이상으로 둡니다. HSTS는 Rove가 붙입니다.

## 다시 볼 때

```sh
go test ./...                                           # 위의 테스트들
./scripts/cluster.sh                                    # 클러스터에서 (k3d)
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...   # CI의 vuln 작업과 같은 것
(cd ts && npm audit)
```

- **새 엔티티를 더했다면:**
  - 벽 밖(`global`)이면 RPC가 권한 표에 없는지 봅니다. 테스트가 확인합니다.
  - 다른 엔티티로 가는 엣지가 둘 이상이면 `agrees:`를 쓰거나, 위의 셋처럼 테스트로 고정합니다.
- **새 HTTP 경로를 더했다면:** 쿠키로 인증하는 쓰기라면 JSON만 받는지 봅니다.
