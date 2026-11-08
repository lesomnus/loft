package cli

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/arg"
	"github.com/lesomnus/xli/flg"
	"github.com/lesomnus/z"

	app "github.com/lesomnus/loft"
)

// searchWritten is `loft search`, the index a search is answered from when
// the deployment names an OpenSearch cluster (D31), and `loft retention`,
// what the keep windows take out of the history: the operator's.
func searchWritten() []written {
	return append(group(newCmdSearch(), map[string][]string{
		"status":  {app.OperatorSearchService_Status_FullMethodName},
		"reindex": {app.OperatorSearchService_Reindex_FullMethodName},
		"remove":  {app.OperatorSearchService_Remove_FullMethodName},
		"analyze": {app.OperatorSearchService_Analyze_FullMethodName},
	}), group(newCmdRetention(), map[string][]string{
		"plan": {app.OperatorRetentionService_Plan_FullMethodName},
		"run":  {app.OperatorRetentionService_Run_FullMethodName},
	})...)
}

func newCmdSearch() *xli.Command {
	return &xli.Command{
		Name:  "search",
		Brief: "the search index: what it holds, making it again or taking it out, and how it splits a text",
		Commands: xli.Commands{
			{
				Name:  "status",
				Brief: "what each index is -- of assets, parties and models -- against what the database holds",
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					conn, err := operatorFor(ctx, self)
					if err != nil {
						return err
					}
					v, err := app.NewOperatorSearchServiceClient(conn).Status(ctx, &app.OperatorSearchStatusRequest{})
					if err != nil {
						return err
					}
					for _, ix := range v.GetIndexes() {
						if ix.GetIndex() == "" {
							self.Printf("%s points at no index: `loft search reindex` makes one (%d %s)\n", ix.GetAlias(), ix.GetHeld(), ix.GetName())
						} else {
							self.Printf("%s → %s\n", ix.GetAlias(), ix.GetIndex())
							self.Printf("  %d documents, %d %s in the database\n", ix.GetDocs(), ix.GetHeld(), ix.GetName())
							if ix.GetVersion() != ix.GetBuilt() {
								self.Printf("  made in shape %d, and this build makes %d: `loft search reindex` makes it again\n", ix.GetVersion(), ix.GetBuilt())
							}
						}
						if len(ix.GetOthers()) > 0 {
							self.Printf("  left by a reindex that stopped, and deleted by the next: %s\n", strings.Join(ix.GetOthers(), ", "))
						}
					}
					return next(ctx)
				}),
			},
			{
				Name:  "reindex",
				Brief: "make each index again from the database, and search it once it is whole",
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					conn, err := operatorFor(ctx, self)
					if err != nil {
						return err
					}
					stream, err := app.NewOperatorSearchServiceClient(conn).Reindex(ctx, &app.OperatorSearchReindexRequest{})
					if err != nil {
						return err
					}
					if err := each(stream, self, func(m *app.OperatorSearchReindexResponse) string {
						if r := m.GetMade(); m.HasMade() {
							self.Printf("made %s with %d %s, and %s points at it\n", r.GetIndex(), r.GetDocs(), r.GetName(), r.GetAlias())
							for _, v := range r.GetReplaced() {
								self.Printf("deleted %s\n", v)
							}
						}
						return m.GetProgress()
					}); err != nil {
						return err
					}
					return next(ctx)
				}),
			},
			{
				Name:  "remove",
				Brief: "delete every index this deployment made, and the aliases with them",
				Flags: flg.Flags{&flg.Switch{Name: "yes", Brief: "do it; without this it only says what it would do"}},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					conn, err := operatorFor(ctx, self)
					if err != nil {
						return err
					}
					v, err := app.NewOperatorSearchServiceClient(conn).Remove(ctx, app.OperatorSearchRemoveRequest_builder{DryRun: !yes(self)}.Build())
					if err != nil {
						return err
					}
					switch {
					case len(v.GetIndexes()) == 0:
						self.Printf("this deployment has no index to delete\n")
					case !yes(self):
						self.Printf("would delete %s (--yes deletes)\n", strings.Join(v.GetIndexes(), ", "))
					default:
						self.Printf("deleted %s\n", strings.Join(v.GetIndexes(), ", "))
					}
					return next(ctx)
				}),
			},
			{
				Name:  "analyze",
				Brief: "the words the index makes of a text: as a name is kept, and as a search asks",
				Args:  arg.Args{&arg.String{Name: "TEXT", Brief: "such as 웹서버"}},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					text, _ := arg.Get[string](self, "TEXT")
					conn, err := operatorFor(ctx, self)
					if err != nil {
						return err
					}
					v, err := app.NewOperatorSearchServiceClient(conn).Analyze(ctx, app.OperatorSearchAnalyzeRequest_builder{Text: text}.Build())
					if err != nil {
						return err
					}
					self.Printf("%-8s %s\n", "kept as", strings.Join(v.GetKept(), " · "))
					self.Printf("%-8s %s\n", "asked as", strings.Join(v.GetAsked(), " · "))
					return next(ctx)
				}),
			},
		},
		Handler: xli.RequireSubcommand(),
	}
}

