// Package levyan is this deployment's client of levyan, the billing service
// its workspaces are billed by (levyan#14), over one connection:
//
//   - what each workspace holds, as payday's meter reader measures it
//     (payday#186), handed to levyan's `levyan.v1.MeteringService` -- the
//     sink of `meter.Reader` (design 7.1);
//   - what levyan grants each workspace, which payday's follower keeps a copy
//     of from levyan's `payday.GrantService` (payday#185, design 7.5,
//     [Client.Grants]);
//   - who pays for each workspace, and what became of it -- closed, restored,
//     destroyed -- told to levyan's `levyan.v1.WorkspaceService` from a queue
//     written beside the change that tells it ([Notices], [Teller], design
//     7.6, 7.8).
//
// It speaks levyan's own API module (`github.com/lesomnus/levyan/api`), which
// is levyan's promise to the products it bills and holds Loft to nothing of
// levyan's: not its schema, and not the payday it is built with (levyan's
// `docs/design.md`, 8).
//
// # What becomes of a reading
//
// levyan keeps one reading of each workspace, meter and moment, and takes one
// sent again as the one it has: so a call that failed is a call made again as
// it was, which [Client.Send] leaves to the reader by answering its error. A
// reading levyan refused -- a meter its price book does not have, a unit that
// is not the meter's -- is refused for what it is, and would be refused
// again: it is logged and not sent again, and the rest of the call is kept.
package levyan

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	levyanv1 "github.com/lesomnus/levyan/api/levyan/v1"
	"github.com/lesomnus/otx/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/meter"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
)

// Config is levyan, and how it is reached: `app.billing.levyan`.
type Config struct {
	// Addr is levyan's gRPC address, `levyan:50051`: where `levyan serve`
	// listens (its `server.addr`). Empty is no billing: nothing is measured,
	// and a workspace is destroyed without being measured first -- a
	// deployment that bills nobody, as a checkout is.
	Addr string `yaml:"addr"`

	// Key is the product key levyan's operator issued this deployment
	// (`levyan-admin key issue`), which every call carries as
	// `authorization: Bearer lvk_…`: `env:NAME`, `file:PATH`, or the key
	// itself. Nothing says it: not a log, not an error.
	Key string `yaml:"key"`

	// Insecure dials levyan in plaintext, which is a cluster whose network
	// policy is the wall between the two -- as levyan's own reference
	// deployment serves it -- or a lab. Anything else is TLS, as Tls says.
	Insecure bool `yaml:"insecure"`

	// Tls is how levyan's certificate is checked, and what this deployment
	// presents to it: the system's roots unless `ca_file` names a CA, a
	// certificate of its own with `cert_file` and `key_file`, and
	// `server_name` for a certificate the address does not name. It is TLS
	// whatever `enabled` says, unless Insecure.
	Tls config.DialConfig `yaml:"tls"`

	// Timeout is how long one call may take before it is given up, and its
	// readings handed over again later. Unset is [DefaultTimeout].
	Timeout time.Duration `yaml:"timeout"`
}

// On says whether c names levyan.
func (c Config) On() bool { return c.Addr != "" }

// DefaultTimeout is how long one call to levyan may take when the
// configuration says nothing: a call is one workspace's readings, a handful,
// and the reader measures one workspace at a time -- so a levyan that does
// not answer holds each workspace of a pass this long, and no longer.
const DefaultTimeout = 10 * time.Second

// KeyPrefix is what every product key levyan issues begins with.
const KeyPrefix = "lvk_"

// Keepalive is how often the connection to levyan asks whether levyan is
// still there while a call is open on it, and Answer how long it waits to be
// told: the grants' stream is open for as long as the follower is, and quiet
// between changes, which a proxy's idle timeout would otherwise end (levyan's
// `docs/apps.md`). levyan's `server.keepalive.min_time` is to be shorter than
// Keepalive, or levyan hangs up on it (`too_many_pings`).
const (
	Keepalive = 5 * time.Minute
	Answer    = 20 * time.Second
)

// Client is this deployment's connection to levyan.
type Client struct {
	conn       *grpc.ClientConn
	metering   levyanv1.MeteringServiceClient
	grants     pdpb.GrantServiceClient
	workspaces levyanv1.WorkspaceServiceClient
	key        string
	timeout    time.Duration
}

var _ meter.Sink = (*Client)(nil)

