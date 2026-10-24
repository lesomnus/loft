package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/version"

	app "github.com/lesomnus/rove"
)

// NewCmdMcp is `rove mcp`: an MCP server on stdin and stdout, for an
// assistant -- Claude Code, Claude Desktop -- to read a workspace with, as the
// account this terminal signed in as (D32, rove#91).
//
// It is a terminal command like `rove api`, and holds nothing of its own. A
// tool is RPCs to the address a browser reaches the deployment at, with this
// terminal's session: what an assistant can read is what its person can,
// through the same policy and the same wall as the page, and the deployment
// does not know there is an assistant. The sign-in is read again for every
// call, so that `rove login` and `rove use` take effect without starting this
// again.
//
// Every tool reads. One that writes waits for a way for the person to say yes
// to what it is about to do.
func NewCmdMcp() *xli.Command {
	return &xli.Command{
		Name:  "mcp",
		Brief: "an MCP server on stdin and stdout: an assistant reads the workspace as this terminal's account (`rove login` first)",
		Flags: flg.Flags{
			&flg.String{Name: "workspace", Brief: "the workspace it reads, by alias; `rove use` sets the default"},
		},
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			// Stdout is the protocol's; anything else said goes to stderr,
			// which a client keeps as the server's log.
			log := slog.New(slog.NewTextHandler(self.ErrWriter, &slog.HandlerOptions{Level: slog.LevelWarn}))
			s := newMcp(flag(self, "workspace"), log)
			err := s.Run(ctx, &mcp.IOTransport{Reader: self.ReadCloser, Writer: nopWriteCloser{self.Writer}})
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
				return err
			}
			return next(ctx)
		}),
	}
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

const mcpInstructions = `Rove is the register of a workspace's tangible assets: what there is, where it is, who has it, and what happened to it.

- Names are mostly Korean. search_assets understands Korean words: a compound by its last word, a word with a particle on it, a letter wrong.
- An asset is an item, a space (a room, a shelf, a rack), a kit or a group, and is inside another: "where" is the chain of what it is in, the outermost first.
- Tools take the ids other tools answer with. An asset may also be named by its tag.
- Times are RFC 3339; a date alone is midnight in this machine's zone.
- The read tools read, as the person signed in. To change the register, a plan_* tool answers what would happen, in names, with a token: show the person that summary, and only once they say yes, call apply with the token and the summary word for word. A plan changes nothing; a plan never applied does nothing.
- What may be done is the person's role's to say, and a terminal signed in with rove login --allow is held to what it allows.`

