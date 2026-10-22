package domain

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"uuid"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/pdid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent/itemmodel"
	"github.com/lesomnus/rove/internal/ent/party"
	"github.com/lesomnus/rove/internal/ent/predicate"
	"github.com/lesomnus/rove/server/opensearch"
)

// The searches of what an asset takes a name from -- people, teams and the
// rest, and models -- as `AssetService.Search` is of assets (rove#91): a
// deployment that names a search engine asks it (D31), and what it finds is
// read through the wall, best first; one that names none asks the database,
// which reads every row of the tenant. With nothing typed it is the filters
// alone, and the database answers it either way.

// page is what a search asked for, within what it may: fifty if it said
// nothing, five hundred at most, and no further into what the engine finds
// than it pages.
func page(size, offset uint32, engine bool) (int, int, error) {
	n := int(size)
	if n <= 0 || n > 500 {
		n = 50
	}
	if engine && int(offset)+n > opensearch.Window {
		return 0, 0, invalid("offset", "검색은 앞의 %d건까지만 넘겨 볼 수 있습니다", opensearch.Window)
	}
	return n, int(offset), nil
}

// unanswered is what a search the engine did not answer says: the engine's
// error is the log's, and the caller is asked to try again rather than
// answered from the database behind its back.
func (t *Tx) unanswered(err error) error {
	log.From(t.ctx).WarnContext(t.ctx, "search: the engine did not answer", slog.Any("err", err))
	return status.Error(codes.Unavailable, "지금은 검색할 수 없습니다. 잠시 뒤에 다시 해 보세요")
}

// Search finds people, teams, organizations and vendors by a name or part of
// it, a code, or the team one is in.
func (s domainParty) Search(ctx context.Context, req *app.PartySearchRequest) (*app.PartySearchResponse, error) {
	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	kind := req.GetKind()
	if kind != "" && !slices.Contains(PartyKinds, kind) {
		return nil, invalid("kind", "구분이 올바르지 않습니다 (%s)", strings.Join(PartyKinds, ", "))
	}
	q := strings.TrimSpace(req.GetQ())
	engine := q != "" && t.deps.Finder != nil
	size, offset, err := page(req.GetSize(), req.GetOffset(), engine)
	if err != nil {
		return nil, err
	}

	var keys []uuid.UUID
	var total int
	if engine {
		ks, n, err := t.deps.Finder.SearchParties(t.ctx, opensearch.PartyQuery{Tenant: t.tenant.Uuid(), Q: q, Kind: kind, Offset: offset, Size: size})
		if err != nil {
			return nil, t.unanswered(err)
		}
		keys, total = ks, int(n)
	} else {
		ps := []predicate.Party{party.TenantId(t.tenant.Uuid()), party.DateErasedIsNil()}
		if kind != "" {
			ps = append(ps, party.Kind(kind))
		}
		if q != "" {
			ps = append(ps, predicate.Party(like(t.postgres(), []string{party.FieldName, party.FieldCode}, q)))
		}
		rows := t.db.Party.Query().Where(ps...)
		if total, err = rows.Clone().Count(ctx); err != nil {
			return nil, err
		}
		if keys, err = rows.Order(party.ByName(), party.ById()).Offset(offset).Limit(size).Ids(ctx); err != nil {
			return nil, err
		}
	}

	items := make([]*app.Party, 0, len(keys))
	for _, k := range keys {
		// One the caller may not see, or that went since it was found, is
		// left out.
		v, err := t.next.Party().Get(t.ctx, app.PartyGetRequest_builder{Ref: app.PartyById(pdid.Id(k).Bytes())}.Build())
		if err != nil {
			continue
		}
		items = append(items, v)
	}
	return app.PartySearchResponse_builder{Items: items, Total: uint32(total)}.Build(), nil
}

// Search finds models by a name, who makes it, a code, or what it is a model
// of.
func (s domainItemModel) Search(ctx context.Context, req *app.ItemModelSearchRequest) (*app.ItemModelSearchResponse, error) {
	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	q := strings.TrimSpace(req.GetQ())
	engine := q != "" && t.deps.Finder != nil
	size, offset, err := page(req.GetSize(), req.GetOffset(), engine)
	if err != nil {
		return nil, err
	}
	var kind uuid.UUID
	if req.HasType() {
		kind = uuidOf(req.GetType().GetId())
	}

	var keys []uuid.UUID
	var total int
	if engine {
		ks, n, err := t.deps.Finder.SearchModels(t.ctx, opensearch.ModelQuery{Tenant: t.tenant.Uuid(), Q: q, Type: kind, Offset: offset, Size: size})
		if err != nil {
			return nil, t.unanswered(err)
		}
		keys, total = ks, int(n)
	} else {
		ps := []predicate.ItemModel{itemmodel.TenantId(t.tenant.Uuid()), itemmodel.DateErasedIsNil()}
		if kind != uuid.Nil() {
			ps = append(ps, itemmodel.TypeId(kind))
		}
		if q != "" {
			ps = append(ps, predicate.ItemModel(like(t.postgres(), []string{itemmodel.FieldName, itemmodel.FieldMaker, itemmodel.FieldCode}, q)))
		}
		rows := t.db.ItemModel.Query().Where(ps...)
		if total, err = rows.Clone().Count(ctx); err != nil {
			return nil, err
		}
		if keys, err = rows.Order(itemmodel.ByName(), itemmodel.ById()).Offset(offset).Limit(size).Ids(ctx); err != nil {
			return nil, err
		}
	}

	items := make([]*app.ItemModel, 0, len(keys))
	for _, k := range keys {
		v, err := t.next.ItemModel().Get(t.ctx, app.ItemModelGetRequest_builder{Ref: app.ItemModelById(pdid.Id(k).Bytes())}.Build())
		if err != nil {
			continue
		}
		items = append(items, v)
	}
	return app.ItemModelSearchResponse_builder{Items: items, Total: uint32(total)}.Build(), nil
}
