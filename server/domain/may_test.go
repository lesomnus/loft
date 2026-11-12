package domain_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/server/policy"
)

// personWith is somebody bound at roster to a role the tenant made for itself
// there: what a member holds, and `more` (D89). Loft knows nothing of the role
// but what roster answers of the methods it holds.
func (e *env) personWith(more ...string) (context.Context, *app.Party) {
	e.t.Helper()
	ctx := context.Background()
	e.n++
	member, _ := policy.RoleOf("member")
	alias := fmt.Sprintf("ours-%d", e.n)
	_, err := e.s.Identity.WriteRoles(ctx, []policy.Role{{Alias: alias, Name: alias, Methods: slices.Concat(member.Methods, more)}})
	e.x.NoError(err)
	p, err := e.app().Party().Add(e.owner, app.PartyAddRequest_builder{Name: fmt.Sprintf("%s %d", alias, e.n), Kind: "person"}.Build())
	e.x.NoError(err)
	as, v := e.admit(p, fmt.Sprintf("p%d", e.n), "")
	e.x.NoError(e.s.Identity.Bind(ctx, alias, idOf(v.GetHolder().GetId())))
	return as, v
}

// TestEachPermissionIsWhatItSays (D89): a method of `PermissionService` is a
// name a role holds at roster, finer than one RPC -- the same act over
// somebody else's -- and the domain asks roster whether the caller may call
// it, never which role they hold. So a role the tenant made for itself, what a
// member holds and that one permission, does what the permission says, and a
// member's does not.
func TestEachPermissionIsWhatItSays(t *testing.T) {
	e := newEnv(t)
	room := e.space("회의실", nil)
	e.bookable(room, nil)
	laptop := e.item("노트북", "L-1", room, time.Hour)
	kim, kimP := e.person("member")

	cases := []struct {
		perm string
		// ready makes ready what the permission is for, and answers the act:
		// refused without it, and done with it.
		ready func(t *testing.T) func(ctx context.Context) error
	}{
		{app.PermissionService_BookForOthers_FullMethodName, func(t *testing.T) func(context.Context) error {
			return func(ctx context.Context) error {
				_, err := e.reserve(ctx, room, e.tomorrow(8, 0), e.tomorrow(9, 0), func(r *app.ReservationAddRequest) {
					r.SetParty(ref[app.PartyRef](kimP.GetId()))
				})
				return err
			}
		}},
		{app.PermissionService_BookOverConflicts_FullMethodName, func(t *testing.T) func(context.Context) error {
			_, err := e.reserve(kim, room, e.tomorrow(10, 0), e.tomorrow(11, 0), nil)
			require.NoError(t, err)
			return func(ctx context.Context) error {
				_, err := e.reserve(ctx, room, e.tomorrow(10, 0), e.tomorrow(11, 0), func(r *app.ReservationAddRequest) {
					r.SetOverride(true)
					r.SetReason("임원 회의")
				})
				return err
			}
		}},
		{app.PermissionService_ActOnOthersBookings_FullMethodName, func(t *testing.T) func(context.Context) error {
			r, err := e.reserve(kim, room, e.tomorrow(13, 0), e.tomorrow(14, 0), nil)
			require.NoError(t, err)
			return func(ctx context.Context) error {
				_, err := e.app().Reservation().Cancel(ctx, app.ReservationDecideRequest_builder{Ref: ref[app.ReservationRef](r.GetId())}.Build())
				return err
			}
		}},
		{app.PermissionService_ReadEveryReservation_FullMethodName, func(t *testing.T) func(context.Context) error {
			r, err := e.reserve(kim, room, e.tomorrow(15, 0), e.tomorrow(16, 0), nil)
			require.NoError(t, err)
			return func(ctx context.Context) error {
				_, err := e.app().Reservation().Timeline(ctx, app.ReservationTimelineRequest_builder{Ref: ref[app.ReservationRef](r.GetId())}.Build())
				return err
			}
		}},
		{app.PermissionService_AcknowledgeForOthers_FullMethodName, func(t *testing.T) func(context.Context) error {
			c := e.issue(kimP, laptop)
			return func(ctx context.Context) error {
				_, err := e.app().Custody().Acknowledge(ctx, app.CustodyAcknowledgeRequest_builder{Ref: ref[app.CustodyRef](c.GetId())}.Build())
				return err
			}
		}},
		{app.PermissionService_ScanAnyCount_FullMethodName, func(t *testing.T) func(context.Context) error {
			c, err := e.app().InventoryCount().Add(e.owner, app.InventoryCountAddRequest_builder{Scope: assetRef(room)}.Build())
			require.NoError(t, err)
			return func(ctx context.Context) error {
				_, err := e.app().InventoryCount().Scan(ctx, app.InventoryCountScanRequest_builder{Ref: ref[app.InventoryCountRef](c.GetId()), Code: "L-1", Op: newOp()}.Build())
				return err
			}
		}},
		{app.PermissionService_RunWork_FullMethodName, func(t *testing.T) func(context.Context) error {
			return func(ctx context.Context) error {
				_, err := e.app().WorkOrder().Add(ctx, app.WorkOrderAddRequest_builder{Asset: assetRef(laptop), Kind: "inspection", Name: "정기 점검"}.Build())
				return err
			}
		}},
	}

	tried := []string{}
	for _, c := range cases {
		tried = append(tried, c.perm)
		t.Run(strings.TrimPrefix(c.perm, "/loft.PermissionService/"), func(t *testing.T) {
			x := require.New(t)
			act := c.ready(t)
			without, _ := e.personWith()
			err := act(without)
			x.Equal(codes.PermissionDenied, codeOf(err), "what a member holds")
			x.NotContains(status.Convert(err).Message(), "(/loft.", "refused for the permission, and not at the gate: %v", err)
			with, _ := e.personWith(c.perm)
			x.NoError(act(with), "and %s", c.perm)
		})
	}

	sd := app.File_loft_permission_proto.Services().ByName("PermissionService")
	for i := range sd.Methods().Len() {
		m := "/loft.PermissionService/" + string(sd.Methods().Get(i).Name())
		require.Contains(t, tried, m, "a permission nothing here tries")
	}
}

// TestWhoIsToldIsWhoMayAct (D89): a notice of something to act on goes to
// whoever roster says may act on it -- asked by the method that acts -- and
// not to a role by its name: somebody whose role the tenant made to approve
// reservations is told one waits for them, and a member is not.
func TestWhoIsToldIsWhoMayAct(t *testing.T) {
	e := newEnv(t)
	x := e.x
	room := e.space("회의실", nil)
	e.bookable(room, func(r *app.BookableAddRequest) { r.SetApproval(true) })
	approver, _ := e.personWith(app.ReservationService_Approve_FullMethodName)
	member, _ := e.person("member")
	kim, _ := e.person("member")

	r, err := e.reserve(kim, room, e.tomorrow(10, 0), e.tomorrow(11, 0), nil)
	x.NoError(err)
	x.Equal("requested", r.GetStatus())
	x.NotEmpty(e.notices(approver, "reservation.requested"))
	x.Empty(e.notices(member, "reservation.requested"))
}