// newMcp is the server `rove mcp` runs: the tools, each of them reads, over
// the connection the terminal's sign-in makes for `ws`.
func newMcp(ws string, log *slog.Logger) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "rove", Version: version.Get().Short()}, &mcp.ServerOptions{
		Instructions: mcpInstructions,
		Logger:       log,
	})
	t := mcpTools{ws: ws, plans: &mcpPlans{m: map[string]*plan{}}}
	read := &mcp.ToolAnnotations{ReadOnlyHint: true}
	plans := &mcp.ToolAnnotations{ReadOnlyHint: true, Title: "plan a change"}
	yes := true
	writes := &mcp.ToolAnnotations{DestructiveHint: &yes, Title: "apply a planned change"}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "whoami",
		Description: "Who these tools read as: the account, the workspace and its role there, the person it is in the workspace, and every workspace the account is in.",
		Annotations: read,
	}, t.whoami)
	mcp.AddTool(s, &mcp.Tool{
		Name: "search_assets",
		Description: "Find assets by what a person would type: a name or words of it, a tag, a serial, what it is, its model or maker, who has it. " +
			"Best first where the deployment searches with its engine, by tag otherwise; with no query, the filters alone, by tag. " +
			"Each answers where it is and who has it.",
		InputSchema: mcpSchema[mcpSearchIn](map[string][]any{
			"kind":   {"item", "space", "kit", "group"},
			"status": {"ordered", "active", "in_repair", "lost", "retired", "disposed"},
		}),
		Annotations: read,
	}, t.searchAssets)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_asset",
		Description: "One asset in full: what it is, where it is, who owns, manages and has it, its attributes, and what is directly inside it.",
		Annotations: read,
	}, t.getAsset)
	mcp.AddTool(s, &mcp.Tool{
		Name: "asset_history",
		Description: "What happened to an asset, newest first: where it was put, what changed, who was given it, and every operation with who did it and why. " +
			"Only as far back as the workspace keeps its history.",
		Annotations: read,
	}, t.assetHistory)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "asset_at",
		Description: "An asset and everything inside it as it was at a moment: its name, status and condition then, and who had it.",
		Annotations: read,
	}, t.assetAt)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "recent_events",
		Description: "What happened in the workspace lately, newest first: each operation, who did it, and why.",
		Annotations: read,
	}, t.recentEvents)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_custody",
		Description: "Hand-overs: who was given what -- assets, or stock by quantity -- when it is due back, and what has come back. Open ones by default.",
		InputSchema: mcpSchema[mcpCustodyIn](map[string][]any{
			"status": {"open", "returned", "cancelled", "any"},
			"kind":   {"issue", "loan"},
		}),
		Annotations: read,
	}, t.listCustody)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "reservations",
		Description: "Reservations touching a span of time, for some assets or all: who, what, when, and in what state.",
		Annotations: read,
	}, t.reservations)
	mcp.AddTool(s, &mcp.Tool{
		Name: "find_people",
		Description: "Find people, teams, organizations and vendors by a name or part of it, a code, or the team one is in -- or list them, of a kind, with no query. " +
			"Answers the ids search_assets and list_custody take, and the team each is in.",
		InputSchema: mcpSchema[mcpPeopleIn](map[string][]any{
			"kind": {"person", "team", "org", "vendor"},
		}),
		Annotations: read,
	}, t.findPeople)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "find_models",
		Description: "Find models by a name, who makes them, a code, or what they are a model of -- or list them, of a type, with no query.",
		Annotations: read,
	}, t.findModels)
	mcp.AddTool(s, &mcp.Tool{
		Name: "plan_asset",
		Description: "Plan a change to an asset, which changes nothing: add one, move it into another (or out of it), set what it is, assign who owns, manages or has it, relate it to another, correct a row of its history, or erase one added by mistake. " +
			"Answers the summary to show the person, and the token apply takes once they agree.",
		InputSchema: mcpSchema[mcpAssetPlan](map[string][]any{
			"action":   {"add", "move", "set", "assign", "relate", "correct", "erase"},
			"kind":     {"item", "space", "kit", "group"},
			"mode":     {"located", "installed", "part"},
			"role":     {"owner", "manager", "custodian"},
			"relation": {"member_of", "connected_to"},
		}),
		Annotations: plans,
	}, t.planAsset)
	mcp.AddTool(s, &mcp.Tool{
		Name: "plan_custody",
		Description: "Plan a hand-over, which changes nothing: issue assets or supplies to somebody (a loan, with a due date), take back some or all of what is out, give it longer, or say the receiver has it. " +
			"Answers the summary to show the person, and the token apply takes once they agree.",
		InputSchema: mcpSchema[mcpCustodyPlan](map[string][]any{
			"action": {"issue", "return", "extend", "acknowledge"},
			"kind":   {"issue", "loan"},
		}),
		Annotations: plans,
	}, t.planCustody)
	mcp.AddTool(s, &mcp.Tool{
		Name: "plan_reservation",
		Description: "Plan a reservation, which changes nothing: reserve rooms or equipment for a span of time, or confirm, approve, reject, cancel, check in or complete one. " +
			"Answers the summary to show the person, and the token apply takes once they agree.",
		InputSchema: mcpSchema[mcpReservationPlan](map[string][]any{
			"action": {"reserve", "confirm", "approve", "reject", "cancel", "check_in", "complete"},
		}),
		Annotations: plans,
	}, t.planReservation)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_stock",
		Description: "Supplies kept by quantity: how many there are of each, where, and which are low -- the ids plan_stock and plan_custody take.",
		Annotations: read,
	}, t.listStock)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_work_orders",
		Description: "Repairs, inspections and maintenance: on what, by whom, in what state -- the ids plan_work_order takes. Newest first.",
		InputSchema: mcpSchema[mcpWorkIn](map[string][]any{
			"status": {"open", "scheduled", "in_progress", "done", "cancelled"},
			"kind":   {"repair", "inspection", "maintenance", "rma"},
		}),
		Annotations: read,
	}, t.listWorkOrders)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_counts",
		Description: "Inventory counts, newest first; one of them by id with what it found -- seen, misplaced, missing, unknown -- and the ids plan_count takes.",
		InputSchema: mcpSchema[mcpCountsIn](map[string][]any{"status": {"open", "closed"}}),
		Annotations: read,
	}, t.listCounts)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_purchases",
		Description: "Purchases, newest first; one of them by id with what was bought and what has arrived -- the ids plan_purchase takes.",
		InputSchema: mcpSchema[mcpPurchasesIn](map[string][]any{"status": {"ordered", "received", "cancelled"}}),
		Annotations: read,
	}, t.listPurchases)
	mcp.AddTool(s, &mcp.Tool{
		Name: "plan_stock",
		Description: "Plan a change to supplies kept by quantity, which changes nothing: register a stock, receive more, consume some, adjust it to what was counted, move some to another space, or make some of it assets of their own. " +
			"Answers the summary to show the person, and the token apply takes once they agree.",
		InputSchema: mcpSchema[mcpStockPlan](map[string][]any{"action": {"add", "receive", "consume", "adjust", "transfer", "convert"}}),
		Annotations: plans,
	}, t.planStock)
	mcp.AddTool(s, &mcp.Tool{
		Name: "plan_count",
		Description: "Plan a step of an inventory count, which changes nothing: start one over a space, record what was seen, mark what was never seen, settle a finding, or close it. " +
			"Answers the summary to show the person, and the token apply takes once they agree.",
		InputSchema: mcpSchema[mcpCountPlan](map[string][]any{
			"action":     {"start", "scan", "reconcile", "resolve", "close"},
			"resolution": {"moved", "lost", "ignored"},
		}),
		Annotations: plans,
	}, t.planCount)
	mcp.AddTool(s, &mcp.Tool{
		Name: "plan_work_order",
		Description: "Plan a repair, inspection or maintenance, which changes nothing: report one on an asset, change it, complete it, or cancel it. " +
			"Answers the summary to show the person, and the token apply takes once they agree.",
		InputSchema: mcpSchema[mcpWorkPlan](map[string][]any{
			"action":    {"report", "update", "complete", "cancel"},
			"kind":      {"repair", "inspection", "maintenance", "rma"},
			"status":    {"open", "scheduled", "in_progress"},
			"condition": {"good", "damaged", "broken"},
		}),
		Annotations: plans,
	}, t.planWorkOrder)
	mcp.AddTool(s, &mcp.Tool{
		Name: "plan_purchase",
		Description: "Plan a purchase, which changes nothing: register what is bought from whom, or receive what arrived into a space, as assets or stock. " +
			"Answers the summary to show the person, and the token apply takes once they agree.",
		InputSchema: mcpSchema[mcpPurchasePlan](map[string][]any{"action": {"add", "receive"}}),
		Annotations: plans,
	}, t.planPurchase)
	mcp.AddTool(s, &mcp.Tool{
		Name: "plan_model",
		Description: "Plan a model -- what an asset is a make of -- which changes nothing: register one, or change one. " +
			"Answers the summary to show the person, and the token apply takes once they agree.",
		InputSchema: mcpSchema[mcpModelPlan](map[string][]any{"action": {"add", "update"}}),
		Annotations: plans,
	}, t.planModel)
	mcp.AddTool(s, &mcp.Tool{
		Name: "plan_label",
		Description: "Plan labels, which changes nothing: make new ones (or one for each of some assets), stick one on an asset, or take one off. " +
			"Answers the summary to show the person, and the token apply takes once they agree; apply answers the codes and addresses of labels made.",
		InputSchema: mcpSchema[mcpLabelPlan](map[string][]any{"action": {"print", "bind", "unbind"}}),
		Annotations: plans,
	}, t.planLabel)
	mcp.AddTool(s, &mcp.Tool{
		Name: "apply",
		Description: "Carry out a plan a plan_* tool made, once the person has read its summary and said yes: the token, and the summary word for word. " +
			"It is the one tool that changes the register, as the person signed in, and the history names them.",
		Annotations: writes,
	}, t.apply)
	return s
}

// mcpSchema is In's schema, with the values a property takes where Go cannot
// say them.
func mcpSchema[In any](enums map[string][]any) *jsonschema.Schema {
	s, err := jsonschema.For[In](nil)
	if err != nil {
		panic(err)
	}
	for k, vs := range enums {
		s.Properties[k].Enum = vs
	}
	return s
}

type mcpTools struct {
	// The workspace named on the command line, over `ROVE_WORKSPACE` and `rove
	// use`.
	ws string
	// The plans made and not yet applied, for every call of this process.
	plans *mcpPlans
}

// call is what one tool call reads with: the terminal's sign-in as it is now,
// and the names of what it reads, asked once each.
func (t mcpTools) call(ctx context.Context) (*mcpCall, error) {
	c, err := loadClient()
	if err != nil {
		return nil, err
	}
	if !c.signedIn() {
		return nil, errNoLogin
	}
	ws := t.ws
	if ws == "" {
		ws = os.Getenv("ROVE_WORKSPACE")
	}
	if ws == "" {
		ws = c.Workspace
	}
	return &mcpCall{
		ctx:     ctx,
		client:  c,
		ws:      ws,
		conn:    c.conn(ws),
		assets:  map[pdid.Id]*app.Asset{},
		parties: map[pdid.Id]string{},
		holders: map[pdid.Id]string{},
		types:   map[pdid.Id]string{},
		models:  map[pdid.Id]string{},
		stocks:  map[pdid.Id]*app.Stock{},
	}, nil
}

