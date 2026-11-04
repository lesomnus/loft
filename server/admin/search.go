package admin

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/asset"
	"github.com/lesomnus/loft/internal/ent/itemmodel"
	"github.com/lesomnus/loft/internal/ent/party"
	"github.com/lesomnus/loft/server/opensearch"
)

// operatorSearch is `OperatorSearchService`: the index a search is answered
// from, when the deployment names an OpenSearch cluster (D31) -- what the
// shell's `loft search` did on the box.
type operatorSearch struct {
	app.UnimplementedOperatorSearchServiceServer
	a Routes
}

// cluster is the cluster the deployment names, or a refusal saying it names
// none: such a deployment searches its database, and has no index.
func (s operatorSearch) cluster() (*opensearch.Client, error) {
	if s.a.S.Search == nil {
		return nil, status.Error(codes.FailedPrecondition, "app.search.opensearch.addrs names no cluster: this deployment searches its database, and has no index (LOFT_APP_SEARCH_OPENSEARCH_ADDRS)")
	}
	return s.a.S.Search, nil
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

// Status is each index against what the database holds.
func (s operatorSearch) Status(ctx context.Context, _ *app.OperatorSearchStatusRequest) (*app.OperatorSearchStatusResponse, error) {
	o, err := s.cluster()
	if err != nil {
		return nil, err
	}
	out := &app.OperatorSearchStatusResponse{}
	for _, ix := range opensearch.Indexes {
		st, err := o.Status(ctx, ix)
		if err != nil {
			return nil, err
		}
		n, err := held(ctx, s.a.S.Ent, ix)
		if err != nil {
			return nil, err
		}
		out.SetIndexes(append(out.GetIndexes(), app.OperatorSearchIndex_builder{
			Name:    ix.Name,
			Alias:   st.Alias,
			Index:   st.Index,
			Docs:    uint64(st.Docs),
			Held:    uint64(n),
			Version: int32(st.Version),
			Built:   int32(ix.Version),
			Others:  st.Others,
		}.Build()))
	}
	return out, nil
}

// Reindex makes each index again from the database (`opensearch.Reindex`),
// saying every ten thousand documents how far it has got and every heartbeat
// that it is still at it, and each index as it is made.
func (s operatorSearch) Reindex(_ *app.OperatorSearchReindexRequest, out grpc.ServerStreamingServer[app.OperatorSearchReindexResponse]) error {
	ctx := out.Context()
	o, err := s.cluster()
	if err != nil {
		return err
	}
	say := func(m string) error { return out.Send(app.OperatorSearchReindexResponse_builder{Progress: m}.Build()) }
	for _, ix := range opensearch.Indexes {
		var r opensearch.Reindexed
		if err := working(ctx, "reindexing "+ix.Name, say, func(ctx context.Context, p progress) error {
			var err error
			r, err = opensearch.Reindex(ctx, o, s.a.S.Ent, ix, time.Now(), func(n int) {
				if n%10000 == 0 {
					p.say("%d %s", n, ix.Name)
				}
			})
			return err
		}); err != nil {
			return err
		}
		if err := out.Send(app.OperatorSearchReindexResponse_builder{Made: app.OperatorSearchReindexed_builder{
			Name:     ix.Name,
			Index:    r.Index,
			Alias:    o.Alias(ix),
			Docs:     uint64(r.Docs),
			Replaced: r.Replaced,
		}.Build()}.Build()); err != nil {
			return err
		}
	}
	return nil
}

// Remove deletes every index this deployment made, the aliases with them --
// or says which it would.
func (s operatorSearch) Remove(ctx context.Context, req *app.OperatorSearchRemoveRequest) (*app.OperatorSearchRemoveResponse, error) {
	o, err := s.cluster()
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, ix := range opensearch.Indexes {
		st, err := o.Status(ctx, ix)
		if err != nil {
			return nil, err
		}
		if st.Index != "" {
			names = append(names, st.Index)
		}
		names = append(names, st.Others...)
	}
	if len(names) > 0 && !req.GetDryRun() {
		if err := o.Remove(ctx); err != nil {
			return nil, err
		}
	}
	return app.OperatorSearchRemoveResponse_builder{Indexes: names}.Build(), nil
}

// Analyze is the words the index makes of a text, as a name is kept and as a
// search asks.
func (s operatorSearch) Analyze(ctx context.Context, req *app.OperatorSearchAnalyzeRequest) (*app.OperatorSearchAnalyzeResponse, error) {
	o, err := s.cluster()
	if err != nil {
		return nil, err
	}
	out := &app.OperatorSearchAnalyzeResponse{}
	for _, v := range []struct {
		analyzer string
		set      func([]string)
	}{{"ko", out.SetKept}, {"ko_search", out.SetAsked}} {
		words, err := o.Analyze(ctx, v.analyzer, req.GetText())
		if err != nil {
			if opensearch.IsNotFound(err) {
				return nil, status.Errorf(codes.FailedPrecondition, "%s points at no index: `search reindex` makes one", o.Alias(opensearch.Assets))
			}
			return nil, err
		}
		v.set(words)
	}
	return out, nil
}
