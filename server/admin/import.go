package admin

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/lesomnus/payday/pdid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/server/offboard"
	"github.com/lesomnus/loft/server/operator"
)

// MaxImport is the largest export the entry point takes in: a workspace's
// every row and file, which is large, and not without end.
var MaxImport int64 = 16 << 30

// Import is a workspace back from its signed export, into a deployment that
// does not have it (D34), as `offboard.Deployment.Import` puts one back: the
// export is what the caller sends, spooled to a file only this process reads
// -- an archive is read out of order, and a stream only once -- and taken
// away again when the call ends. Nothing in it is believed before its
// signature is (`offboard.Verify`), by a key this deployment trusts.
//
// Once put back, what it had out holds its time as anything out does (D53),
// and what levyan grants it is copied now rather than at the follower's next
// comparison (design 7.5).
func (s operatorTenants) Import(in grpc.ClientStreamingServer[app.OperatorTenantImportRequest, app.OperatorTenantImportResponse]) error {
	ctx := in.Context()
	first, err := in.Recv()
	if err != nil {
		return err
	}
	if !first.HasOptions() {
		return status.Error(codes.InvalidArgument, "options: the first message says how, and the export's bytes follow")
	}
	o := first.GetOptions()

	f, err := os.CreateTemp("", "loft-import-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	size := int64(0)
	for {
		v, err := in.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if v.HasOptions() {
			return status.Error(codes.InvalidArgument, "options: said once, first")
		}
		size += int64(len(v.GetData()))
		if size > MaxImport {
			return status.Errorf(codes.ResourceExhausted, "an export of more than %d bytes", MaxImport)
		}
		if _, err := f.Write(v.GetData()); err != nil {
			return err
		}
	}
	if size == 0 {
		return status.Error(codes.InvalidArgument, "data: the export, which came empty")
	}

	x, err := s.a.S.Offboard(s.a.C).Import(operator.Frame(ctx, pdid.Nil), f, size, offboard.ImportOptions{
		Alias: o.GetAlias(), Reuse: o.GetReuse(), Dry: o.GetDryRun(),
	})
	if err != nil {
		return refused(err)
	}

	out := app.OperatorTenantImportResponse_builder{
		Tenant:   x.Tenant.Bytes(),
		DryRun:   o.GetDryRun(),
		Alias:    x.Alias,
		Asked:    x.Asked,
		Released: x.Released,
		Reopened: x.Reopened,
		Retired:  x.Retired,
		Returned: uint32(x.Returned),
		Rows:     counts(x.Rows),
		Left:     counts(x.Left),
		Files:    uint32(x.Files),
		Missing:  x.Missing,
		Unlinked: x.Unlinked,
		Manifest: x.Manifest,
		Key:      x.Key,
	}.Build()
	why := map[string]string{}
	for k := range x.Left {
		why[k] = offboard.Dropped[k]
	}
	out.SetLeftWhy(why)
	if o.GetDryRun() {
		return in.SendAndClose(out)
	}

	held, err := s.a.S.Deps.HoldOut(operator.Frame(ctx, x.Tenant), s.a.S.Base, s.a.S.Drv, x.Tenant)
	if err != nil {
		return fmt.Errorf("what @%s has out: %w", x.Alias, err)
	}
	out.SetHeld(uint32(held))
	if s.a.S.Follower != nil {
		if _, err := s.a.S.Follower.Sync(ctx, x.Tenant); err != nil {
			out.SetGrants(fmt.Sprintf("copied at the next comparison, as levyan could not be asked now: %v", err))
		} else {
			out.SetGrants("copied")
		}
	}
	return in.SendAndClose(out)
}
