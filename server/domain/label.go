package domain

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path"
	"regexp"
	"strings"
	"uuid"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"golang.org/x/net/idna"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/asset"
	"github.com/lesomnus/loft/internal/ent/countfinding"
	"github.com/lesomnus/loft/internal/ent/custody"
	"github.com/lesomnus/loft/internal/ent/label"
	"github.com/lesomnus/loft/internal/ent/purchase"
	"github.com/lesomnus/loft/internal/ent/tenantdomain"
	"github.com/lesomnus/loft/internal/ent/workorder"
	"github.com/lesomnus/loft/server/apphost"
	"github.com/lesomnus/loft/server/pd"
)

// A label's code is its identifier in base32: 26 characters a phone camera
// reads easily and a URL carries without escaping.
var code32 = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// LabelCode answers what a label's QR encodes after the host.
func LabelCode(id pdid.Id) string { return code32.EncodeToString(id.Bytes()) }

// ParseLabel reads a label identifier out of what a scanner saw: a label URL,
// a bare code, or an identifier as text.
func ParseLabel(v string) (pdid.Id, bool) {
	v = strings.TrimSpace(v)
	if u, err := url.Parse(v); err == nil && u.Host != "" {
		v = path.Base(u.Path)
	}
	if id, err := pdid.Parse(v); err == nil {
		return id, true
	}
	b, err := code32.DecodeString(strings.ToLower(v))
	if err != nil || len(b) != 16 {
		return pdid.Nil, false
	}
	id, err := pdid.From(b)
	return id, err == nil
}

// A label's code (D28): eight of Crockford's base32 -- no I, L, O or U to be
// read for something else, upper case for a QR's alphanumeric mode, which
// packs it densest -- unique in the whole deployment, so that a label is
// found from the deployment's own address whichever workspace it is in. A
// label printed before codes had none and goes by its identifier still
// ([LabelCode]).
const labelAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// newLabelCode is a code at random. 256 is a multiple of 32, so a byte's low
// five bits are as fair as the byte.
func newLabelCode() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i, v := range b {
		b[i] = labelAlphabet[int(v)%len(labelAlphabet)]
	}
	return string(b)
}

// NormalLabelCode is a code as somebody typed or a camera read it, as it is
// kept: upper case, the letters Crockford reads as digits read so, no dashes
// or spaces -- or false for something that is not one.
func NormalLabelCode(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if u, err := url.Parse(v); err == nil && u.Host != "" {
		v = path.Base(u.Path)
	}
	v = strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(v))
	v = strings.NewReplacer("I", "1", "L", "1", "O", "0").Replace(v)
	if len(v) != 8 || strings.Trim(v, labelAlphabet) != "" {
		return "", false
	}
	return v, true
}

// freeLabelCode is a code no label in the deployment has.
func (t *Tx) freeLabelCode() (string, error) {
	for range 8 {
		c := newLabelCode()
		taken, err := t.db.Label.Query().Where(label.Code(c)).Exist(t.ctx)
		if err != nil {
			return "", err
		}
		if !taken {
			return c, nil
		}
	}
	return "", errors.New("labels: no code came free")
}

// The states a tenant domain is in (design 9.9).
const (
	domainPending = "pending"
	domainReady   = "ready"
	domainActive  = "active"
	domainLegacy  = "legacy"
	domainLapsed  = "lapsed"
	domainRetired = "retired"
)

// What a tenant domain is for (design 9.9, loft#155): `app` is the tenant's
// own domain, which the whole app is on and its labels print with; `label` is
// one its labels print with and nothing more -- a default subdomain, or a
// custom domain added before domains carried the app.
const (
	purposeApp   = "app"
	purposeLabel = "label"
)

var hostOk = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

type domainTenantDomain struct {
	Domain
	app.TenantDomainServiceServer
}

func (s Domain) TenantDomain() app.TenantDomainServiceServer {
	return domainTenantDomain{s, s.Next().TenantDomain()}
}

