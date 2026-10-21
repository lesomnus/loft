package cmd_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/trail"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/aliashold"
	entaudit "github.com/lesomnus/rove/internal/ent/audit"
	entconsent "github.com/lesomnus/rove/internal/ent/consent"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/server/clilogin"
	"github.com/lesomnus/rove/server/closing"
	"github.com/lesomnus/rove/server/consent"
	"github.com/lesomnus/rove/server/pd"
)

// What Rove writes past its servers -- an agreement, a terminal's sign-in, an
// address held -- is on the trail as the act it is (payday#108), so a check of
// the rows finds nothing after it, and finds a row changed past it.

var digestKey = bytes.Repeat([]byte{7}, 32)

// digestWorld is a world whose trail keeps a digest of every row, set up as
// `with` says, and the digest it is checked by.
func digestWorld(t *testing.T, with func(*cmd.Config)) (*world, *audit.Digest) {
	t.Helper()

	w := newWorldWith(t, func(c *cmd.Config) {
		if with != nil {
			with(c)
		}
		c.Audit.Digest.Key = "base64:" + base64.StdEncoding.EncodeToString(digestKey)
	})
	d, err := audit.NewDigest(digestKey)
	require.NoError(t, err)

	return w, d
}

// check is the world's rows against its trail.
func (w *world) check(t *testing.T, d *audit.Digest) audit.Rows {
	t.Helper()

	got, err := pd.Digests(context.Background(), w.s.Ent, d, trail.Policy{})
	require.NoError(t, err)

	return got
}

// acts is what the trail says was done to the row `id` names, oldest first.
func (w *world) acts(t *testing.T, id pdid.Id) []*ent.Audit {
	t.Helper()

	vs, err := w.s.Ent.Audit.Query().
		Where(entaudit.ObjectId(id.Uuid())).
		Order(ent.Asc(entaudit.FieldDateCreated), ent.Asc(entaudit.FieldId)).
		All(context.Background())
	require.NoError(t, err)

	return vs
}

func TestWhatAnAccountAgreedToIsOnTheTrail(t *testing.T) {
	w, d := digestWorld(t, func(c *cmd.Config) {
		c.App.Consent.Terms = terms
		c.App.Consent.Privacy = privacy
	})
	x := w.x
	ctx := context.Background()
	h, err := w.s.Ent.Holder.Query().Where(holder.Role("owner")).First(ctx)
	x.NoError(err)
	owner := pdid.Id(*h.Account)

	x.Equal(http.StatusNoContent, w.post(w.owner, "/account/consents", `{"agree":{"terms":"2026-10-01","privacy":"2026-10-01"}}`).Code)
	rows, err := w.s.Ent.Consent.Query().Where(entconsent.Account(owner.Uuid())).All(ctx)
	x.NoError(err)
	x.Len(rows, 2)
	for _, r := range rows {
		acts := w.acts(t, pdid.Id(r.Id))
		x.Len(acts, 1)
		x.Equal(consent.Agreed, acts[0].Action)
		x.Equal(owner.Uuid(), acts[0].ActorId, "the account's own act")
	}
	got := w.check(t, d)
	x.True(got.Ok(), "%+v", got)

	x.NoError(w.s.Consents.Left(ctx, owner))
	x.Equal(consent.Gone, w.acts(t, pdid.Id(rows[0].Id))[1].Action)
	got = w.check(t, d)
	x.True(got.Ok(), "%+v", got)

	// An agreement changed in the database: to a version nobody agreed to.
	x.NoError(w.s.Ent.Consent.UpdateOneId(rows[0].Id).SetVersion("2026-09-01").Exec(ctx))
	x.Equal([]pdid.Id{pdid.Id(rows[0].Id)}, w.check(t, d).Differs)

	// And destroyed once the account has been gone longer than they are kept
	// (`app.consent.keep`): the deployment's act, the last of each.
	w.s.Consents.Now = func() time.Time { return time.Now().Add(91 * 24 * time.Hour) }
	t.Cleanup(func() { w.s.Consents.Now = nil })
	n, err := w.s.Consents.Sweep(ctx, 90*24*time.Hour)
	x.NoError(err)
	x.Equal(2, n)
	for _, r := range rows {
		acts := w.acts(t, pdid.Id(r.Id))
		x.Equal(consent.Destroyed, acts[len(acts)-1].Action, "destroyed with no word on the trail")
		x.Zero(acts[len(acts)-1].ActorId, "the deployment's act, not the account's")
	}
	got = w.check(t, d)
	x.True(got.Ok(), "%+v", got)
}

func TestATerminalsSignInIsOnTheTrail(t *testing.T) {
	w, d := digestWorld(t, nil)
	x := w.x
	ctx := context.Background()
	h, err := w.s.Ent.Holder.Query().Where(holder.Role("owner")).First(ctx)
	x.NoError(err)
	owner := pdid.Id(*h.Account)

	ask := func() clilogin.Started {
		key := make([]byte, 32)
		_, _ = rand.Read(key)
		res := w.post(ctx, "/cli/logins", `{"secret":"`+base64.RawURLEncoding.EncodeToString(key)+`","client":"a test"}`)
		x.Equal(http.StatusCreated, res.Code, res.Body.String())

		var v clilogin.Started
		x.NoError(json.Unmarshal(res.Body.Bytes(), &v))
		return v
	}
	actions := func(v clilogin.Started) []string {
		id, err := pdid.Parse(v.Id)
		x.NoError(err)
		out := []string{}
		for _, a := range w.acts(t, id) {
			out = append(out, a.Action)
		}
		return out
	}

	approved := ask()
	x.Equal(http.StatusNoContent, w.post(w.owner, "/cli/logins/approve", `{"code":"`+approved.Code+`"}`).Code)
	x.Equal([]string{clilogin.Asked, clilogin.Approved}, actions(approved))
	id, _ := pdid.Parse(approved.Id)
	x.Equal(owner.Uuid(), w.acts(t, id)[1].ActorId, "answered by the account at the page")

	denied := ask()
	x.Equal(http.StatusNoContent, w.post(w.owner, "/cli/logins/deny", `{"code":"`+denied.Code+`"}`).Code)
	x.Equal([]string{clilogin.Asked, clilogin.Denied}, actions(denied))

	got := w.check(t, d)
	x.True(got.Ok(), "%+v", got)
}

func TestAnAddressHeldIsOnTheTrail(t *testing.T) {
	w, d := digestWorld(t, nil)
	x := w.x
	ctx := context.Background()

	old := w.tenant.GetAlias()
	x.NoError(w.s.Closing.Rename(ctx, old, "lab-2", false))
	held, err := w.s.Ent.AliasHold.Query().Where(aliashold.Held(old)).Only(ctx)
	x.NoError(err)
	acts := w.acts(t, pdid.Id(held.Id))
	x.Len(acts, 1)
	x.Equal(closing.Held, acts[0].Action)

	got := w.check(t, d)
	x.True(got.Ok(), "%+v", got)
}
