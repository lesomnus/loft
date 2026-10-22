package domain

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"github.com/protobuf-orm/ent/dialect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/holder"
)

// A workspace closing (D29, design 8.4).
//
//	close → restore window (30 days) → destroy → address held (a year) → free
//	           ↑ restore                 ↑ a legal hold waits it out
//
// Closing destroys nothing: the workspace's rows stay as they are, nobody but
// its owner gets in (`server/policy`), and the owner only to restore it or take
// its data out. Destroying is the deployment's, once the window is over and
// nothing holds it (`server/closing`). Whoever closes it -- the owner, an
// operator, or the deployment when an account leaving left nobody in it -- the
// window is the same.

// The states of a workspace an event names.
const (
	ClosedByOwner      = "owner"
	ClosedByOperator   = "operator"
	ClosedByDeployment = "deployment"
)

type domainTenant struct {
	Domain
	app.TenantServiceServer
}

func (s Domain) Tenant() app.TenantServiceServer { return domainTenant{s, s.Next().Tenant()} }

// Patch is a workspace's own settings, less what is not its owner's to write:
// its address, which nobody chose and only an operator moves (`rove tenant
// alias`), and whether it is closed, which Close and Restore say. Its zone,
// currency and check-in rule are its own (#144), and are held to what can be
// told in; and how long it keeps its history (D36), which a grace holds a
// shorter window to: all of them Configure's.
func (s domainTenant) Patch(ctx context.Context, req *app.TenantPatchRequest) (*app.Tenant, error) {
	if req.HasTimezone() || req.HasCurrency() || req.HasNoShowMinutes() {
		return nil, invalid("timezone", "시간대, 통화와 노쇼 해제는 TenantService.Configure로 정합니다")
	}
	if req.HasKeepDays() || req.HasAccessKeepDays() || req.HasRestoreDays() ||
		req.HasKeepDaysBefore() || req.HasDateKeepShortened() || req.GetDateKeepShortenedNull() ||
		req.HasAccessKeepDaysBefore() || req.HasDateAccessKeepShortened() || req.GetDateAccessKeepShortenedNull() {
		return nil, invalid("keep_days", "보존 기간과 복구 기간은 TenantService.Configure로 정합니다")
	}
	if req.HasAlias() {
		return nil, invalid("alias", "워크스페이스 주소는 바꿀 수 없습니다. 바꿔야 한다면 운영자에게 요청하세요")
	}
	if req.HasDateClosed() || req.GetDateClosedNull() || req.HasClosedBy() || req.HasCloseReason() || req.HasDateDestroy() || req.GetDateDestroyNull() {
		return nil, invalid("date_closed", "닫기와 복구는 TenantService.Close와 Restore로 합니다")
	}
	return s.TenantServiceServer.Patch(ctx, req)
}

// Configure is the workspace's own settings, its owner's to say (#144): its
// zone, its currency, how long a room waits for whoever booked it, and how
// long it keeps its history and its access record and may be restored once
// closed (D36). One operation, with an event that says what it came to -- a
// window made shorter, from when it is.
func (s domainTenant) Configure(ctx context.Context, req *app.TenantConfigureRequest) (*app.Tenant, error) {
	if req.HasTimezone() {
		if err := checkZone(req.GetTimezone()); err != nil {
			return nil, invalid("timezone", "시간대가 올바르지 않습니다 (예: Asia/Seoul)")
		}
	}
	if req.HasCurrency() {
		if err := priced(req.GetCurrency()); err != nil {
			return nil, err
		}
	}
	if req.HasNoShowMinutes() && req.GetNoShowMinutes() > maxNoShowMinutes {
		return nil, invalid("no_show_minutes", "노쇼 해제는 %d분(하루) 안에서 정합니다. 체크인을 받지 않으려면 음수를 넣으세요", maxNoShowMinutes)
	}
	if err := s.deps.checkKeep(days(req.HasKeepDays(), req.GetKeepDays()), days(req.HasAccessKeepDays(), req.GetAccessKeepDays()), days(req.HasRestoreDays(), req.GetRestoreDays())); err != nil {
		return nil, err
	}

	var out *app.Tenant
	err := s.tx(ctx, func(t *Tx) error {
		if err := t.mine(req.GetRef()); err != nil {
			return err
		}

		patch := app.TenantPatchRequest_builder{Ref: t.tenantRef(), DateUpdatedForce: z.Ptr(true)}.Build()
		said := map[string]string{}
		if req.HasTimezone() {
			patch.SetTimezone(req.GetTimezone())
			said["timezone"] = req.GetTimezone()
		}
		if req.HasCurrency() {
			patch.SetCurrency(req.GetCurrency())
			said["currency"] = req.GetCurrency()
		}
		if req.HasNoShowMinutes() {
			patch.SetNoShowMinutes(req.GetNoShowMinutes())
			said["no_show_minutes"] = fmt.Sprint(req.GetNoShowMinutes())
		}
		if err := t.keepPatch(req, patch, said); err != nil {
			return err
		}
		if len(said) == 0 {
			v, err := t.next.Tenant().Get(t.ctx, app.TenantGetRequest_builder{Ref: t.tenantRef()}.Build())
			out = v
			return err
		}

		if err := t.begin(nil, "workspace.configure", pdid.Nil, t.now, "워크스페이스 설정 변경", "", said); err != nil {
			return err
		}
		v, err := t.next.Tenant().Patch(t.ctx, patch)
		out = v
		return err
	})
	return out, err
}