// Add registers a domain of the tenant's: its own (`host`), which the app is
// on and its labels print with once a TXT record says it is the tenant's, or
// a default subdomain of this deployment (`sub`), which its labels print with
// and which is verified already. What it is for is decided here, not asked:
// a custom domain is an `app` one, and a default subdomain a `label` one.
func (s domainTenantDomain) Add(ctx context.Context, req *app.TenantDomainAddRequest) (*app.TenantDomain, error) {
	var out *app.TenantDomain
	err := s.tx(ctx, func(t *Tx) error {
		row := app.TenantDomainAddRequest_builder{
			Id:      pdid.New(pd.TenantDomainDomain).Bytes(),
			Tenant:  t.tenantRef(),
			Purpose: purposeLabel,
			Token:   token(),
		}.Build()

		if sub := strings.ToLower(strings.TrimSpace(req.GetSub())); sub != "" {
			if t.deps.LabelSuffix == "" {
				return failed("이 서버에는 기본 라벨 주소가 없습니다. 자체 도메인을 쓰세요")
			}
			if !regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`).MatchString(sub) {
				return invalid("sub", "영문 소문자, 숫자, -로 씁니다")
			}
			row.SetHost(sub + "." + t.deps.LabelSuffix)
			row.SetSource("default")
			row.SetState(domainReady)
		} else {
			host, err := t.deps.domainHost(req.GetHost())
			if err != nil {
				return err
			}
			row.SetHost(host)
			row.SetPurpose(purposeApp)
			row.SetSource("custom")
			row.SetState(domainPending)
		}

		if err := t.begin(nil, "domain.add", pdid.Nil, t.now, "", app.TenantDomainAdded_builder{
			Domain:  row.GetId(),
			Host:    row.GetHost(),
			Purpose: row.GetPurpose(),
			Source:  row.GetSource(),
		}.Build()); err != nil {
			return err
		}
		v, err := t.next.TenantDomain().Add(t.ctx, row)
		if err != nil {
			return err
		}
		if row.GetSource() == "default" {
			if v, err = t.next.TenantDomain().Patch(t.ctx, app.TenantDomainPatchRequest_builder{
				Ref:              app.TenantDomainRef_builder{Id: v.GetId()}.Build(),
				VerifiedAt:       ts(t.now),
				DateUpdatedForce: z.Ptr(true),
			}.Build()); err != nil {
				return err
			}
			// The first domain a tenant has is the one it prints with.
			active, err := t.activeDomain()
			if err != nil {
				return err
			}
			if active == nil {
				if v, err = t.activate(v.GetId()); err != nil {
					return err
				}
			}
		}
		out = v
		return nil
	})
	return out, err
}

func token() string {
	b := make([]byte, 15)
	rand.Read(b)
	return "loft-" + code32.EncodeToString(b)
}

// domainWords is what a domain is called in an event's sentence.
func domainWords(purpose string) string {
	if purpose == purposeApp {
		return "워크스페이스 도메인"
	}
	return "라벨 도메인"
}

// domainHost is a host somebody typed for a tenant's own domain, as it is
// kept -- lower case, in its ASCII form (IDNA), with no trailing dot -- or why
// it is not one a tenant may take: not a host name, an address, a name under
// `localhost`, this deployment's own host or a name under it (where people
// sign in, and the app of every tenant with no domain), or a name under the
// suffix default subdomains are made with. A host is the deployment's to give
// once, to one tenant; the TXT record is what says the tenant holds it.
func (d *Deps) domainHost(v string) (string, error) {
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(v)), ".")
	if net.ParseIP(strings.Trim(host, "[]")) != nil {
		return "", invalid("host", "IP 주소가 아니라 호스트 이름을 적어 주세요 (예: assets.example.com)")
	}
	ascii, err := idna.Lookup.ToASCII(host)
	if err != nil || !hostOk.MatchString(ascii) {
		return "", invalid("host", "호스트 이름을 적어 주세요 (예: assets.example.com)")
	}
	host = ascii
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return "", invalid("host", "localhost 아래의 이름은 쓸 수 없습니다")
	}
	for _, s := range d.ServiceHosts {
		if s != "" && (host == s || strings.HasSuffix(host, "."+s)) {
			return "", invalid("host", "%s은(는) 이 서비스의 주소입니다", host)
		}
	}
	if s := d.LabelSuffix; s != "" && (host == s || strings.HasSuffix(host, "."+s)) {
		return "", invalid("host", "%s의 하위 주소는 기본 주소로 추가하세요", s)
	}
	return host, nil
}

// activeDomain is the tenant's one active domain, of either purpose: what its
// labels print with, and -- an `app` one -- where its app is. None prints with
// this deployment's own address.
func (t *Tx) activeDomain() (*ent.TenantDomain, error) {
	v, err := t.db.TenantDomain.Query().Where(
		tenantdomain.TenantId(t.tenant.Uuid()),
		tenantdomain.PurposeIn(purposeApp, purposeLabel),
		tenantdomain.State(domainActive),
		tenantdomain.DateErasedIsNil(),
	).First(t.ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	return v, err
}

// activate makes a domain the tenant's active one. The one active before is
// legacy, and an `app` one sends its pages on to where the app is now, for
// [Deps.DomainRedirect] (loft#155); what was printed with it still opens.
func (t *Tx) activate(id []byte) (*app.TenantDomain, error) {
	cur, err := t.activeDomain()
	if err != nil {
		return nil, err
	}
	if cur != nil && pdid.Id(cur.Id) == idOf(id) {
		return t.next.TenantDomain().Get(t.ctx, app.TenantDomainGetRequest_builder{Ref: app.TenantDomainRef_builder{Id: id}.Build()}.Build())
	}
	if cur != nil {
		legacy := app.TenantDomainPatchRequest_builder{
			Ref:              app.TenantDomainRef_builder{Id: pdid.Id(cur.Id).Bytes()}.Build(),
			State:            z.Ptr(domainLegacy),
			DateUpdatedForce: z.Ptr(true),
		}.Build()
		if cur.Purpose == purposeApp {
			legacy.SetRedirectUntil(ts(t.now.Add(t.deps.domainRedirect())))
		}
		if _, err := t.next.TenantDomain().Patch(t.ctx, legacy); err != nil {
			return nil, err
		}
	}
	// In use from now, and asked about again from now.
	return t.next.TenantDomain().Patch(t.ctx, app.TenantDomainPatchRequest_builder{
		Ref:               app.TenantDomainRef_builder{Id: id}.Build(),
		State:             z.Ptr(domainActive),
		RedirectUntilNull: z.Ptr(true),
		Failures:          z.Ptr(uint32(0)),
		FailingSinceNull:  z.Ptr(true),
		CheckedAt:         ts(t.now),
		DateUpdatedForce:  z.Ptr(true),
	}.Build())
}

// Verify checks a pending or a lapsed custom domain's DNS: its TXT record,
// and that it points here (loft#155). A pending one is ready once it does, for
// a person to switch to; a lapsed one is the workspace's active domain again,
// when the workspace has no other.
func (s domainTenantDomain) Verify(ctx context.Context, req *app.TenantDomainVerifyRequest) (*app.TenantDomain, error) {
	var out *app.TenantDomain
	err := s.tx(ctx, func(t *Tx) error {
		v, err := t.next.TenantDomain().Get(t.ctx, app.TenantDomainGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		if v.GetState() != domainPending && v.GetState() != domainLapsed {
			out = v
			return nil
		}
		host := v.GetHost()
		switch txt, here := t.deps.txt(t.ctx, host, v.GetToken()), t.deps.pointsHere(t.ctx, host); {
		case txt == heardNo:
			return failed("_loft-challenge.%s에 TXT 레코드 %s가 없습니다. DNS 반영에 시간이 걸릴 수 있습니다", host, v.GetToken())
		case here == heardNo:
			return failed("%s가 %s를 가리키지 않습니다. CNAME을 확인하세요", host, t.deps.LabelTarget)
		case txt != heardYes || here != heardYes:
			return status.Error(codes.Unavailable, "지금은 DNS를 읽을 수 없습니다. 잠시 뒤에 다시 확인하세요")
		}
		out, err = t.verified(v.GetId(), v.GetPurpose(), host, v.GetState() == domainLapsed, false)
		return err
	})
	return out, err
}

// verified is a domain whose DNS checked out: ready, and -- one the sweep
// found, or one that lapsed -- the workspace's active domain when it has
// none. One left ready for a person to switch to tells whoever may, when
// `tell`.
func (t *Tx) verified(id []byte, purpose, host string, lapsed, tell bool) (*app.TenantDomain, error) {
	if err := t.begin(nil, "domain.verify", pdid.Nil, t.now, "", app.TenantDomainVerified_builder{
		Domain:  id,
		Host:    host,
		Purpose: purpose,
		Again:   lapsed,
	}.Build()); err != nil {
		return nil, err
	}
	v, err := t.next.TenantDomain().Patch(t.ctx, app.TenantDomainPatchRequest_builder{
		Ref:              app.TenantDomainRef_builder{Id: id}.Build(),
		State:            z.Ptr(domainReady),
		VerifiedAt:       ts(t.now),
		DateUpdatedForce: z.Ptr(true),
	}.Build())
	if err != nil || (!lapsed && !tell) {
		return v, err
	}
	cur, err := t.activeDomain()
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return t.activate(id)
	}
	if tell {
		// Switching to it is a person's, which a read-only workspace takes
		// once it writes again (D64, D68): said so while it does not.
		body := "설정에서 활성화하면 새로 인쇄하는 라벨부터 이 주소를 씁니다"
		quiet, err := t.readonly()
		if err != nil {
			return nil, err
		}
		if quiet {
			body = "워크스페이스를 다시 바꿀 수 있게 되면 설정에서 활성화할 수 있고, 그러면 새로 인쇄하는 라벨부터 이 주소를 씁니다"
		}
		if err := t.notifyManagers("domain.ready", domainWords(purpose)+" 확인 완료: "+host, body, idOf(id), "/settings"); err != nil {
			return nil, err
		}
	}
	return v, nil
}

// Activate makes a verified domain the tenant's active one: what new labels
// print with, and for an `app` domain where the app is. The one active before
// is legacy, and what was printed with it still opens.
func (s domainTenantDomain) Activate(ctx context.Context, req *app.TenantDomainActivateRequest) (*app.TenantDomain, error) {
	var out *app.TenantDomain
	err := s.tx(ctx, func(t *Tx) error {
		v, err := t.next.TenantDomain().Get(t.ctx, app.TenantDomainGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		switch v.GetState() {
		case domainReady, domainLegacy, domainActive:
		case domainLapsed:
			return failed("%s의 DNS가 더 이상 이 서비스를 가리키지 않아 사용이 중지되었습니다. 다시 확인한 뒤에 쓸 수 있습니다", v.GetHost())
		default:
			return failed("%s은(는) 아직 확인되지 않았습니다. 확인된 도메인만 쓸 수 있습니다", v.GetHost())
		}
		if err := t.begin(nil, "domain.activate", pdid.Nil, t.now, "", app.TenantDomainActivated_builder{
			Domain:  v.GetId(),
			Host:    v.GetHost(),
			Purpose: v.GetPurpose(),
		}.Build()); err != nil {
			return err
		}
		out, err = t.activate(v.GetId())
		return err
	})
	return out, err
}

// Retire stops a domain resolving. Labels printed with it stop opening, so
// that has to be meant.
func (s domainTenantDomain) Retire(ctx context.Context, req *app.TenantDomainRetireRequest) (*app.TenantDomain, error) {
	var out *app.TenantDomain
	err := s.tx(ctx, func(t *Tx) error {
		v, err := t.next.TenantDomain().Get(t.ctx, app.TenantDomainGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		n, err := t.db.Label.Query().Where(label.TenantId(t.tenant.Uuid()), label.DomainId(uuidOf(v.GetId())), label.StateNEQ("void")).Count(t.ctx)
		if err != nil {
			return err
		}
		if n > 0 && !req.GetForce() {
			return failed("%s로 인쇄된 라벨 %d장이 더 이상 열리지 않게 됩니다. 그래도 중지하려면 강제로 중지하세요", v.GetHost(), n)
		}
		if err := t.begin(nil, "domain.retire", pdid.Nil, t.now, "", app.TenantDomainRetired_builder{
			Domain:  v.GetId(),
			Host:    v.GetHost(),
			Purpose: v.GetPurpose(),
			Force:   req.GetForce(),
			Labels:  uint32(n),
		}.Build()); err != nil {
			return err
		}
		out, err = t.next.TenantDomain().Patch(t.ctx, app.TenantDomainPatchRequest_builder{
			Ref:              req.GetRef(),
			State:            z.Ptr(domainRetired),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
		return err
	})
	return out, err
}

// Status answers where this tenant's labels print and what a domain of its
// own takes: its active domain, or none and this deployment's address.
func (s domainTenantDomain) Status(ctx context.Context, _ *app.TenantDomainStatusRequest) (*app.TenantDomainStatusResponse, error) {
	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	out := app.TenantDomainStatusResponse_builder{
		Target:        t.deps.LabelTarget,
		DefaultSuffix: t.deps.LabelSuffix,
		Base:          t.deps.Links.Home(apphost.Label),
	}.Build()
	cur, err := t.activeDomain()
	if err != nil {
		return nil, err
	}
	if cur != nil {
		v, err := t.next.TenantDomain().Get(ctx, app.TenantDomainGetRequest_builder{Ref: app.TenantDomainRef_builder{Id: pdid.Id(cur.Id).Bytes()}.Build()}.Build())
		if err != nil {
			return nil, err
		}
		out.SetActive(v)
	}
	return out, nil
}

type domainLabel struct {
	Domain
	app.LabelServiceServer
}

func (s Domain) Label() app.LabelServiceServer { return domainLabel{s, s.Next().Label()} }

// Print makes labels: with the tenant's active domain when it has one, and
// with this deployment's own address otherwise (design 9.9, loft#155). Each
// keeps the domain it was printed with, and opens on it for as long as the
// domain is not retired, whichever domain is active then.
func (s domainLabel) Print(ctx context.Context, req *app.LabelPrintRequest) (*app.LabelPrintResponse, error) {
	var out *app.LabelPrintResponse
	err := s.tx(ctx, func(t *Tx) error {
		dom, err := t.activeDomain()
		if err != nil {
			return err
		}
		// What every label of these prints: its workspace's active domain,
		// or this deployment's address while it has none (design 9.9).
		at, err := t.deps.Links.Of(t.ctx, t.db, t.tenant, apphost.Label)
		if err != nil {
			return err
		}
		n := int(req.GetCount())
		ids := req.GetAssetIds()
		if len(ids) == 0 && (n <= 0 || n > 500) {
			return invalid("count", "한 번에 1~500장, 또는 자산을 고르세요")
		}
		batch := strings.TrimSpace(req.GetBatch())
		if batch == "" {
			batch = t.now.In(t.zone()).Format("2006-01-02 15:04")
		}

		if err := t.begin(nil, "label.print", pdid.Nil, t.now, "", app.LabelsPrinted_builder{
			Batch:  batch,
			Count:  uint32(max(n, len(ids))),
			Assets: ids,
		}.Build()); err != nil {
			return err
		}

		out = &app.LabelPrintResponse{}
		makeOne := func(subject []byte) error {
			code, err := t.freeLabelCode()
			if err != nil {
				return err
			}
			req := app.LabelAddRequest_builder{
				Tenant:    t.tenantRef(),
				State:     "new",
				PrintedAt: ts(t.now),
				Batch:     batch,
				Code:      z.Ptr(code),
			}.Build()
			if dom != nil {
				req.SetDomain(app.TenantDomainRef_builder{Id: pdid.Id(dom.Id).Bytes()}.Build())
			}
			if subject != nil {
				req.SetSubjectId(subject)
				req.SetState("bound")
				req.SetBoundAt(ts(t.now))
			}
			v, err := t.next.Label().Add(t.ctx, req)
			if err != nil {
				return err
			}
			out.SetLabels(append(out.GetLabels(), v))
			out.SetUrls(append(out.GetUrls(), at+"/l/"+code))
			return nil
		}
		if len(ids) > 0 {
			for i, b := range ids {
				if _, _, err := t.get(app.AssetRef_builder{Id: b}.Build(), fmt.Sprintf("asset_ids[%d]", i)); err != nil {
					return err
				}
				if err := makeOne(b); err != nil {
					return err
				}
			}
			return nil
		}
		for range n {
			if err := makeOne(nil); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// Bind is a label stuck on an asset.
func (s domainLabel) Bind(ctx context.Context, req *app.LabelBindRequest) (*app.Label, error) {
	var out *app.Label
	err := s.tx(ctx, func(t *Tx) error {
		l, err := t.next.Label().Get(t.ctx, app.LabelGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		a, id, err := t.get(req.GetAsset(), "asset")
		if err != nil {
			return err
		}
		switch l.GetState() {
		case "void":
			return failed("폐기된 라벨입니다")
		case "bound":
			if idOf(l.GetSubjectId()) == id {
				out = l
				return nil
			}
			return failed("다른 자산에 붙은 라벨입니다. 먼저 떼세요")
		}
		if err := t.begin(req.GetOp(), "label.bind", id, t.now, "", app.LabelBound_builder{
			Label: l.GetId(),
			Asset: id.Bytes(),
			Tag:   a.GetTag(),
		}.Build()); err != nil {
			return err
		}
		out, err = t.next.Label().Patch(t.ctx, app.LabelPatchRequest_builder{
			Ref:              req.GetRef(),
			SubjectId:        id.Bytes(),
			State:            z.Ptr("bound"),
			BoundAt:          ts(t.now),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
		return err
	})
	if err == errDone {
		return s.LabelServiceServer.Get(ctx, app.LabelGetRequest_builder{Ref: req.GetRef()}.Build())
	}
	return out, err
}

// Unbind takes a label off what it was on, and voids it if it is damaged.
func (s domainLabel) Unbind(ctx context.Context, req *app.LabelUnbindRequest) (*app.Label, error) {
	var out *app.Label
	err := s.tx(ctx, func(t *Tx) error {
		l, err := t.next.Label().Get(t.ctx, app.LabelGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		state := "new"
		if req.GetVoid() {
			state = "void"
		}
		if err := t.begin(nil, "label.unbind", idOf(l.GetSubjectId()), t.now, "", app.LabelUnbound_builder{
			Label: l.GetId(),
			Asset: l.GetSubjectId(),
			Void:  req.GetVoid(),
		}.Build()); err != nil {
			return err
		}
		out, err = t.next.Label().Patch(t.ctx, app.LabelPatchRequest_builder{
			Ref:              req.GetRef(),
			SubjectIdNull:    z.Ptr(true),
			State:            z.Ptr(state),
			BoundAtNull:      z.Ptr(true),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
		return err
	})
	return out, err
}

// Resolve reads what a scanned code names, through the wall: another
// tenant's label is not found.
func (s domainLabel) Resolve(ctx context.Context, req *app.LabelResolveRequest) (*app.LabelResolveResponse, error) {
	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	// A code, which is a label of this tenant's or nothing here -- though
	// eight characters may be an asset's tag too, which is tried after.
	if c, ok := NormalLabelCode(req.GetCode()); ok {
		l, err := t.db.Label.Query().Where(label.Code(c), label.TenantId(t.tenant.Uuid()), label.DateErasedIsNil()).Only(ctx)
		if err == nil {
			return t.resolved(pdid.Id(l.Id))
		}
		if !ent.IsNotFound(err) {
			return nil, err
		}
	}
	id, ok := ParseLabel(req.GetCode())
	if !ok {
		// Not a label: perhaps an asset tag typed or printed as a barcode.
		a, err := t.db.Asset.Query().Where(asset.TenantId(t.tenant.Uuid()), asset.Tag(strings.TrimSpace(req.GetCode())), asset.DateErasedIsNil()).First(ctx)
		if err != nil {
			return nil, status.Error(codes.NotFound, "이 코드로 찾을 수 있는 것이 없습니다")
		}
		v, _, err := t.get(assetRef(a.Id), "code")
		if err != nil {
			return nil, err
		}
		return app.LabelResolveResponse_builder{Asset: v}.Build(), nil
	}
	if id.Domain() == pd.AssetDomain {
		v, _, err := t.get(app.AssetRef_builder{Id: id.Bytes()}.Build(), "code")
		if err != nil {
			return nil, err
		}
		return app.LabelResolveResponse_builder{Asset: v}.Build(), nil
	}

	return t.resolved(id)
}

// LabelWorkspace is the workspace a scanned code is in, whichever workspace it
// was scanned from: a label's by its code, or by its identifier for one
// printed before codes, or an asset's by its own -- or false for none. It reads
// past the wall, and is only for telling an account which of its own
// workspaces to look in (`GET /account/labels/{code}`).
func LabelWorkspace(ctx context.Context, db *ent.Client, v string) (pdid.Id, bool, error) {
	var (
		tenant uuid.UUID
		err    error
	)
	if c, ok := NormalLabelCode(v); ok {
		var l *ent.Label
		if l, err = db.Label.Query().Where(label.Code(c), label.DateErasedIsNil()).Only(ctx); err == nil {
			tenant = l.TenantId
		}
	} else if id, ok := ParseLabel(v); !ok {
		return pdid.Nil, false, nil
	} else if id.Domain() == pd.AssetDomain {
		var a *ent.Asset
		if a, err = db.Asset.Query().Where(asset.Id(id.Uuid()), asset.DateErasedIsNil()).Only(ctx); err == nil {
			tenant = a.TenantId
		}
	} else {
		var l *ent.Label
		if l, err = db.Label.Query().Where(label.Id(id.Uuid()), label.DateErasedIsNil()).Only(ctx); err == nil {
			tenant = l.TenantId
		}
	}
	switch {
	case ent.IsNotFound(err):
		return pdid.Nil, false, nil
	case err != nil:
		return pdid.Nil, false, err
	}
	return pdid.Id(tenant), true, nil
}

// resolved is what a label of this tenant's answers to a scan: itself, and
// what it is stuck on.
func (t *Tx) resolved(id pdid.Id) (*app.LabelResolveResponse, error) {
	l, err := t.next.Label().Get(t.ctx, app.LabelGetRequest_builder{Ref: app.LabelRef_builder{Id: id.Bytes()}.Build()}.Build())
	if err != nil {
		return nil, err
	}
	out := app.LabelResolveResponse_builder{Label: l}.Build()
	if s := idOf(l.GetSubjectId()); !s.IsZero() && l.GetState() == "bound" {
		if v, _, err := t.get(app.AssetRef_builder{Id: s.Bytes()}.Build(), "subject"); err == nil {
			out.SetAsset(v)
		}
	}
	return out, nil
}

type domainAttachment struct {
	Domain
	app.AttachmentServiceServer
}

func (s Domain) Attachment() app.AttachmentServiceServer {
	return domainAttachment{s, s.Next().Attachment()}
}

// Erase is the generated one, which begins no operation, held to what a
// read-only workspace does (D64). The file stays until retention or the
// workspace's destruction takes it (design 7.4).
func (s domainAttachment) Erase(ctx context.Context, ref *app.AttachmentRef) (*app.AttachmentEraseResponse, error) {
	return passing(s.Domain, ctx, func(t *Tx) (*app.AttachmentEraseResponse, error) { return t.next.Attachment().Erase(t.ctx, ref) })
}

// MaxUpload is the largest file an upload takes.
const MaxUpload = 20 << 20

// Upload keeps a file for something: an asset, a custody, a work order, a
// purchase, a count finding.
func (s domainAttachment) Upload(ctx context.Context, req *app.AttachmentUploadRequest) (*app.Attachment, error) {
	var out *app.Attachment
	err := s.tx(ctx, func(t *Tx) error {
		if t.deps.Files == nil {
			return failed("이 서버는 파일을 보관하지 않습니다")
		}
		// Before the file is kept, which the store would keep whatever the
		// transaction came to (D60, D64).
		if err := t.held(); err != nil {
			return err
		}
		data := req.GetData()
		if len(data) == 0 {
			return invalid("data", "빈 파일입니다")
		}
		if len(data) > MaxUpload {
			return invalid("data", "파일은 %dMB까지입니다", MaxUpload>>20)
		}
		subject := idOf(req.GetSubjectId())
		if err := t.exists(subject); err != nil {
			return err
		}

		id := pdid.New(pd.AttachmentDomain)
		key := fmt.Sprintf("%s/%s/%s", t.tenant, t.now.Format("2006/01"), id)
		if _, err := t.deps.Files.Put(t.ctx, key, bytes.NewReader(data)); err != nil {
			return err
		}
		sum := sha256.Sum256(data)

		name := strings.TrimSpace(req.GetName())
		if name == "" {
			name = "file"
		}
		ct := req.GetContentType()
		if ct == "" {
			ct = "application/octet-stream"
		}
		if err := t.begin(nil, "attachment.add", subject, t.now, "", app.AttachmentAdded_builder{
			Attachment:  id.Bytes(),
			Subject:     subject.Bytes(),
			Name:        name,
			ContentType: ct,
			Size:        uint64(len(data)),
		}.Build()); err != nil {
			return err
		}
		v, err := t.next.Attachment().Add(t.ctx, app.AttachmentAddRequest_builder{
			Id:          id.Bytes(),
			Tenant:      t.tenantRef(),
			Name:        name,
			SubjectId:   subject.Bytes(),
			ObjectKey:   key,
			SizeBytes:   uint64(len(data)),
			ContentType: ct,
			Sha256:      sum[:],
		}.Build())
		if err != nil {
			t.deps.Files.Delete(context.WithoutCancel(t.ctx), key)
			return err
		}
		out = v
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// exists refuses a subject that is not this tenant's.
func (t *Tx) exists(id pdid.Id) error {
	u := id.Uuid()
	tid := t.tenant.Uuid()
	var ok bool
	var err error
	switch id.Domain() {
	case pd.AssetDomain:
		ok, err = t.db.Asset.Query().Where(asset.Id(u), asset.TenantId(tid), asset.DateErasedIsNil()).Exist(t.ctx)
	case pd.CustodyDomain:
		ok, err = t.db.Custody.Query().Where(custody.Id(u), custody.TenantId(tid)).Exist(t.ctx)
	case pd.WorkOrderDomain:
		ok, err = t.db.WorkOrder.Query().Where(workorder.Id(u), workorder.TenantId(tid)).Exist(t.ctx)
	case pd.PurchaseDomain:
		ok, err = t.db.Purchase.Query().Where(purchase.Id(u), purchase.TenantId(tid)).Exist(t.ctx)
	case pd.CountFindingDomain:
		ok, err = t.db.CountFinding.Query().Where(countfinding.Id(u), countfinding.TenantId(tid)).Exist(t.ctx)
	default:
		return invalid("subject_id", "첨부는 자산, 지급, 작업, 구매, 실사 결과에만 합니다")
	}
	if err != nil {
		return err
	}
	if !ok {
		return status.Error(codes.NotFound, "첨부할 대상을 찾을 수 없습니다")
	}
	return nil
}

// Url answers a short-lived address to download a file.
func (s domainAttachment) Url(ctx context.Context, req *app.AttachmentUrlRequest) (*app.AttachmentUrlResponse, error) {
	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	v, err := t.next.Attachment().Get(ctx, app.AttachmentGetRequest_builder{Ref: req.GetRef()}.Build())
	if err != nil {
		return nil, err
	}
	// On the workspace's own domain while it has one, which serves its
	// files; at the service's address otherwise.
	at, err := t.deps.Links.Of(ctx, t.db, t.tenant, apphost.File)
	if err != nil {
		return nil, err
	}
	u, exp := t.deps.Sign.URL(at, v.GetObjectKey(), v.GetName(), t.now)
	return app.AttachmentUrlResponse_builder{Url: u, ExpiresAt: ts(exp)}.Build(), nil
}
