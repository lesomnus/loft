package domain

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/identity"
	"github.com/lesomnus/loft/server/pd"
	"github.com/lesomnus/loft/server/tenancy"
)

// What an account has outside any one workspace, asked of `TenantService` and
// `LabelService` as the account itself (payday#184, D73): the workspaces it
// is in, a new one of its own, and which of them a label is in. They were
// HTTP beside `/session` while every RPC was served as a membership of one
// workspace; these are the questions asked before there is one.
//
// Each reads nothing through the walled servers -- there is no workspace to
// narrow by -- and what it reads past them is held here to the account's own:
// its memberships, as `tenancy.Workspaces` finds them by its identifier.
//
// A workspace's export is its owner's membership's instead, and so is no
// method of the account's: it is of one workspace, which it is asked in,
// open or closed (`cmd.WhileClosed`).

// caller is the account calling as itself (payday#184), which only a session
// is.
func caller(ctx context.Context) (pdid.Id, error) {
	f, ok := frame.From(ctx)
	if !ok || !f.AsAccount() || f.Account.IsZero() {
		return pdid.Nil, status.Error(codes.PermissionDenied, "계정으로 로그인한 사람이 자기 자신으로 묻는 것입니다")
	}
	return f.Account, nil
}

// workspaces is what an account's workspaces are read and made with, or the
// refusal of a deployment that put none together.
func (s Domain) workspaces() (*tenancy.Workspaces, error) {
	if s.deps == nil || s.deps.Workspaces == nil {
		return nil, status.Error(codes.Unimplemented, "이 배포는 계정의 워크스페이스를 답하지 않습니다")
	}
	return s.deps.Workspaces, nil
}

// membership is one workspace of an account's as it is told it, and whose
// personal data telling it shows -- the owner one with no name of its own is
// called after (D57) -- on the access record, as a read of theirs.
func (s Domain) membership(ctx context.Context, m tenancy.Membership) *app.TenantMembership {
	v := app.TenantMembership_builder{
		Id:    m.Workspace.Bytes(),
		Alias: m.Alias,
		Name:  m.Name,
		Role:  m.Role,
	}.Build()
	if m.Closed != nil {
		v.SetDateClosed(timestamppb.New(*m.Closed))
		v.SetAliasHoldDays(uint32(s.deps.AliasHold / (24 * time.Hour)))
	}
	if m.Destroy != nil {
		v.SetDateDestroy(timestamppb.New(*m.Destroy))
	}
	access.Saw(ctx, m.Saw...)

	return v
}

// Mine is every workspace the caller's account is in, in the order it came to
// be in them, and how many it owns and may own.
func (s domainTenant) Mine(ctx context.Context, _ *app.TenantMineRequest) (*app.TenantMineResponse, error) {
	ctx, err := pd.AccountReads(ctx, app.TenantService_Mine_FullMethodName, pd.Reads{})
	if err != nil {
		return nil, err
	}
	me, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	ws, err := s.workspaces()
	if err != nil {
		return nil, err
	}
	ms, err := ws.Of(ctx, me)
	if err != nil {
		return nil, err
	}

	out := &app.TenantMineResponse{}
	for _, m := range ms {
		out.SetWorkspaces(append(out.GetWorkspaces(), s.membership(ctx, m)))
		if m.Role == "owner" {
			out.SetOwns(out.GetOwns() + 1)
		}
	}
	if ws.Owned >= 0 {
		out.SetMay(uint32(ws.Owned))
	}

	return out, nil
}

// Create makes a workspace the caller's account owns, as the service's page
// does: called what it asks, at an address nobody chose, and with what an
// asset register starts with (`tenancy.Workspaces.Create`). One owning as many
// as it may without paying for another is refused, as levyan is to say
// otherwise.
func (s domainTenant) Create(ctx context.Context, req *app.TenantCreateRequest) (*app.TenantMembership, error) {
	ctx, err := pd.AccountReads(ctx, app.TenantService_Create_FullMethodName, pd.Reads{})
	if err != nil {
		return nil, err
	}
	me, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	if name == "" || !tenancy.Fits(name) {
		return nil, invalid("name", "이름은 한 줄로, 1~%d자로 씁니다", tenancy.MaxName)
	}
	ws, err := s.workspaces()
	if err != nil {
		return nil, err
	}
	if s.deps.People == nil {
		return nil, status.Error(codes.Unimplemented, "이 배포는 계정을 알지 못합니다")
	}
	p, err := s.deps.People.ById(ctx, me.String())
	switch {
	case errors.Is(err, identity.ErrNoPerson):
		return nil, status.Error(codes.Unauthenticated, "who is asking?")
	case errors.Is(err, identity.ErrUnreachable):
		return nil, status.Error(codes.Unavailable, "cannot say just now")
	case err != nil:
		return nil, err
	}

	m, err := ws.Create(ctx, p, name)
	if errors.Is(err, tenancy.ErrOwnsEnough) {
		return nil, failed("워크스페이스를 더 만들려면 결제가 필요합니다")
	}
	if err != nil {
		return nil, err
	}

	return s.membership(ctx, m), nil
}

