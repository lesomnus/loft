package domain

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"uuid"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/asset"
	"github.com/lesomnus/rove/internal/ent/countfinding"
	"github.com/lesomnus/rove/internal/ent/custody"
	"github.com/lesomnus/rove/internal/ent/label"
	"github.com/lesomnus/rove/internal/ent/purchase"
	"github.com/lesomnus/rove/internal/ent/tenantdomain"
	"github.com/lesomnus/rove/internal/ent/workorder"
	"github.com/lesomnus/rove/server/pd"
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

// labelURL answers the address a label prints: on a tenant's own `host`
// (D23), or on this deployment's own address when there is none (D28).
func (d *Deps) labelURL(host string, code string) string {
	if host == "" {
		return strings.TrimSuffix(d.LabelBase, "/") + "/l/" + code
	}
	scheme := d.LabelScheme
	if scheme == "" {
		scheme = "https"
	}
	h := host
	if p := d.LabelPort; p != "" && !(scheme == "https" && p == "443") && !(scheme == "http" && p == "80") {
		h = host + ":" + p
	}
	return fmt.Sprintf("%s://%s/l/%s", scheme, h, code)
}

// The states a tenant domain is in (design 9.9).
const (
	domainPending = "pending"
	domainReady   = "ready"
	domainActive  = "active"
	domainLegacy  = "legacy"
	domainRetired = "retired"
)

var hostOk = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

type domainTenantDomain struct {
	Domain
	app.TenantDomainServiceServer
}

func (s Domain) TenantDomain() app.TenantDomainServiceServer {
	return domainTenantDomain{s, s.Next().TenantDomain()}
}