type mcpCall struct {
	ctx    context.Context
	client clientConfig
	ws     string
	conn   grpc.ClientConnInterface

	me *app.PartyMeResponse
	// allow is what GET /account said the sign-in is narrowed to, once asked:
	// empty for nothing.
	allow []string

	mu      sync.Mutex
	assets  map[pdid.Id]*app.Asset
	parties map[pdid.Id]string
	holders map[pdid.Id]string
	types   map[pdid.Id]string
	models  map[pdid.Id]string
	stocks  map[pdid.Id]*app.Stock
}

// failed is a call's error as an assistant reads it: what the deployment
// said, and the code it said it with.
func failed(err error) error {
	if s, ok := status.FromError(err); ok {
		return fmt.Errorf("%s: %s", s.Code(), s.Message())
	}
	return err
}

func (c *mcpCall) whom() (*app.PartyMeResponse, error) {
	if c.me != nil {
		return c.me, nil
	}
	v, err := app.NewPartyServiceClient(c.conn).Me(c.ctx, &app.PartyMeRequest{})
	if err != nil {
		return nil, failed(err)
	}
	c.me = v
	return v, nil
}

// assetRef is an asset named by its id or by its tag.
func (c *mcpCall) assetRef(v string) (*app.AssetRef, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, errors.New("which asset: an id, or its tag")
	}
	if id, err := pdid.Parse(v); err == nil {
		return app.AssetById(id.Bytes()), nil
	}
	me, err := c.whom()
	if err != nil {
		return nil, err
	}
	k := app.AssetRefByTag_builder{Tenant: app.TenantById(me.GetTenant().GetId())}.Build()
	k.SetTag(v)
	return app.AssetRef_builder{Tag: k}.Build(), nil
}

func idOf(field, v string) (pdid.Id, error) {
	id, err := pdid.Parse(strings.TrimSpace(v))
	if err != nil {
		return pdid.Nil, fmt.Errorf("%s: %q is not an id another tool answered with", field, v)
	}
	return id, nil
}

func idString(b []byte) string {
	id, err := pdid.From(b)
	if err != nil || id.IsZero() {
		return ""
	}
	return id.String()
}

// fetch asks for what `get` reads of each identifier not read yet in this
// call, a few at a time; what cannot be read is left out of the answer
// rather than failing it.
func fetch[V any](c *mcpCall, into map[pdid.Id]V, ids []pdid.Id, get func(pdid.Id) (V, error)) {
	g := errgroup.Group{}
	g.SetLimit(8)
	seen := map[pdid.Id]bool{}
	for _, id := range ids {
		c.mu.Lock()
		_, known := into[id]
		c.mu.Unlock()
		if known || seen[id] || id.IsZero() {
			continue
		}
		seen[id] = true
		g.Go(func() error {
			v, err := get(id)
			c.mu.Lock()
			defer c.mu.Unlock()
			if err == nil {
				into[id] = v
			} else {
				var zero V
				into[id] = zero
			}
			return nil
		})
	}
	_ = g.Wait()
}

func pid(b []byte) pdid.Id {
	id, _ := pdid.From(b)
	return id
}

// prefetch reads the names a page of assets is answered with: what each is
// in, all the way out, and its type, model and custodian.
func (c *mcpCall) prefetch(as []*app.Asset) {
	types, models, parties := []pdid.Id{}, []pdid.Id{}, []pdid.Id{}
	for _, a := range as {
		c.assets[pid(a.GetId())] = a
		types = append(types, pid(a.GetType().GetId()))
		models = append(models, pid(a.GetModel().GetId()))
		parties = append(parties, pid(a.GetCustodian().GetId()))
	}

	g := errgroup.Group{}
	g.Go(func() error {
		frontier := as
		for depth := 0; len(frontier) > 0 && depth < 32; depth++ {
			up := []pdid.Id{}
			for _, a := range frontier {
				if p := pid(a.GetParentId()); !p.IsZero() {
					up = append(up, p)
				}
			}
			fetch(c, c.assets, up, func(id pdid.Id) (*app.Asset, error) {
				return app.NewAssetServiceClient(c.conn).Get(c.ctx, app.AssetGetById(id.Bytes()))
			})
			frontier = frontier[:0:0]
			c.mu.Lock()
			for _, id := range up {
				if v := c.assets[id]; v != nil {
					frontier = append(frontier, v)
				}
			}
			c.mu.Unlock()
		}
		return nil
	})
	g.Go(func() error {
		fetch(c, c.types, types, func(id pdid.Id) (string, error) {
			v, err := app.NewAssetTypeServiceClient(c.conn).Get(c.ctx, app.AssetTypeGetRequest_builder{Ref: app.AssetTypeById(id.Bytes())}.Build())
			return v.GetName(), err
		})
		return nil
	})
	g.Go(func() error {
		fetch(c, c.models, models, func(id pdid.Id) (string, error) {
			v, err := app.NewItemModelServiceClient(c.conn).Get(c.ctx, app.ItemModelGetRequest_builder{Ref: app.ItemModelById(id.Bytes())}.Build())
			if err != nil || v.GetMaker() == "" {
				return v.GetName(), err
			}
			return fmt.Sprintf("%s (%s)", v.GetName(), v.GetMaker()), nil
		})
		return nil
	})
	g.Go(func() error {
		c.fetchParties(parties)
		return nil
	})
	_ = g.Wait()
}

func (c *mcpCall) fetchParties(ids []pdid.Id) {
	fetch(c, c.parties, ids, func(id pdid.Id) (string, error) {
		v, err := app.NewPartyServiceClient(c.conn).Get(c.ctx, app.PartyGetRequest_builder{Ref: app.PartyById(id.Bytes())}.Build())
		return v.GetName(), err
	})
}