// exportPart is how much of an export one message holds.
const exportPart = 512 << 10

// Export is everything the workspace has, as one zip, for its owner to take
// out (D29, D34): what `loft tenant export` writes, written whole before its
// first part goes, so that the moment it is read in is held while the
// database is read and not while somebody takes their time over the download,
// and one that fails is an answer that says so rather than a zip that stops
// part of the way (loft#120).
//
// It is a download of everybody in it (`cmd.Downloads`), and says so before
// anything is read for it (loft#136): its first message is what the file is
// called, and the access record's row of whose data it carries -- by the
// owner's membership, from where, and what for -- is written before that
// message leaves, or nothing does. So the export holds its own row. The
// reason is the interceptor's to have asked for.
func (s domainTenant) Export(req *app.TenantExportRequest, out grpc.ServerStreamingServer[app.TenantExportResponse]) error {
	ctx := out.Context()
	if s.deps == nil || s.deps.Export == nil || s.deps.Carried == nil {
		return status.Error(codes.Unimplemented, "이 배포는 워크스페이스를 내보내지 않습니다")
	}
	t, err := s.read(ctx)
	if err != nil {
		return err
	}
	if t.tenant.IsZero() {
		return status.Error(codes.PermissionDenied, "워크스페이스의 소유자가 그 워크스페이스에서 내보냅니다")
	}
	if err := t.mine(req.GetRef()); err != nil {
		return err
	}
	cur, err := t.db.Tenant.Get(ctx, t.tenant.Uuid())
	if err != nil {
		return err
	}

	people, err := s.deps.Carried(ctx, t.tenant)
	if err != nil {
		return err
	}
	access.Saw(ctx, people...)
	if err := out.Send(app.TenantExportResponse_builder{
		Name:        fmt.Sprintf("%s-%s.zip", cur.Alias, t.now.In(t.zone()).Format("20060102")),
		ContentType: "application/zip",
	}.Build()); err != nil {
		return err
	}

	f, err := os.CreateTemp("", "loft-export-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := s.deps.Export(ctx, t.tenant, f); err != nil {
		return fmt.Errorf("export of @%s: %w", cur.Alias, err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	for {
		// A part of its own each time: a message sent is not the sender's to
		// change, whatever reads it after.
		buf := make([]byte, exportPart)
		n, err := io.ReadFull(f, buf)
		if n > 0 {
			if err := out.Send(app.TenantExportResponse_builder{Data: buf[:n]}.Build()); err != nil {
				return err
			}
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// Locate is which of the caller's account's workspaces a scanned code is a
// label in (D28). One of a workspace it is not in reads as no label at all,
// and its memberships are asked first either way, so that the two take the
// same asking.
func (s domainLabel) Locate(ctx context.Context, req *app.LabelLocateRequest) (*app.TenantMembership, error) {
	ctx, err := pd.AccountReads(ctx, app.LabelService_Locate_FullMethodName, pd.Reads{})
	if err != nil {
		return nil, err
	}
	me, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	ws, err := s.workspaces()
	if err != nil {
		return nil, err
	}
	ms, err := ws.Of(ctx, me)
	if err != nil {
		return nil, err
	}

	db := ent.NewClient(ent.Driver(s.drv))
	in, at, err := LabelWorkspace(ctx, db, req.GetCode())
	if err != nil {
		return nil, err
	}
	for _, m := range ms {
		if at && m.Workspace == in {
			return s.membership(ctx, m), nil
		}
	}

	return nil, status.Error(codes.NotFound, "no workspace of yours has this label")
}