// newCmdRetention is `loft retention`: what the keep windows take out of the
// history (design 8.2, 8.3). `plan` is the dry run design 8.3 asks for before
// anything; `run` takes it, and only when `app.retention.apply` says the
// deployment may.
func newCmdRetention() *xli.Command {
	pass := func(name, brief string, dry bool) *xli.Command {
		return &xli.Command{
			Name:  name,
			Brief: brief,
			Flags: flg.Flags{&flg.String{Name: "tenant", Brief: "one workspace, by address; every workspace by default"}},
			Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
				conn, err := operatorFor(ctx, self)
				if err != nil {
					return err
				}
				req := &app.OperatorRetentionRequest{}
				if v := strings.TrimPrefix(str(self, "tenant"), "@"); v != "" {
					req.SetRef(app.OperatorTenantRef_builder{Alias: z.Ptr(v)}.Build())
				}
				c := app.NewOperatorRetentionServiceClient(conn)
				call := c.Run
				if dry {
					call = c.Plan
				}
				stream, err := call(ctx, req)
				if err != nil {
					return err
				}
				apply, last := false, false
				if err := each(stream, self, func(m *app.OperatorRetentionResponse) string {
					if m.HasTaken() {
						printTaken(self, m.GetTaken())
					}
					if m.GetLast() {
						apply, last = m.GetApply(), true
					}
					return m.GetProgress()
				}); err != nil {
					return err
				}
				if !last {
					return errors.New("the entry point ended without saying it was done")
				}
				if dry && !apply {
					self.Printf("(app.retention.apply is off: `loft serve` takes none of this, and `loft retention run` refuses to)\n")
				}
				return next(ctx)
			}),
		}
	}
	return &xli.Command{
		Name:  "retention",
		Brief: "what the keep windows take out of the history",
		Commands: xli.Commands{
			pass("plan", "what a pass would take now, changing nothing", true),
			pass("run", "take it, when app.retention.apply says the deployment may", false),
		},
		Handler: xli.RequireSubcommand(),
	}
}

// printTaken is what a workspace's windows took, or would.
func printTaken(self *xli.Command, x *app.OperatorRetentionTaken) {
	switch {
	case !x.HasDateBefore():
		self.Printf("%s: kept forever\n", x.GetAlias())
		return
	case len(x.GetHeld()) > 0:
		self.Printf("%s: held (%s), nothing goes\n", x.GetAlias(), strings.Join(x.GetHeld(), "; "))
		return
	}
	verb := "took"
	if x.GetDryRun() {
		verb = "would take"
	}
	total := uint32(0)
	for _, n := range x.GetTaken() {
		total += n
	}
	order := []string{"placements", "links", "stewardships", "facts", "events", "reservations", "loans", "counts", "work orders", "stock movements", "attachments"}
	parts := []string{}
	for _, k := range order {
		parts = append(parts, fmt.Sprintf("%d %s", x.GetTaken()[k], k))
	}
	for _, k := range slices.Sorted(maps.Keys(x.GetTaken())) {
		if !slices.Contains(order, k) {
			parts = append(parts, fmt.Sprintf("%d %s", x.GetTaken()[k], k))
		}
	}
	self.Printf("%s: what was over before %s\n", x.GetAlias(), x.GetDateBefore().AsTime().UTC().Format(time.DateOnly))
	self.Printf("  history %s %d: %s\n", verb, total, strings.Join(parts, ", "))
	if len(x.GetLost()) > 0 {
		self.Printf("  files that could not be removed: %s\n", strings.Join(x.GetLost(), ", "))
	}
	if !x.GetDryRun() {
		return
	}
	trail := []string{}
	for _, r := range x.GetTrail() {
		trail = append(trail, fmt.Sprintf("%s %d archived, %d discarded, %d destroyed", r.GetKind(), r.GetArchived(), r.GetDiscarded(), r.GetDestroyed()))
	}
	if len(trail) == 0 {
		trail = append(trail, "nothing")
	}
	self.Printf("  trail would take: %s\n", strings.Join(trail, "; "))
}