func (c *mcpCall) named(m map[pdid.Id]string, b []byte) *mcpRef {
	id := pid(b)
	if id.IsZero() {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return &mcpRef{Id: id.String(), Name: m[id]}
}

// where is what an asset is in now, the outermost first.
func (c *mcpCall) where(a *app.Asset) []mcpRef {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []mcpRef{}
	seen := map[pdid.Id]bool{}
	for p := pid(a.GetParentId()); !p.IsZero() && !seen[p] && len(out) < 32; {
		seen[p] = true
		v := c.assets[p]
		if v == nil {
			// One this person may not see, or that went: said as there,
			// unnamed, so that the chain does not lie about its length.
			out = append(out, mcpRef{Id: p.String()})
			break
		}
		out = append(out, mcpRef{Id: p.String(), Name: v.GetName()})
		p = pid(v.GetParentId())
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// The answers. Names a person reads, beside the identifiers other tools
// take; times in RFC 3339, in this machine's zone.

type mcpRef struct {
	Id   string `json:"id"`
	Name string `json:"name,omitempty"`
}

type mcpAsset struct {
	Id        string   `json:"id"`
	Tag       string   `json:"tag,omitempty"`
	Name      string   `json:"name"`
	Kind      string   `json:"kind" jsonschema:"item, space, kit or group"`
	Status    string   `json:"status,omitempty"`
	Condition string   `json:"condition,omitempty"`
	Serial    string   `json:"serial,omitempty"`
	Type      *mcpRef  `json:"type,omitempty" jsonschema:"what it is"`
	Model     *mcpRef  `json:"model,omitempty" jsonschema:"its model, and who makes it"`
	Where     []mcpRef `json:"where,omitempty" jsonschema:"what it is in now, the outermost first"`
	Custodian *mcpRef  `json:"custodian,omitempty" jsonschema:"the person or team that has it now"`

	// What get_asset says beyond a search's line.
	Desc       string            `json:"desc,omitempty"`
	Placement  string            `json:"placement,omitempty" jsonschema:"how it is held where it is: located, installed or part"`
	Slot       string            `json:"slot,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
	Stewards   map[string]string `json:"stewards,omitempty" jsonschema:"owner, manager and custodian, by name"`
	Acquired   string            `json:"acquired,omitempty"`
	Inside     []mcpChild        `json:"inside,omitempty" jsonschema:"what is directly inside it now, fifty at most"`
}

type mcpChild struct {
	Id        string  `json:"id"`
	Tag       string  `json:"tag,omitempty"`
	Name      string  `json:"name"`
	Kind      string  `json:"kind"`
	Status    string  `json:"status,omitempty"`
	Custodian *mcpRef `json:"custodian,omitempty"`
}

func stamp(ts *timestamppb.Timestamp) string {
	if ts == nil || !ts.IsValid() || ts.AsTime().IsZero() {
		return ""
	}
	return ts.AsTime().Local().Format(time.RFC3339)
}

// at reads a moment an assistant gave: RFC 3339, or a date alone.
func at(field, v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation(time.DateOnly, v, time.Local); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("%s: %q is not RFC 3339, nor a date (2026-10-16)", field, v)
}

func (c *mcpCall) brief(a *app.Asset) mcpAsset {
	return mcpAsset{
		Id:        idString(a.GetId()),
		Tag:       a.GetTag(),
		Name:      a.GetName(),
		Kind:      a.GetKind(),
		Status:    a.GetStatus(),
		Condition: a.GetCondition(),
		Serial:    a.GetSerial(),
		Type:      c.named(c.types, a.GetType().GetId()),
		Model:     c.named(c.models, a.GetModel().GetId()),
		Where:     c.where(a),
		Custodian: c.named(c.parties, a.GetCustodian().GetId()),
	}
}

type mcpWhoamiIn struct{}

type mcpWorkspace struct {
	Alias string `json:"alias"`
	Name  string `json:"name"`
	Role  string `json:"role"`
}

type mcpWhoami struct {
	Deployment string         `json:"deployment" jsonschema:"the address of the deployment"`
	Login      string         `json:"login"`
	Name       string         `json:"name"`
	Workspace  *mcpWorkspace  `json:"workspace,omitempty" jsonschema:"the workspace these tools read"`
	Person     *mcpRef        `json:"person,omitempty" jsonschema:"the person this account is in that workspace"`
	History    string         `json:"history" jsonschema:"how far back the workspace keeps its history"`
	Workspaces []mcpWorkspace `json:"workspaces" jsonschema:"every workspace the account is in; another is read by starting rove mcp --workspace ALIAS"`
	Allow      []string       `json:"allow,omitempty" jsonschema:"the only methods this terminal's sign-in may call (rove login --allow); absent is all that the account may do"`
}

func (t mcpTools) whoami(ctx context.Context, _ *mcp.CallToolRequest, _ mcpWhoamiIn) (*mcp.CallToolResult, mcpWhoami, error) {
	out := mcpWhoami{}
	c, err := t.call(ctx)
	if err != nil {
		return nil, out, err
	}
	var account struct {
		Login      string         `json:"login"`
		Name       string         `json:"name"`
		Workspaces []mcpWorkspace `json:"workspaces"`
		Allow      []string       `json:"allow"`
	}
	if _, err := c.client.do(ctx, http.MethodGet, "/account", nil, &account); err != nil {
		return nil, out, err
	}
	out.Deployment, out.Login, out.Name, out.Workspaces, out.Allow = c.client.Url, account.Login, account.Name, account.Workspaces, account.Allow
	if out.Workspaces == nil {
		out.Workspaces = []mcpWorkspace{}
	}

	me, err := c.whom()
	if err != nil {
		return nil, out, err
	}
	for _, w := range account.Workspaces {
		if w.Alias == me.GetTenant().GetAlias() {
			out.Workspace = &w
		}
	}
	if out.Workspace == nil {
		out.Workspace = &mcpWorkspace{Alias: me.GetTenant().GetAlias(), Name: me.GetTenant().GetName(), Role: me.GetRole()}
	}
	if p := me.GetParty(); len(p.GetId()) > 0 {
		out.Person = &mcpRef{Id: idString(p.GetId()), Name: p.GetName()}
	}
	out.History = "all of it"
	if d := me.GetTenant().GetKeepDays(); d > 0 {
		out.History = fmt.Sprintf("the last %d days", d)
	}
	return nil, out, nil
}

type mcpSearchIn struct {
	Query     string `json:"query,omitempty" jsonschema:"what to look for: a name or words of it, a tag, a serial, what it is, its model or maker, who has it"`
	Kind      string `json:"kind,omitempty" jsonschema:"only assets of this kind"`
	Status    string `json:"status,omitempty" jsonschema:"only assets in this status"`
	Within    string `json:"within,omitempty" jsonschema:"only what is inside this asset now, at any depth: its id or its tag"`
	Custodian string `json:"custodian,omitempty" jsonschema:"only what this person or team has: an id find_people answers with"`
	Type      string `json:"type,omitempty" jsonschema:"only assets of this type: a type id another answer gave"`
	Limit     int    `json:"limit,omitempty" jsonschema:"how many to answer with: 20 if left out, 100 at most"`
	Offset    int    `json:"offset,omitempty" jsonschema:"how many to skip, for the next page"`
}

type mcpAssets struct {
	Total int        `json:"total" jsonschema:"how many match in all"`
	Items []mcpAsset `json:"items"`
}

func (t mcpTools) searchAssets(ctx context.Context, _ *mcp.CallToolRequest, in mcpSearchIn) (*mcp.CallToolResult, mcpAssets, error) {
	out := mcpAssets{Items: []mcpAsset{}}
	c, err := t.call(ctx)
	if err != nil {
		return nil, out, err
	}
	req := app.AssetSearchRequest_builder{
		Q:      in.Query,
		Kind:   in.Kind,
		Status: in.Status,
		Size:   uint32(min(max(in.Limit, 0), 100)),
		Offset: uint32(max(in.Offset, 0)),
	}.Build()
	if in.Limit <= 0 {
		req.SetSize(20)
	}
	if in.Within != "" {
		ref, err := c.assetRef(in.Within)
		if err != nil {
			return nil, out, err
		}
		req.SetWithin(ref)
	}
	if in.Custodian != "" {
		id, err := idOf("custodian", in.Custodian)
		if err != nil {
			return nil, out, err
		}
		req.SetCustodian(app.PartyById(id.Bytes()))
	}
	if in.Type != "" {
		id, err := idOf("type", in.Type)
		if err != nil {
			return nil, out, err
		}
		req.SetType(app.AssetTypeById(id.Bytes()))
	}
	v, err := app.NewAssetServiceClient(c.conn).Search(ctx, req)
	if err != nil {
		return nil, out, failed(err)
	}
	c.prefetch(v.GetItems())
	out.Total = int(v.GetTotal())
	for _, a := range v.GetItems() {
		out.Items = append(out.Items, c.brief(a))
	}
	return nil, out, nil
}

type mcpAssetIn struct {
	Asset string `json:"asset" jsonschema:"the asset: its id, or its tag"`
}

func (t mcpTools) getAsset(ctx context.Context, _ *mcp.CallToolRequest, in mcpAssetIn) (*mcp.CallToolResult, mcpAsset, error) {
	out := mcpAsset{}
	c, err := t.call(ctx)
	if err != nil {
		return nil, out, err
	}
	ref, err := c.assetRef(in.Asset)
	if err != nil {
		return nil, out, err
	}
	a, err := app.NewAssetServiceClient(c.conn).Get(ctx, app.AssetGetRequest_builder{Ref: ref}.Build())
	if err != nil {
		return nil, out, failed(err)
	}
	// It and what is directly in it, as they are now: who owns, manages and
	// has each is the history's to say.
	now, err := app.NewAssetServiceClient(c.conn).QueryAt(ctx, app.AssetQueryAtRequest_builder{
		Root:  app.AssetById(a.GetId()),
		At:    timestamppb.Now(),
		Depth: 1,
	}.Build())
	if err != nil {
		return nil, out, failed(err)
	}
	ids := []pdid.Id{}
	for _, st := range now.GetItems() {
		if st.GetDepth() == 1 && len(ids) < 50 {
			ids = append(ids, pid(st.GetId()))
		}
	}
	fetch(c, c.assets, ids, func(id pdid.Id) (*app.Asset, error) {
		return app.NewAssetServiceClient(c.conn).Get(ctx, app.AssetGetById(id.Bytes()))
	})
	inside := []*app.Asset{}
	for _, id := range ids {
		if v := c.assets[id]; v != nil {
			inside = append(inside, v)
		}
	}
	c.prefetch(append([]*app.Asset{a}, inside...))

	out = c.brief(a)
	out.Desc = a.GetDesc()
	out.Placement = a.GetPlacementMode()
	out.Slot = a.GetSlot()
	out.Attributes = a.GetAttributes()
	out.Acquired = stamp(a.GetAcquiredAt())
	for _, st := range now.GetItems() {
		if st.GetDepth() == 0 && len(st.GetStewardNames()) > 0 {
			out.Stewards = st.GetStewardNames()
		}
	}
	for _, v := range inside {
		out.Inside = append(out.Inside, mcpChild{
			Id:        idString(v.GetId()),
			Tag:       v.GetTag(),
			Name:      v.GetName(),
			Kind:      v.GetKind(),
			Status:    v.GetStatus(),
			Custodian: c.named(c.parties, v.GetCustodian().GetId()),
		})
	}
	return nil, out, nil
}

type mcpHistoryIn struct {
	Asset      string `json:"asset" jsonschema:"the asset: its id, or its tag"`
	Limit      int    `json:"limit,omitempty" jsonschema:"how many entries, the newest: 30 if left out, 200 at most"`
	Superseded bool   `json:"superseded,omitempty" jsonschema:"also what later corrections replaced"`
}

type mcpEntry struct {
	Kind       string `json:"kind" jsonschema:"placement, fact, stewardship, link or event"`
	Row        string `json:"row,omitempty" jsonschema:"the row, which plan_asset's correct takes; an event is not one"`
	From       string `json:"from" jsonschema:"when it became so, or when it happened"`
	Until      string `json:"until,omitempty" jsonschema:"when it stopped being so"`
	What       string `json:"what"`
	Operation  string `json:"operation,omitempty" jsonschema:"for an event, its kind: asset.move, custody.issue, ..."`
	Who        string `json:"who,omitempty"`
	Why        string `json:"why,omitempty"`
	Part       string `json:"part_of,omitempty" jsonschema:"the operation over many things this was part of, in its own words"`
	Superseded string `json:"superseded,omitempty" jsonschema:"when a correction replaced it"`
	// What the history's own entry holds (`TimelineEntry.detail`), which is
	// made of what the server reads it with: an event's message, each person
	// in it by the name they are called by and never anything else of
	// theirs, and a time row's own columns.
	Detail map[string]string `json:"detail,omitempty" jsonschema:"what it held, field by field: of an event, what its operation held, a thing or a person by the name a person reads it by and a moment in the workspace's zone; of a time row, its own fields -- a placement's mode and slot, a stewardship's role, a fact's key and value"`
}

type mcpHistory struct {
	Asset   mcpRef     `json:"asset"`
	Entries []mcpEntry `json:"entries"`
}

func (t mcpTools) assetHistory(ctx context.Context, _ *mcp.CallToolRequest, in mcpHistoryIn) (*mcp.CallToolResult, mcpHistory, error) {
	out := mcpHistory{Entries: []mcpEntry{}}
	c, err := t.call(ctx)
	if err != nil {
		return nil, out, err
	}
	ref, err := c.assetRef(in.Asset)
	if err != nil {
		return nil, out, err
	}
	a, err := app.NewAssetServiceClient(c.conn).Get(ctx, app.AssetGetRequest_builder{Ref: ref}.Build())
	if err != nil {
		return nil, out, failed(err)
	}
	n := in.Limit
	if n <= 0 {
		n = 30
	}
	v, err := app.NewAssetServiceClient(c.conn).Timeline(ctx, app.AssetTimelineRequest_builder{
		Ref:        app.AssetById(a.GetId()),
		Limit:      uint32(min(n, 200)),
		Superseded: in.Superseded,
	}.Build())
	if err != nil {
		return nil, out, failed(err)
	}
	out.Asset = mcpRef{Id: idString(a.GetId()), Name: a.GetName()}
	for _, e := range v.GetEntries() {
		row := ""
		if e.GetKind() != "event" {
			row = idString(e.GetRowId())
		}
		out.Entries = append(out.Entries, mcpEntry{
			Kind:       e.GetKind(),
			Row:        row,
			From:       stamp(e.GetValidFrom()),
			Until:      stamp(e.GetValidTo()),
			What:       e.GetSummary(),
			Operation:  e.GetEventKind(),
			Who:        e.GetActor(),
			Why:        e.GetReason(),
			Part:       e.GetCause(),
			Superseded: stamp(e.GetSupersededAt()),
			Detail:     e.GetDetail(),
		})
	}
	return nil, out, nil
}

type mcpAtIn struct {
	Asset string `json:"asset" jsonschema:"the asset: its id, or its tag"`
	At    string `json:"at" jsonschema:"the moment: RFC 3339, or a date"`
	Depth int    `json:"depth,omitempty" jsonschema:"how deep inside it: 1 is what was directly in it; 0, or left out, is all of it"`
}

type mcpState struct {
	Id        string            `json:"id"`
	Tag       string            `json:"tag,omitempty"`
	Kind      string            `json:"kind"`
	Depth     int               `json:"depth" jsonschema:"0 is the asset asked about"`
	In        string            `json:"in,omitempty" jsonschema:"the id of what it was in then"`
	Placement string            `json:"placement,omitempty"`
	Slot      string            `json:"slot,omitempty"`
	Facts     map[string]string `json:"facts,omitempty" jsonschema:"name, status, condition and attributes as they were"`
	Stewards  map[string]string `json:"stewards,omitempty" jsonschema:"who owned, managed and had it, by name"`
	Existed   bool              `json:"existed" jsonschema:"whether it had been registered by then"`
}

type mcpAt struct {
	At    string     `json:"at"`
	Items []mcpState `json:"items"`
}

func (t mcpTools) assetAt(ctx context.Context, _ *mcp.CallToolRequest, in mcpAtIn) (*mcp.CallToolResult, mcpAt, error) {
	out := mcpAt{Items: []mcpState{}}
	c, err := t.call(ctx)
	if err != nil {
		return nil, out, err
	}
	ref, err := c.assetRef(in.Asset)
	if err != nil {
		return nil, out, err
	}
	moment, err := at("at", in.At)
	if err != nil {
		return nil, out, err
	}
	v, err := app.NewAssetServiceClient(c.conn).QueryAt(ctx, app.AssetQueryAtRequest_builder{
		Root:  ref,
		At:    timestamppb.New(moment),
		Depth: uint32(max(in.Depth, 0)),
	}.Build())
	if err != nil {
		return nil, out, failed(err)
	}
	out.At = moment.Local().Format(time.RFC3339)
	for _, st := range v.GetItems() {
		out.Items = append(out.Items, mcpState{
			Id:        idString(st.GetId()),
			Tag:       st.GetTag(),
			Kind:      st.GetKind(),
			Depth:     int(st.GetDepth()),
			In:        idString(st.GetParentId()),
			Placement: st.GetMode(),
			Slot:      st.GetSlot(),
			Facts:     st.GetFacts(),
			Stewards:  st.GetStewardNames(),
			Existed:   st.GetExisted(),
		})
	}
	return nil, out, nil
}

type mcpEventsIn struct {
	Kind   string `json:"kind,omitempty" jsonschema:"only operations whose kind starts with this: asset., custody., reservation."`
	Limit  int    `json:"limit,omitempty" jsonschema:"how many: 30 if left out, 200 at most"`
	Before string `json:"before,omitempty" jsonschema:"only what was recorded before this moment, for the next page: the last answer's oldest 'recorded'"`
}

type mcpEvent struct {
	When      string  `json:"when" jsonschema:"when it happened"`
	Recorded  string  `json:"recorded" jsonschema:"when it was written down"`
	Operation string  `json:"operation"`
	What      string  `json:"what"`
	Who       string  `json:"who" jsonschema:"who did it; the deployment's own work says so"`
	Why       string  `json:"why,omitempty"`
	Subject   *mcpRef `json:"subject,omitempty" jsonschema:"what it was about: an asset, a hand-over, a reservation ... by id"`
}

type mcpEvents struct {
	Items []mcpEvent `json:"items"`
}

func (t mcpTools) recentEvents(ctx context.Context, _ *mcp.CallToolRequest, in mcpEventsIn) (*mcp.CallToolResult, mcpEvents, error) {
	out := mcpEvents{Items: []mcpEvent{}}
	c, err := t.call(ctx)
	if err != nil {
		return nil, out, err
	}
	n := in.Limit
	if n <= 0 {
		n = 30
	}
	req := app.EventRecentRequest_builder{Size: uint32(min(n, 200)), Kind: in.Kind}.Build()
	if in.Before != "" {
		b, err := at("before", in.Before)
		if err != nil {
			return nil, out, err
		}
		req.SetBefore(timestamppb.New(b))
	}
	v, err := app.NewEventServiceClient(c.conn).Recent(ctx, req)
	if err != nil {
		return nil, out, failed(err)
	}
	actors := []pdid.Id{}
	for _, e := range v.GetItems() {
		actors = append(actors, pid(e.GetActorId()))
	}
	// Who did it is a membership, which the history names by the person
	// it is, as the history's own sentences do.
	fetch(c, c.holders, actors, func(id pdid.Id) (string, error) {
		p, err := app.NewPartyServiceClient(c.conn).Get(ctx, app.PartyGetRequest_builder{
			Ref: app.PartyRef_builder{Holder: app.PartyRefByHolder_builder{Holder: app.HolderById(id.Bytes())}.Build()}.Build(),
		}.Build())
		return p.GetName(), err
	})
	for _, e := range v.GetItems() {
		who := "시스템"
		if a := pid(e.GetActorId()); !a.IsZero() {
			who = c.holders[a]
		}
		ev := mcpEvent{
			When:      stamp(e.GetOccurredAt()),
			Recorded:  stamp(e.GetDateCreated()),
			Operation: e.GetKind(),
			What:      e.GetDesc(),
			Who:       who,
			Why:       e.GetReason(),
		}
		if s := idString(e.GetSubjectId()); s != "" {
			ev.Subject = &mcpRef{Id: s}
		}
		out.Items = append(out.Items, ev)
	}
	return nil, out, nil
}

type mcpCustodyIn struct {
	Party  string `json:"party,omitempty" jsonschema:"only what this person or team was given: an id find_people answers with"`
	Status string `json:"status,omitempty" jsonschema:"open (still out) if left out; any is every state"`
	Kind   string `json:"kind,omitempty" jsonschema:"issue (handed over to keep) or loan (lent, due back)"`
	Limit  int    `json:"limit,omitempty" jsonschema:"how many hand-overs: 20 if left out, 100 at most"`
	Cursor string `json:"cursor,omitempty" jsonschema:"where to carry on from: the last answer's next"`
}

type mcpLine struct {
	Id        string  `json:"id" jsonschema:"the line, which plan_custody's return takes"`
	Asset     *mcpRef `json:"asset,omitempty"`
	Stock     *mcpRef `json:"stock,omitempty" jsonschema:"supplies handed over by quantity"`
	Quantity  int64   `json:"quantity"`
	Returned  int64   `json:"returned" jsonschema:"how many have come back"`
	Unit      string  `json:"unit,omitempty"`
	Out       string  `json:"out,omitempty"`
	Back      string  `json:"back,omitempty" jsonschema:"when it all came back"`
	Condition string  `json:"condition,omitempty" jsonschema:"its condition when it went out"`
}

type mcpCustody struct {
	Id           string    `json:"id"`
	Party        *mcpRef   `json:"party" jsonschema:"who was given it"`
	Kind         string    `json:"kind"`
	Status       string    `json:"status"`
	Issued       string    `json:"issued,omitempty"`
	Due          string    `json:"due,omitempty"`
	Overdue      bool      `json:"overdue" jsonschema:"still out past when it was due"`
	Acknowledged string    `json:"acknowledged,omitempty" jsonschema:"when the receiver said they had it"`
	Desc         string    `json:"desc,omitempty"`
	Lines        []mcpLine `json:"lines"`
}

type mcpCustodies struct {
	Items []mcpCustody `json:"items"`
	Next  string       `json:"next,omitempty" jsonschema:"the cursor for the next page; absent when this was the last"`
}

func (t mcpTools) listCustody(ctx context.Context, _ *mcp.CallToolRequest, in mcpCustodyIn) (*mcp.CallToolResult, mcpCustodies, error) {
	out := mcpCustodies{Items: []mcpCustody{}}
	c, err := t.call(ctx)
	if err != nil {
		return nil, out, err
	}
	f := &app.CustodyFilter{}
	if in.Kind != "" {
		f.SetKind(in.Kind)
	}
	switch in.Status {
	case "":
		f.SetStatus("open")
	case "any":
	default:
		f.SetStatus(in.Status)
	}
	if in.Party != "" {
		id, err := idOf("party", in.Party)
		if err != nil {
			return nil, out, err
		}
		f.SetParty(app.PartyById(id.Bytes()))
	}
	n := in.Limit
	if n <= 0 {
		n = 20
	}
	v, err := app.NewCustodyServiceClient(c.conn).List(ctx, app.CustodyListRequest_builder{
		Filters: filters(f),
		Size:    int32(min(n, 100)),
		After:   in.Cursor,
	}.Build())
	if err != nil {
		return nil, out, failed(err)
	}
	out.Next = v.GetNext()

	parties := []pdid.Id{}
	for _, k := range v.GetItems() {
		parties = append(parties, pid(k.GetParty().GetId()))
	}
	c.fetchParties(parties)
	lines := make([][]*app.CustodyLine, len(v.GetItems()))
	g := errgroup.Group{}
	g.SetLimit(8)
	for i, k := range v.GetItems() {
		g.Go(func() error {
			ls, err := app.NewCustodyLineServiceClient(c.conn).List(ctx, app.CustodyLineListRequest_builder{
				Filters: []*app.CustodyLineFilter{app.CustodyLineFilter_builder{Custody: app.CustodyById(k.GetId())}.Build()},
				Size:    100,
			}.Build())
			if err != nil {
				return failed(err)
			}
			lines[i] = ls.GetItems()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, out, err
	}
	assets, stocks := []pdid.Id{}, []pdid.Id{}
	for _, ls := range lines {
		for _, l := range ls {
			assets = append(assets, pid(l.GetAsset().GetId()))
			stocks = append(stocks, pid(l.GetStock().GetId()))
		}
	}
	fetch(c, c.assets, assets, func(id pdid.Id) (*app.Asset, error) {
		return app.NewAssetServiceClient(c.conn).Get(ctx, app.AssetGetById(id.Bytes()))
	})
	fetch(c, c.stocks, stocks, func(id pdid.Id) (*app.Stock, error) {
		return app.NewStockServiceClient(c.conn).Get(ctx, app.StockGetRequest_builder{Ref: app.StockById(id.Bytes())}.Build())
	})

	now := time.Now()
	for i, k := range v.GetItems() {
		item := mcpCustody{
			Id:           idString(k.GetId()),
			Party:        c.named(c.parties, k.GetParty().GetId()),
			Kind:         k.GetKind(),
			Status:       k.GetStatus(),
			Issued:       stamp(k.GetIssuedAt()),
			Due:          stamp(k.GetDueAt()),
			Overdue:      k.GetStatus() == "open" && k.HasDueAt() && k.GetDueAt().AsTime().Before(now),
			Acknowledged: stamp(k.GetAcknowledgedAt()),
			Desc:         k.GetDesc(),
			Lines:        []mcpLine{},
		}
		for _, l := range lines[i] {
			line := mcpLine{
				Id:        idString(l.GetId()),
				Quantity:  l.GetQuantity(),
				Returned:  l.GetReturnedQuantity(),
				Out:       stamp(l.GetOutAt()),
				Back:      stamp(l.GetReturnedAt()),
				Condition: l.GetConditionOut(),
			}
			if id := pid(l.GetAsset().GetId()); !id.IsZero() {
				line.Asset = &mcpRef{Id: id.String()}
				if a := c.assets[id]; a != nil {
					line.Asset.Name = strings.TrimSpace(a.GetTag() + " " + a.GetName())
				}
			}
			if id := pid(l.GetStock().GetId()); !id.IsZero() {
				line.Stock = &mcpRef{Id: id.String()}
				if s := c.stocks[id]; s != nil {
					line.Stock.Name, line.Unit = s.GetName(), s.GetUnit()
				}
			}
			item.Lines = append(item.Lines, line)
		}
		out.Items = append(out.Items, item)
	}
	return nil, out, nil
}

type mcpReservationsIn struct {
	From   string   `json:"from,omitempty" jsonschema:"the span's start: RFC 3339 or a date; now if left out"`
	To     string   `json:"to,omitempty" jsonschema:"the span's end: RFC 3339 or a date; a week after its start if left out"`
	Assets []string `json:"assets,omitempty" jsonschema:"only these assets -- rooms, equipment -- by id or tag; all of them if left out"`
	Mine   bool     `json:"mine,omitempty" jsonschema:"only the signed-in person's own"`
}

type mcpReservation struct {
	Id     string   `json:"id"`
	Name   string   `json:"name,omitempty" jsonschema:"what it is for"`
	Status string   `json:"status" jsonschema:"held, requested, confirmed, in_use, completed, cancelled, rejected, expired or no_show"`
	Begins string   `json:"begins"`
	Ends   string   `json:"ends"`
	For    string   `json:"for,omitempty" jsonschema:"who it is for"`
	Assets []mcpRef `json:"assets" jsonschema:"what is reserved"`
	Series bool     `json:"series,omitempty" jsonschema:"one of a repeating series"`
}

type mcpReservations struct {
	From  string           `json:"from"`
	To    string           `json:"to"`
	Items []mcpReservation `json:"items"`
}

func (t mcpTools) reservations(ctx context.Context, _ *mcp.CallToolRequest, in mcpReservationsIn) (*mcp.CallToolResult, mcpReservations, error) {
	out := mcpReservations{Items: []mcpReservation{}}
	c, err := t.call(ctx)
	if err != nil {
		return nil, out, err
	}
	from := time.Now()
	if in.From != "" {
		if from, err = at("from", in.From); err != nil {
			return nil, out, err
		}
	}
	to := from.AddDate(0, 0, 7)
	if in.To != "" {
		if to, err = at("to", in.To); err != nil {
			return nil, out, err
		}
	}
	req := app.ReservationCalendarRequest_builder{From: timestamppb.New(from), To: timestamppb.New(to), Mine: in.Mine}.Build()
	for _, v := range in.Assets {
		ref, err := c.assetRef(v)
		if err != nil {
			return nil, out, err
		}
		req.SetResources(append(req.GetResources(), ref))
	}
	v, err := app.NewReservationServiceClient(c.conn).Calendar(ctx, req)
	if err != nil {
		return nil, out, failed(err)
	}
	ids := []pdid.Id{}
	for _, e := range v.GetEntries() {
		for _, r := range e.GetResourceIds() {
			ids = append(ids, pid(r))
		}
	}
	fetch(c, c.assets, ids, func(id pdid.Id) (*app.Asset, error) {
		return app.NewAssetServiceClient(c.conn).Get(ctx, app.AssetGetById(id.Bytes()))
	})
	out.From, out.To = from.Local().Format(time.RFC3339), to.Local().Format(time.RFC3339)
	for _, e := range v.GetEntries() {
		r := e.GetReservation()
		item := mcpReservation{
			Id:     idString(r.GetId()),
			Name:   r.GetName(),
			Status: r.GetStatus(),
			Begins: stamp(r.GetBeginsAt()),
			Ends:   stamp(r.GetEndsAt()),
			For:    e.GetPartyName(),
			Assets: []mcpRef{},
			Series: len(r.GetSeriesId()) > 0 || r.GetRrule() != "",
		}
		for _, b := range e.GetResourceIds() {
			ref := mcpRef{Id: idString(b)}
			if a := c.assets[pid(b)]; a != nil {
				ref.Name = a.GetName()
			}
			item.Assets = append(item.Assets, ref)
		}
		out.Items = append(out.Items, item)
	}
	return nil, out, nil
}

type mcpPeopleIn struct {
	Query  string `json:"query,omitempty" jsonschema:"a name or part of it, a code, or a team's name; every one of the kind if left out"`
	Kind   string `json:"kind,omitempty" jsonschema:"only this kind"`
	Limit  int    `json:"limit,omitempty" jsonschema:"how many: 20 if left out, 100 at most"`
	Offset int    `json:"offset,omitempty" jsonschema:"how many to skip, for the next page"`
}

type mcpParty struct {
	Id   string  `json:"id"`
	Name string  `json:"name"`
	Kind string  `json:"kind" jsonschema:"person, team, org or vendor"`
	In   *mcpRef `json:"in,omitempty" jsonschema:"the team a person is in, or the organization a team is in"`
	Code string  `json:"code,omitempty" jsonschema:"an employee number or a vendor code"`
}

type mcpPeople struct {
	Total int        `json:"total" jsonschema:"how many match in all"`
	Items []mcpParty `json:"items"`
}

func (t mcpTools) findPeople(ctx context.Context, _ *mcp.CallToolRequest, in mcpPeopleIn) (*mcp.CallToolResult, mcpPeople, error) {
	out := mcpPeople{Items: []mcpParty{}}
	c, err := t.call(ctx)
	if err != nil {
		return nil, out, err
	}
	n := in.Limit
	if n <= 0 {
		n = 20
	}
	v, err := app.NewPartyServiceClient(c.conn).Search(ctx, app.PartySearchRequest_builder{
		Q:      in.Query,
		Kind:   in.Kind,
		Size:   uint32(min(n, 100)),
		Offset: uint32(max(in.Offset, 0)),
	}.Build())
	if err != nil {
		return nil, out, failed(err)
	}
	ups := []pdid.Id{}
	for _, p := range v.GetItems() {
		c.parties[pid(p.GetId())] = p.GetName()
		ups = append(ups, pid(p.GetParentId()))
	}
	c.fetchParties(ups)
	out.Total = int(v.GetTotal())
	for _, p := range v.GetItems() {
		out.Items = append(out.Items, mcpParty{
			Id:   idString(p.GetId()),
			Name: p.GetName(),
			Kind: p.GetKind(),
			In:   c.named(c.parties, p.GetParentId()),
			Code: p.GetCode(),
		})
	}
	return nil, out, nil
}

type mcpModelsIn struct {
	Query  string `json:"query,omitempty" jsonschema:"a name, who makes it, a code, or what it is a model of; every one if left out"`
	Type   string `json:"type,omitempty" jsonschema:"only models of this type: a type id another answer gave"`
	Limit  int    `json:"limit,omitempty" jsonschema:"how many: 20 if left out, 100 at most"`
	Offset int    `json:"offset,omitempty" jsonschema:"how many to skip, for the next page"`
}

type mcpModel struct {
	Id    string  `json:"id"`
	Name  string  `json:"name"`
	Maker string  `json:"maker,omitempty"`
	Code  string  `json:"code,omitempty"`
	Type  *mcpRef `json:"type,omitempty" jsonschema:"what it is a model of"`
}

type mcpModels struct {
	Total int        `json:"total" jsonschema:"how many match in all"`
	Items []mcpModel `json:"items"`
}

func (t mcpTools) findModels(ctx context.Context, _ *mcp.CallToolRequest, in mcpModelsIn) (*mcp.CallToolResult, mcpModels, error) {
	out := mcpModels{Items: []mcpModel{}}
	c, err := t.call(ctx)
	if err != nil {
		return nil, out, err
	}
	n := in.Limit
	if n <= 0 {
		n = 20
	}
	req := app.ItemModelSearchRequest_builder{
		Q:      in.Query,
		Size:   uint32(min(n, 100)),
		Offset: uint32(max(in.Offset, 0)),
	}.Build()
	if in.Type != "" {
		id, err := idOf("type", in.Type)
		if err != nil {
			return nil, out, err
		}
		req.SetType(app.AssetTypeById(id.Bytes()))
	}
	v, err := app.NewItemModelServiceClient(c.conn).Search(ctx, req)
	if err != nil {
		return nil, out, failed(err)
	}
	types := []pdid.Id{}
	for _, m := range v.GetItems() {
		types = append(types, pid(m.GetType().GetId()))
	}
	fetch(c, c.types, types, func(id pdid.Id) (string, error) {
		v, err := app.NewAssetTypeServiceClient(c.conn).Get(ctx, app.AssetTypeGetRequest_builder{Ref: app.AssetTypeById(id.Bytes())}.Build())
		return v.GetName(), err
	})
	out.Total = int(v.GetTotal())
	for _, m := range v.GetItems() {
		out.Items = append(out.Items, mcpModel{
			Id:    idString(m.GetId()),
			Name:  m.GetName(),
			Maker: m.GetMaker(),
			Code:  m.GetCode(),
			Type:  c.named(c.types, m.GetType().GetId()),
		})
	}
	return nil, out, nil
}