// Dial is a client of the levyan c names, its key read by `resolve`
// (`identity.ResolveRef`). It connects when it is first called, so a levyan
// that is down is the first call failing, and not the deployment refusing to
// start.
func Dial(c Config, resolve func(string) (string, error)) (*Client, error) {
	if !c.On() {
		return nil, errors.New("app.billing.levyan.addr: no levyan to dial")
	}
	if c.Key == "" {
		return nil, errors.New("app.billing.levyan.key: levyan is called with the product key its operator issued (" + KeyPrefix + "…)")
	}
	key, err := resolve(c.Key)
	if err != nil {
		return nil, fmt.Errorf("app.billing.levyan.key: %w", err)
	}
	// Not what it was, which would be the key in a log.
	if !strings.HasPrefix(key, KeyPrefix) {
		return nil, errors.New("app.billing.levyan.key: not a product key of levyan's, each of which begins " + KeyPrefix)
	}

	creds, err := c.credentials()
	if err != nil {
		return nil, err
	}
	conn, err := grpc.NewClient(c.Addr,
		grpc.WithTransportCredentials(creds),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: Keepalive, Timeout: Answer}),
	)
	if err != nil {
		return nil, fmt.Errorf("app.billing.levyan.addr: %w", err)
	}

	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	return &Client{
		conn:       conn,
		metering:   levyanv1.NewMeteringServiceClient(conn),
		grants:     pdpb.NewGrantServiceClient(conn),
		workspaces: levyanv1.NewWorkspaceServiceClient(conn),
		key:        key,
		timeout:    timeout,
	}, nil
}

// keyed is `ctx` carrying the product key, as every call to levyan does.
func (c *Client) keyed(ctx context.Context) context.Context {
	return auth.BearerProvider(c.key).Provide(ctx)
}

// call is `ctx` carrying the product key, and an end of its own: a caller's
// context -- a loop's -- ends only when the process stops.
func (c *Client) call(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.keyed(ctx), c.timeout)
}

// credentials is what levyan is dialled with: plaintext only when the
// configuration says so in as many words, and TLS otherwise -- a key that
// can bill is not one to send in the clear because a block was left out.
func (c Config) credentials() (credentials.TransportCredentials, error) {
	if c.Insecure {
		if c.Tls.Active() {
			return nil, errors.New("app.billing.levyan: insecure and tls both say how levyan is dialled; say one")
		}
		return insecure.NewCredentials(), nil
	}

	t := c.Tls
	t.Enabled = true
	creds, err := t.Credentials()
	if err != nil {
		return nil, fmt.Errorf("app.billing.levyan.tls: %w", err)
	}

	return creds, nil
}

// Send hands one workspace's readings to levyan, and is what the meter
// reader's sink is.
//
// An error is levyan not having them -- it did not answer, or not in time --
// and the reader hands the same readings over again later, which levyan
// takes as the ones it has if they did reach it. A reading levyan refused is
// not an error: it is logged, and the reader hands it over no more.
func (c *Client) Send(ctx context.Context, rs []meter.Reading) error {
	if len(rs) == 0 {
		return nil
	}

	vs := make([]*levyanv1.Reading, 0, len(rs))
	for _, r := range rs {
		vs = append(vs, levyanv1.Reading_builder{
			Workspace:  r.Tenant.String(),
			Meter:      r.Meter,
			MeasuredAt: timestamppb.New(r.MeasuredAt),
			Value:      r.Value,
			Unit:       r.Unit,
		}.Build())
	}

	// The reader's context ends only when the process stops, so a call is
	// given an end of its own.
	call, cancel := c.call(ctx)
	defer cancel()
	res, err := c.metering.Record(call, levyanv1.RecordRequest_builder{Readings: vs}.Build())
	if err != nil {
		return fmt.Errorf("levyan: %w", err)
	}

	// One for each, in the order they were sent: anything else is an answer
	// that does not say what became of some of them, and they are sent again.
	results := res.GetResults()
	if len(results) != len(rs) {
		return fmt.Errorf("levyan: answered %d results for %d readings", len(results), len(rs))
	}
	for i, v := range results {
		r := rs[i]
		switch v.GetOutcome() {
		case levyanv1.Outcome_OUTCOME_KEPT, levyanv1.Outcome_OUTCOME_ALREADY:
		case levyanv1.Outcome_OUTCOME_REFUSED:
			log.From(ctx).WarnContext(ctx, "levyan: a reading was refused, and is not handed over again",
				"workspace", r.Tenant.String(), "meter", r.Meter, "measured_at", r.MeasuredAt.Format(time.RFC3339Nano), "value", r.Value, "unit", r.Unit,
				"refusal", v.GetRefusal().String(), "why", v.GetWhy())
		default:
			// An outcome a later levyan says that this build does not know.
			// levyan answered, and has whatever it has of the reading: sent
			// again, it would be answered the same.
			log.From(ctx).WarnContext(ctx, "levyan: a reading came to what this build cannot say, and is not handed over again",
				"workspace", r.Tenant.String(), "meter", r.Meter, "measured_at", r.MeasuredAt.Format(time.RFC3339Nano), "outcome", v.GetOutcome().String())
		}
	}

	return nil
}

// Grants is levyan's `payday.GrantService`, which payday's follower keeps
// this deployment's copy of the grants by (`entitle.Follower`, design 7.5):
// each call with the product key -- levyan answers the grants of the product
// the key is, and nothing names it otherwise -- a page of the list with an
// end of its own, and the stream with none, since it is open for as long as
// the follower is.
func (c *Client) Grants() pdpb.GrantServiceClient { return grants{c} }

type grants struct{ c *Client }