// Close is the owner closing their workspace. They type its name, which is
// what keeps a click from doing it.
func (s domainTenant) Close(ctx context.Context, req *app.TenantCloseRequest) (*app.Tenant, error) {
	var out *app.Tenant
	err := s.tx(ctx, func(t *Tx) error {
		if err := t.mine(req.GetRef()); err != nil {
			return err
		}
		cur, err := t.next.Tenant().Get(t.ctx, app.TenantGetRequest_builder{Ref: t.tenantRef()}.Build())
		if err != nil {
			return err
		}
		if strings.TrimSpace(req.GetConfirm()) != strings.TrimSpace(cur.GetName()) {
			return invalid("confirm", "닫으려면 워크스페이스 이름(%s)을 그대로 입력하세요", cur.GetName())
		}
		out, err = t.closeWorkspace(ClosedByOwner, strings.TrimSpace(req.GetReason()))
		return err
	})
	return out, err
}

// Restore is the owner opening their closed workspace again.
func (s domainTenant) Restore(ctx context.Context, req *app.TenantRestoreRequest) (*app.Tenant, error) {
	var out *app.Tenant
	err := s.tx(ctx, func(t *Tx) error {
		if err := t.mine(req.GetRef()); err != nil {
			return err
		}
		var err error
		out, err = t.restoreWorkspace()
		return err
	})
	return out, err
}

// CloseWorkspace is a workspace closed as the deployment's own work: by an
// operator, or because an account leaving left nobody in it.
func (d *Deps) CloseWorkspace(ctx context.Context, server app.Server, drv dialect.Driver, tenant pdid.Id, by, reason string) (*app.Tenant, error) {
	var out *app.Tenant
	err := d.System(ctx, server, drv, tenant, func(t *Tx) error {
		var err error
		out, err = t.closeWorkspace(by, reason)
		return err
	})
	return out, err
}

// RestoreWorkspace is a closed workspace opened again by an operator.
func (d *Deps) RestoreWorkspace(ctx context.Context, server app.Server, drv dialect.Driver, tenant pdid.Id) (*app.Tenant, error) {
	var out *app.Tenant
	err := d.System(ctx, server, drv, tenant, func(t *Tx) error {
		var err error
		out, err = t.restoreWorkspace()
		return err
	})
	return out, err
}