// Add registers a label domain: the tenant's own, to verify with a TXT
// record, or a default subdomain of this deployment, verified already. Where
// tenants have no domains of their own (D28) there is nothing to add one for.
func (s domainTenantDomain) Add(ctx context.Context, req *app.TenantDomainAddRequest) (*app.TenantDomain, error) {
	var out *app.TenantDomain
	err := s.tx(ctx, func(t *Tx) error {
		if !t.deps.LabelTenantDomains {
			return failed("이 서비스에서는 라벨이 서비스 주소로 인쇄되어 워크스페이스별 라벨 도메인이 없습니다")
		}
		row := app.TenantDomainAddRequest_builder{
			Tenant:  t.tenantRef(),
			Purpose: "label",
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
			host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(req.GetHost())), ".")
			if !hostOk.MatchString(host) {
				return invalid("host", "호스트 이름을 적어 주세요 (예: assets.example.com)")
			}
			if s := t.deps.LabelSuffix; s != "" && (host == s || strings.HasSuffix(host, "."+s)) {
				return invalid("host", "%s의 하위 주소는 기본 주소로 추가하세요", s)
			}
			row.SetHost(host)
			row.SetSource("custom")
			row.SetState(domainPending)
		}

		if err := t.begin(nil, "domain.add", pdid.Nil, t.now, "라벨 도메인 등록: "+row.GetHost(), "", nil); err != nil {
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
	return "rove-" + code32.EncodeToString(b)
}

func (t *Tx) activeDomain() (*ent.TenantDomain, error) {
	v, err := t.db.TenantDomain.Query().Where(
		tenantdomain.TenantId(t.tenant.Uuid()),
		tenantdomain.Purpose("label"),
		tenantdomain.State(domainActive),
		tenantdomain.DateErasedIsNil(),
	).First(t.ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	return v, err
}

func (t *Tx) activate(id []byte) (*app.TenantDomain, error) {
	cur, err := t.activeDomain()
	if err != nil {
		return nil, err
	}
	if cur != nil && pdid.Id(cur.Id) == idOf(id) {
		return t.next.TenantDomain().Get(t.ctx, app.TenantDomainGetRequest_builder{Ref: app.TenantDomainRef_builder{Id: id}.Build()}.Build())
	}
	if cur != nil {
		if _, err := t.next.TenantDomain().Patch(t.ctx, app.TenantDomainPatchRequest_builder{
			Ref:              app.TenantDomainRef_builder{Id: pdid.Id(cur.Id).Bytes()}.Build(),
			State:            z.Ptr(domainLegacy),
			DateUpdatedForce: z.Ptr(true),
		}.Build()); err != nil {
			return nil, err
		}
	}
	return t.next.TenantDomain().Patch(t.ctx, app.TenantDomainPatchRequest_builder{
		Ref:              app.TenantDomainRef_builder{Id: id}.Build(),
		State:            z.Ptr(domainActive),
		DateUpdatedForce: z.Ptr(true),
	}.Build())
}

// Verify checks a pending custom domain's TXT record.
func (s domainTenantDomain) Verify(ctx context.Context, req *app.TenantDomainVerifyRequest) (*app.TenantDomain, error) {
	var out *app.TenantDomain
	err := s.tx(ctx, func(t *Tx) error {
		v, err := t.next.TenantDomain().Get(t.ctx, app.TenantDomainGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		if v.GetState() != domainPending {
			out = v
			return nil
		}
		ok, err := t.deps.verify(t.ctx, v.GetHost(), v.GetToken())
		if err != nil {
			return status.Errorf(codes.Unavailable, "TXT 레코드를 읽을 수 없습니다: %v", err)
		}
		if !ok {
			return failed("_rove-challenge.%s에 TXT 레코드 %s가 아직 없습니다. DNS 반영에 시간이 걸릴 수 있습니다", v.GetHost(), v.GetToken())
		}
		if err := t.begin(nil, "domain.verify", pdid.Nil, t.now, "라벨 도메인 확인: "+v.GetHost(), "", nil); err != nil {
			return err
		}
		out, err = t.next.TenantDomain().Patch(t.ctx, app.TenantDomainPatchRequest_builder{
			Ref:              req.GetRef(),
			State:            z.Ptr(domainReady),
			VerifiedAt:       ts(t.now),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
		return err
	})
	return out, err
}

// verify answers whether `host` carries `token` in its challenge record.
func (d *Deps) verify(ctx context.Context, host, token string) (bool, error) {
	if d.LookupTXT == nil {
		return false, fmt.Errorf("this deployment cannot read DNS")
	}
	vs, err := d.LookupTXT(ctx, "_rove-challenge."+host)
	if err != nil {
		return false, err
	}
	return slices.Contains(vs, token), nil
}

// Activate makes a verified domain the one labels are printed with.
func (s domainTenantDomain) Activate(ctx context.Context, req *app.TenantDomainActivateRequest) (*app.TenantDomain, error) {
	var out *app.TenantDomain
	err := s.tx(ctx, func(t *Tx) error {
		v, err := t.next.TenantDomain().Get(t.ctx, app.TenantDomainGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		switch v.GetState() {
		case domainReady, domainLegacy, domainActive:
		default:
			return failed("%s은(는) 아직 확인되지 않았습니다. 확인된 도메인만 쓸 수 있습니다", v.GetHost())
		}
		if err := t.begin(nil, "domain.activate", pdid.Nil, t.now, "라벨 도메인 활성화: "+v.GetHost(), "", nil); err != nil {
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
		if err := t.begin(nil, "domain.retire", pdid.Nil, t.now, "라벨 도메인 은퇴: "+v.GetHost(), "", nil); err != nil {
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

// Status answers whether this tenant's labels are on.
func (s domainTenantDomain) Status(ctx context.Context, _ *app.TenantDomainStatusRequest) (*app.TenantDomainStatusResponse, error) {
	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	out := app.TenantDomainStatusResponse_builder{
		Target:        t.deps.LabelTarget,
		DefaultSuffix: t.deps.LabelSuffix,
		TenantDomains: t.deps.LabelTenantDomains,
		Base:          strings.TrimSuffix(t.deps.LabelBase, "/"),
		// Without domains of their own, every tenant's labels are on.
		Labels: !t.deps.LabelTenantDomains,
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
		out.SetLabels(true)
		out.SetActive(v)
	}
	return out, nil
}

type domainLabel struct {
	Domain
	app.LabelServiceServer
}

func (s Domain) Label() app.LabelServiceServer { return domainLabel{s, s.Next().Label()} }

// errLabelsOff is the answer while a tenant has no active label domain: a
// label printed without one would encode a host the tenant cannot keep.
func errLabelsOff() error {
	return failed("라벨 도메인이 없어 QR 라벨이 꺼져 있습니다. 설정에서 도메인을 추가하세요")
}

// Print makes labels: with the tenant's active domain where tenants have
// their own (D23), and with this deployment's own address otherwise (D28).
func (s domainLabel) Print(ctx context.Context, req *app.LabelPrintRequest) (*app.LabelPrintResponse, error) {
	var out *app.LabelPrintResponse
	err := s.tx(ctx, func(t *Tx) error {
		var dom *ent.TenantDomain
		if t.deps.LabelTenantDomains {
			var err error
			if dom, err = t.activeDomain(); err != nil {
				return err
			}
			if dom == nil {
				return errLabelsOff()
			}
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

		if err := t.begin(nil, "label.print", pdid.Nil, t.now, fmt.Sprintf("라벨 인쇄: %s", batch), "", map[string]string{"count": fmt.Sprint(max(n, len(ids)))}); err != nil {
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
			host := ""
			if dom != nil {
				req.SetDomain(app.TenantDomainRef_builder{Id: pdid.Id(dom.Id).Bytes()}.Build())
				host = dom.Host
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
			out.SetUrls(append(out.GetUrls(), t.deps.labelURL(host, code)))
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
		if t.deps.LabelTenantDomains {
			dom, err := t.activeDomain()
			if err != nil {
				return err
			}
			if dom == nil {
				return errLabelsOff()
			}
		}
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
		if err := t.begin(req.GetOp(), "label.bind", id, t.now, fmt.Sprintf("%s 라벨 부착", a.GetTag()), "", nil); err != nil {
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
		if err := t.begin(nil, "label.unbind", idOf(l.GetSubjectId()), t.now, "라벨 떼어냄", "", map[string]string{"state": state}); err != nil {
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
		if err := t.begin(nil, "attachment.add", subject, t.now, "파일 첨부: "+name, "", nil); err != nil {
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
	u, exp := t.deps.Sign.URL(v.GetObjectKey(), v.GetName(), t.now)
	return app.AttachmentUrlResponse_builder{Url: u, ExpiresAt: ts(exp)}.Build(), nil
}
