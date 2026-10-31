package cli

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/arg"
	"github.com/lesomnus/xli/flg"

	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/asset"
	"github.com/lesomnus/loft/internal/ent/itemmodel"
	"github.com/lesomnus/loft/internal/ent/party"
	"github.com/lesomnus/loft/internal/identity"
	"github.com/lesomnus/loft/server/opensearch"
)

// NewCmdSearch is `loft search`: the index a search is answered from, when
// the deployment names an OpenSearch cluster (D31).
func NewCmdSearch(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "search",
		Brief: "the search index: what it holds, making it again or taking it out, and how it splits a text",

		Commands: xli.Commands{
			{
				Name:  "status",
				Brief: "what each index is -- of assets, parties and models -- against what the database holds",
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					return searchStatus(ctx, self, c)
				}),
			},
			{
				Name:  "reindex",
				Brief: "make each index again from the database, and search it once it is whole",
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					return searchReindex(ctx, self, c)
				}),
			},
			{
				Name:  "remove",
				Brief: "delete every index this deployment made, and the aliases with them",
				Flags: flg.Flags{
					&flg.Switch{Name: "yes", Brief: "do it; without this it only says what it would do"},
				},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					yes, _ := flg.Find[bool](self, "yes")
					return searchRemove(ctx, self, c, yes)
				}),
			},
			{
				Name:  "analyze",
				Brief: "the words the index makes of a text: as a name is kept, and as a search asks",
				Args:  arg.Args{&arg.String{Name: "TEXT", Brief: "such as 웹서버"}},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					text, _ := arg.Get[string](self, "TEXT")
					return searchAnalyze(ctx, self, c, text)
				}),
			},
		},
	}
}

// searchClient is the cluster the configuration names, or an error saying it
// names none: such a deployment searches its database, and has no index.
func searchClient(c *cmd.Config) (*opensearch.Client, error) {
	o, err := opensearch.New(c.App.Search.Opensearch, identity.ResolveRef)
	if err != nil {
		return nil, err
	}
	if o == nil {
		return nil, errors.New("app.search.opensearch.addrs names no cluster: this deployment searches its database, and has no index (LOFT_APP_SEARCH_OPENSEARCH_ADDRS)")
	}
	return o, nil
}

// held is how many rows of what ix holds documents of the database has, not
// erased.
func held(ctx context.Context, db *ent.Client, ix *opensearch.Index) (int, error) {
	switch ix {
	case opensearch.Parties:
		return db.Party.Query().Where(party.DateErasedIsNil()).Count(ctx)
	case opensearch.Models:
		return db.ItemModel.Query().Where(itemmodel.DateErasedIsNil()).Count(ctx)
	}
	return db.Asset.Query().Where(asset.DateErasedIsNil()).Count(ctx)
}

// searchStatus is `loft search status`.
func searchStatus(ctx context.Context, self *xli.Command, c *cmd.Config) error {
	o, err := searchClient(c)
	if err != nil {
		return err
	}
	defer o.Close()
	s, err := cmd.Build(ctx, *c)
	if err != nil {
		return err
	}
	defer s.Close()

	for _, ix := range opensearch.Indexes {
		st, err := o.Status(ctx, ix)
		if err != nil {
			return err
		}
		n, err := held(ctx, s.Ent, ix)
		if err != nil {
			return err
		}

		if st.Index == "" {
			self.Printf("%s points at no index: `loft search reindex` makes one (%d %s)\n", st.Alias, n, ix.Name)
		} else {
			self.Printf("%s → %s\n", st.Alias, st.Index)
			self.Printf("  %d documents, %d %s in the database\n", st.Docs, n, ix.Name)
			if st.Version != ix.Version {
				self.Printf("  made in shape %d, and this build makes %d: `loft search reindex` makes it again\n", st.Version, ix.Version)
			}
		}
		if len(st.Others) > 0 {
			self.Printf("  left by a reindex that stopped, and deleted by the next: %s\n", strings.Join(st.Others, ", "))
		}
	}
	return nil
}

// searchReindex is `loft search reindex`.
func searchReindex(ctx context.Context, self *xli.Command, c *cmd.Config) error {
	o, err := searchClient(c)
	if err != nil {
		return err
	}
	defer o.Close()
	s, err := cmd.Build(ctx, *c)
	if err != nil {
		return err
	}
	defer s.Close()

	for _, ix := range opensearch.Indexes {
		r, err := opensearch.Reindex(ctx, o, s.Ent, ix, time.Now(), func(n int) {
			if n%10000 == 0 {
				self.Printf("  %d %s\n", n, ix.Name)
			}
		})
		if err != nil {
			return err
		}
		self.Printf("made %s with %d %s, and %s points at it\n", r.Index, r.Docs, ix.Name, o.Alias(ix))
		for _, v := range r.Replaced {
			self.Printf("deleted %s\n", v)
		}
	}
	return nil
}

// searchRemove is `loft search remove`: what a deployment that stops
// searching with OpenSearch does, and one that wants nothing of its people
// left in a copy it does not keep current.
func searchRemove(ctx context.Context, self *xli.Command, c *cmd.Config, yes bool) error {
	o, err := searchClient(c)
	if err != nil {
		return err
	}
	defer o.Close()

	names := []string{}
	for _, ix := range opensearch.Indexes {
		st, err := o.Status(ctx, ix)
		if err != nil {
			return err
		}
		if st.Index != "" {
			names = append(names, st.Index)
		}
		names = append(names, st.Others...)
	}
	if len(names) == 0 {
		self.Printf("this deployment has no index to delete\n")
		return nil
	}
	if !yes {
		self.Printf("would delete %s (--yes deletes)\n", strings.Join(names, ", "))
		return nil
	}
	if err := o.Remove(ctx); err != nil {
		return err
	}
	self.Printf("deleted %s\n", strings.Join(names, ", "))
	return nil
}

// searchAnalyze is `loft search analyze`.
func searchAnalyze(ctx context.Context, self *xli.Command, c *cmd.Config, text string) error {
	o, err := searchClient(c)
	if err != nil {
		return err
	}
	defer o.Close()

	for _, v := range []struct{ name, analyzer string }{{"kept as", "ko"}, {"asked as", "ko_search"}} {
		words, err := o.Analyze(ctx, v.analyzer, text)
		if err != nil {
			if opensearch.IsNotFound(err) {
				return errors.New(o.Alias(opensearch.Assets) + " points at no index: `loft search reindex` makes one")
			}
			return err
		}
		self.Printf("%-8s %s\n", v.name, strings.Join(words, " · "))
	}
	return nil
}
