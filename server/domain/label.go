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
	"strings"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/asset"
	"github.com/lesomnus/loft/internal/ent/countfinding"
	"github.com/lesomnus/loft/internal/ent/custody"
	"github.com/lesomnus/loft/internal/ent/label"
	"github.com/lesomnus/loft/internal/ent/purchase"
	"github.com/lesomnus/loft/internal/ent/workorder"
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

type domainLabel struct {
	Domain
	app.LabelServiceServer
}

func (s Domain) Label() app.LabelServiceServer { return domainLabel{s, s.Next().Label()} }

// Print makes labels, with this deployment's own address and a code no other
// label in it has (design 9.9, D28).
func (s domainLabel) Print(ctx context.Context, req *app.LabelPrintRequest) (*app.LabelPrintResponse, error) {
	var out *app.LabelPrintResponse
	err := s.tx(ctx, func(t *Tx) error {
		// What every label of these prints: `<address>/l/<code>`.
		at := t.deps.Links.Served()
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
				Tenant:      t.tenantRef(),
				State:       "new",
				DatePrinted: ts(t.now),
				Batch:       batch,
				Code:        z.Ptr(code),
			}.Build()
			if subject != nil {
				req.SetSubjectId(subject)
				req.SetState("bound")
				req.SetDateBound(ts(t.now))
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
			DateBound:        ts(t.now),
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
			DateBoundNull:    z.Ptr(true),
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
	u, exp := t.deps.Sign.URL(t.deps.Links.Served(), v.GetObjectKey(), v.GetName(), t.now)
	return app.AttachmentUrlResponse_builder{Url: u, DateExpires: ts(exp)}.Build(), nil
}