func (g grants) Watch(ctx context.Context, in *pdpb.GrantWatchRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[pdpb.GrantWatchResponse], error) {
	return g.c.grants.Watch(g.c.keyed(ctx), in, opts...)
}

func (g grants) List(ctx context.Context, in *pdpb.GrantListRequest, opts ...grpc.CallOption) (*pdpb.GrantListResponse, error) {
	call, cancel := g.c.call(ctx)
	defer cancel()

	return g.c.grants.List(call, in, opts...)
}

// Arrive tells levyan that `workspace` was made by `creator` at `created`:
// who pays for it from then, until somebody else says they will
// (`levyan.v1.WorkspaceService/Arrive`). levyan takes it once; said again, it
// changes nothing.
func (c *Client) Arrive(ctx context.Context, workspace, creator pdid.Id, created time.Time) error {
	call, cancel := c.call(ctx)
	defer cancel()

	_, err := c.workspaces.Arrive(call, levyanv1.ArriveRequest_builder{
		Workspace: workspace.String(),
		Creator:   creator.String(),
		Created:   timestamppb.New(created),
	}.Build())
	return err
}

// Leave tells levyan that `account` is no longer in `workspace`: if it paid
// for it, it pays no longer (`levyan.v1.WorkspaceService/Leave`). Said again,
// it changes nothing.
func (c *Client) Leave(ctx context.Context, workspace, account pdid.Id) error {
	call, cancel := c.call(ctx)
	defer cancel()

	_, err := c.workspaces.Leave(call, levyanv1.LeaveRequest_builder{
		Workspace: workspace.String(),
		Account:   account.String(),
	}.Build())
	return err
}

// CloseWorkspace tells levyan that `workspace` was closed at `at`: from then
// until it is restored it is billed for none of what it holds, though it is
// still measured, and its payer stays its payer
// (`levyan.v1.WorkspaceService/Close`). levyan reads what became of a
// workspace in the order of its moments, and not of their telling: said
// again, of the same moment, it changes nothing.
func (c *Client) CloseWorkspace(ctx context.Context, workspace pdid.Id, at time.Time) error {
	call, cancel := c.call(ctx)
	defer cancel()

	res, err := c.workspaces.Close(call, levyanv1.CloseRequest_builder{
		Workspace: workspace.String(),
		At:        timestamppb.New(at),
	}.Build())
	if err != nil {
		return err
	}
	late(ctx, workspace, "closed", at, res.GetLate())
	return nil
}

// RestoreWorkspace tells levyan that the closed `workspace` was opened again
// at `at`, and is billed again from then (`levyan.v1.WorkspaceService/Restore`).
func (c *Client) RestoreWorkspace(ctx context.Context, workspace pdid.Id, at time.Time) error {
	call, cancel := c.call(ctx)
	defer cancel()

	res, err := c.workspaces.Restore(call, levyanv1.RestoreRequest_builder{
		Workspace: workspace.String(),
		At:        timestamppb.New(at),
	}.Build())
	if err != nil {
		return err
	}
	late(ctx, workspace, "restored", at, res.GetLate())
	return nil
}

// DestroyWorkspace tells levyan that `workspace` was destroyed at `at`, for
// good: it is billed for nothing after it, its payer pays for it no longer,
// and a reading measured at `at` or after is refused -- so `at` is after its
// final reading (`levyan.v1.WorkspaceService/Destroy`).
func (c *Client) DestroyWorkspace(ctx context.Context, workspace pdid.Id, at time.Time) error {
	call, cancel := c.call(ctx)
	defer cancel()

	res, err := c.workspaces.Destroy(call, levyanv1.DestroyRequest_builder{
		Workspace: workspace.String(),
		At:        timestamppb.New(at),
	}.Build())
	if err != nil {
		return err
	}
	late(ctx, workspace, "destroyed", at, res.GetLate())
	return nil
}

// late says in the log that what became of a workspace reached into a period
// levyan had begun to close: what that period was billed is corrected on a
// later statement, which is levyan's to do and nothing of Loft's.
func late(ctx context.Context, workspace pdid.Id, became string, at time.Time, late bool) {
	if !late {
		return
	}
	log.From(ctx).InfoContext(ctx, "levyan: a workspace "+became+" was told late, and what levyan billed it for then is corrected on a later statement",
		"workspace", workspace.String(), "at", at.UTC().Format(time.RFC3339Nano))
}

// Lifecycle is what levyan has been told became of `workspace`: since when it
// is closed, and when it was destroyed, unset for neither
// (`levyan.v1.WorkspaceService/GetLifecycle`). A workspace levyan heard nothing
// of is open.
func (c *Client) Lifecycle(ctx context.Context, workspace pdid.Id) (*levyanv1.Lifecycle, error) {
	call, cancel := c.call(ctx)
	defer cancel()

	res, err := c.workspaces.GetLifecycle(call, levyanv1.GetLifecycleRequest_builder{Workspace: workspace.String()}.Build())
	if err != nil {
		return nil, err
	}
	return res.GetLifecycle(), nil
}

// Close lets go of the connection; a nil client has none.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}

	return c.conn.Close()
}
