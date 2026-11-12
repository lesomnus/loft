package offboard

import (
	"context"
	"uuid"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/loft/internal/ent/holder"
	"github.com/lesomnus/loft/internal/ent/party"
)

// People is whose data an export of `tenant` carries, as the access record
// names them (loft#136): every person record and every membership. An export
// is a download of all of them, and its row says so.
func (d Deployment) People(ctx context.Context, tenant pdid.Id) ([]access.Subject, error) {
	t := tenant.Uuid()
	ps, err := d.Ent.Party.Query().Where(party.TenantId(t)).Ids(ctx)
	if err != nil {
		return nil, err
	}
	hs, err := d.Ent.Holder.Query().Where(holder.TenantId(t)).Ids(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]access.Subject, 0, len(ps)+len(hs))
	for _, vs := range [][]uuid.UUID{ps, hs} {
		for _, v := range vs {
			out = append(out, access.Subject{Id: pdid.Id(v), Tenant: tenant})
		}
	}

	return out, nil
}
