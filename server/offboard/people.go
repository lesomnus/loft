package offboard

import (
	"context"
	"uuid"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/internal/ent/invitation"
	"github.com/lesomnus/rove/internal/ent/party"
)

// People is whose data an export of `tenant` carries, as the access record
// names them (rove#136): every person record, every membership, and everybody
// an invitation was addressed to. An export is a download of all of them, and
// its row says so.
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
	is, err := d.Ent.Invitation.Query().Where(invitation.TenantId(t)).Ids(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]access.Subject, 0, len(ps)+len(hs)+len(is))
	for _, vs := range [][]uuid.UUID{ps, hs, is} {
		for _, v := range vs {
			out = append(out, access.Subject{Id: pdid.Id(v), Tenant: tenant})
		}
	}

	return out, nil
}