// NameOwner makes a member of a workspace one of its owners, as the deployment's
// own work: an operator's, for a workspace whose owners are gone or who asked
// (decision 17). Whoever owned it still does. The member is named by its
// membership; one that is an owner already is left as it is.
func (d *Deps) NameOwner(ctx context.Context, server app.Server, drv dialect.Driver, tenant, member pdid.Id) error {
	return d.System(ctx, server, drv, tenant, func(t *Tx) error {
		h, err := t.db.Holder.Query().Where(holder.Id(member.Uuid()), holder.TenantId(tenant.Uuid()), holder.DateErasedIsNil()).Only(t.ctx)
		if ent.IsNotFound(err) {
			return status.Error(codes.NotFound, "그런 구성원이 없습니다")
		}
		if err != nil {
			return err
		}
		switch {
		case h.Role == "owner":
			return nil
		case h.DateLeaving != nil:
			return failed("떠나는 중인 구성원은 소유자가 될 수 없습니다")
		case h.Account == nil:
			return failed("계정이 없는 구성원은 소유자가 될 수 없습니다")
		}
		if err := t.begin(nil, "party.role", pdid.Nil, t.now, "소유자 지정: {holder} → owner (운영자)", "", map[string]string{
			"role": "owner", "holder": member.String(),
		}); err != nil {
			return err
		}
		_, err = t.next.Holder().Patch(t.ctx, app.HolderPatchRequest_builder{
			Ref:              app.HolderRef_builder{Id: member.Bytes()}.Build(),
			Role:             z.Ptr("owner"),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
		return err
	})
}

// restoreWithin is how long this workspace may be restored once closed: its
// own, or the deployment's (#144, D36).
func (t *Tx) restoreWithin() time.Duration {
	if w, err := t.window(); err == nil && w.Restore > 0 {
		return w.Restore
	}

	return t.deps.restoreWithin()
}

// restoreWithin is how long a closed workspace may be restored, where it
// says nothing.
func (d *Deps) restoreWithin() time.Duration {
	if d == nil || d.RestoreWithin <= 0 {
		return 30 * 24 * time.Hour
	}
	return d.RestoreWithin
}

// aliasHold is how long a destroyed workspace's address is kept from anybody
// else.
func (d *Deps) aliasHold() time.Duration {
	if d == nil || d.AliasHold <= 0 {
		return 365 * 24 * time.Hour
	}
	return d.AliasHold
}

// mine refuses a reference to any workspace but the caller's own.
func (t *Tx) mine(ref *app.TenantRef) error {
	if ref == nil {
		return nil
	}
	if id := idOf(ref.GetId()); !id.IsZero() && id != t.tenant {
		return invalid("ref", "자기 워크스페이스만 닫거나 복구합니다")
	}
	if a := ref.GetAlias(); a != "" {
		cur, err := t.next.Tenant().Get(t.ctx, app.TenantGetRequest_builder{Ref: t.tenantRef()}.Build())
		if err != nil {
			return err
		}
		if cur.GetAlias() != a {
			return invalid("ref", "자기 워크스페이스만 닫거나 복구합니다")
		}
	}
	return nil
}

func (t *Tx) closeWorkspace(by, reason string) (*app.Tenant, error) {
	cur, err := t.next.Tenant().Get(t.ctx, app.TenantGetRequest_builder{Ref: t.tenantRef()}.Build())
	if err != nil {
		return nil, err
	}
	if cur.HasDateClosed() {
		return nil, failed("이미 닫힌 워크스페이스입니다")
	}
	destroy := t.now.Add(t.restoreWithin())
	desc := "워크스페이스 닫기"
	switch by {
	case ClosedByOperator:
		desc += " (운영자)"
	case ClosedByDeployment:
		desc += " (남은 구성원 없음)"
	}
	if err := t.begin(nil, "workspace.close", pdid.Nil, t.now, desc, reason, map[string]string{"by": by, "destroy": destroy.UTC().Format(time.RFC3339)}); err != nil {
		return nil, err
	}
	return t.next.Tenant().Patch(t.ctx, app.TenantPatchRequest_builder{
		Ref:              t.tenantRef(),
		DateClosed:       ts(t.now),
		ClosedBy:         z.Ptr(by),
		CloseReason:      z.Ptr(reason),
		DateDestroy:      ts(destroy),
		DateUpdatedForce: z.Ptr(true),
	}.Build())
}

func (t *Tx) restoreWorkspace() (*app.Tenant, error) {
	cur, err := t.next.Tenant().Get(t.ctx, app.TenantGetRequest_builder{Ref: t.tenantRef()}.Build())
	if err != nil {
		return nil, err
	}
	if !cur.HasDateClosed() {
		return nil, failed("닫힌 워크스페이스가 아닙니다")
	}
	if cur.HasDateDestroy() && !t.now.Before(cur.GetDateDestroy().AsTime()) {
		return nil, failed("복구 기간이 지났습니다")
	}
	if err := t.begin(nil, "workspace.restore", pdid.Nil, t.now, "워크스페이스 복구", "", nil); err != nil {
		return nil, err
	}
	return t.next.Tenant().Patch(t.ctx, app.TenantPatchRequest_builder{
		Ref:              t.tenantRef(),
		DateClosedNull:   z.Ptr(true),
		ClosedBy:         z.Ptr(""),
		CloseReason:      z.Ptr(""),
		DateDestroyNull:  z.Ptr(true),
		DateUpdatedForce: z.Ptr(true),
	}.Build())
}
