package identity

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/spin"
	"github.com/lesomnus/roster/rstr"
)

var (
	terms   = Document{Name: "terms", Version: "2026-10-01"}
	privacy = Document{Name: "privacy", Version: "2026-10-01"}
)

// TestWhatAnAccountAgreedToIsRostersOnEitherRoster: what an account agreed to
// is asked of roster's ledger and recorded there the same way whichever roster
// its account is in (D37) -- owed until it agrees, each document once at a
// version however often it is sent again, a new version a row of its own, and
// read back newest first with what it was given through.
func TestWhatAnAccountAgreedToIsRostersOnEitherRoster(t *testing.T) {
	for _, r := range bothRosters(t) {
		t.Run(r.name, func(t *testing.T) {
			x := require.New(t)
			ctx := t.Context()
			docs := []Document{terms, privacy}

			owed, err := r.s.Owed(ctx, r.account, docs)
			x.NoError(err)
			x.Equal(docs, owed, "agreed to nothing yet")
			vs, err := r.s.Agreements(ctx, r.account)
			x.NoError(err)
			x.Empty(vs)

			x.NoError(r.s.Agree(ctx, r.account, docs, "web"))
			owed, err = r.s.Owed(ctx, r.account, docs)
			x.NoError(err)
			x.Empty(owed)
			x.NoError(r.s.Agree(ctx, r.account, docs, "cli"), "agreed again")
			vs, err = r.s.Agreements(ctx, r.account)
			x.NoError(err)
			x.Len(vs, 2, "once each, however often it is sent")
			for _, v := range vs {
				x.Equal("2026-10-01", v.Version)
				x.Equal("web", v.Client, "as it was first agreed to")
				x.WithinDuration(time.Now(), v.At, time.Minute)
			}

			next := Document{Name: "terms", Version: "2027-01-01"}
			owed, err = r.s.Owed(ctx, r.account, []Document{next, privacy})
			x.NoError(err)
			x.Equal([]Document{next}, owed, "a new version is owed, and only that")
			x.NoError(r.s.Agree(ctx, r.account, owed, "cli"))
			vs, err = r.s.Agreements(ctx, r.account)
			x.NoError(err)
			x.Len(vs, 3, "every version agreed to stays")
			x.Equal(Agreement{Document: "terms", Version: "2027-01-01", Client: "cli", At: vs[0].At}, vs[0], "newest first")

			if r.s.em != nil {
				// The deployment's documents are the ledger's empty scope, as
				// roster's `docs/apps.md` says Loft keeps them: a workspace's
				// own policy would be a scope of its own.
				rows, err := r.s.em.rs.Ent.Consent.Query().All(ctx)
				x.NoError(err)
				x.Len(rows, 3)
				for _, v := range rows {
					x.Empty(v.Scope)
				}
			}

			_, err = r.s.Owed(ctx, pdid.New(2), docs)
			x.ErrorIs(err, ErrNoPerson, "somebody roster does not have")
		})
	}
}

// TestARoleFromBeforeTheLedgerIsSaidSoAndThenUsed: an external roster's role
// for Loft installed before Loft kept agreements there refuses the ledger.
// That is a roster that cannot be asked, in words that say what to do and
// with nothing of roster's refusal for a caller of Loft to read -- and the
// role patched as the README says is used at the next asking, with no
// restart.
func TestARoleFromBeforeTheLedgerIsSaidSoAndThenUsed(t *testing.T) {
	x := require.New(t)
	e := newExternal(t)
	e.role(t, slices.DeleteFunc(slices.Clone(AgentMethods), func(m string) bool {
		return strings.HasPrefix(m, "/roster.ConsentService/")
	}))
	s := e.open(t, e.key, "acme")
	docs := []Document{terms}

	_, err := s.Owed(t.Context(), e.kim, docs)
	x.ErrorIs(err, ErrUnreachable)
	x.ErrorContains(err, "roster role patch")
	_, isStatus := status.FromError(err)
	x.False(isStatus, "roster's refusal of Loft is not a status a caller of Loft is answered with")

	e.role(t, AgentMethods)
	x.Eventually(func() bool {
		owed, err := s.Owed(t.Context(), e.kim, docs)
		return err == nil && slices.Equal(docs, owed)
	}, 10*time.Second, 50*time.Millisecond)
}

// TestARoleFromBeforeTheLedgerIsWidenedInThisProcess: the roster in this
// process is Loft's own, so a role an older Loft wrote there is widened to
// what this one asks the next time it starts, ledger and all.
func TestARoleFromBeforeTheLedgerIsWidenedInThisProcess(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	drv, dsn := pdtest.DB(t)
	cfg := Config{Db: config.DbConfig{Driver: drv, Dsn: dsn}}
	dir := t.TempDir()

	before, err := Open(ctx, cfg, dir, slog.Default())
	x.NoError(err)
	kim, _, err := before.MakeDirectory(ctx, "kim", "Kim")
	x.NoError(err)
	// Loft's holder and its role are written at the first asking.
	_, err = before.Lookup(ctx, "kim")
	x.NoError(err)
	role := func(s *Store) *rstr.Role {
		t.Helper()
		own, cancel := unframed(ctx)
		defer cancel()
		r, err := s.em.rs.Ungated.Role().Get(own, rstr.RoleGetRequest_builder{
			Ref: rstr.RoleRef_builder{Slug: rstr.RoleRefBySlug_builder{
				Alias:  z.Ptr(Agent),
				Tenant: rstr.TenantRef_builder{Alias: z.Ptr(DefaultDirectory)}.Build(),
			}.Build()}.Build(),
		}.Build())
		x.NoError(err)
		return r
	}
	older := slices.DeleteFunc(slices.Clone(AgentMethods), func(m string) bool {
		return strings.HasPrefix(m, "/roster.ConsentService/")
	})
	own, cancel := unframed(ctx)
	_, err = before.em.rs.Ungated.Role().Patch(own, rstr.RolePatchRequest_builder{
		Ref:              rstr.RoleRef_builder{Id: role(before).GetId()}.Build(),
		Methods:          older,
		DateUpdatedForce: z.Ptr(true),
	}.Build())
	cancel()
	x.NoError(err)
	x.NoError(before.Close())

	s, err := Open(ctx, cfg, dir, slog.Default())
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	owed, err := s.Owed(ctx, kim.Id, []Document{terms})
	x.NoError(err, "asked as the role this build widened")
	x.Equal([]Document{terms}, owed)
	x.ElementsMatch(AgentMethods, role(s).GetMethods())
}

// TestTheRosterInThisProcessKeepsAgreementsForConsentKeep: what an erased
// account agreed to is the roster in this process's to destroy, once
// `auth.roster.consent_keep` is over -- its own housekeeping, as a roster of
// its own does it -- and kept for good while that says nothing. A roster of
// its own is not told it: it keeps them as its own configuration says.
func TestTheRosterInThisProcessKeepsAgreementsForConsentKeep(t *testing.T) {
	sweeper := func(s *Store) spin.Spinner {
		for _, v := range s.Spinners() {
			if n, ok := v.(spin.Named); ok && n.SpinName() == "roster.consent" {
				return v.(spin.Spinner)
			}
		}
		return nil
	}
	erased := func(t *testing.T, keep time.Duration) *Store {
		t.Helper()
		x := require.New(t)
		ctx := t.Context()
		drv, dsn := pdtest.DB(t)
		s, err := Open(ctx, Config{Db: config.DbConfig{Driver: drv, Dsn: dsn}, ConsentKeep: keep}, t.TempDir(), slog.Default())
		x.NoError(err)
		t.Cleanup(func() { s.Close() })
		_, _, err = s.MakeDirectory(ctx, "kim", "Kim")
		x.NoError(err)
		lee, err := s.AddPerson(ctx, "lee", "Lee", pdid.Nil)
		x.NoError(err)
		x.NoError(s.Agree(ctx, lee.Id, []Document{terms, privacy}, "web"))

		own, cancel := unframed(ctx)
		defer cancel()
		_, err = s.em.rs.Ungated.Holder().Erase(own, rstr.HolderRef_builder{Id: lee.Id.Bytes()}.Build())
		x.NoError(err)

		return s
	}

	t.Run("kept for good while it says nothing", func(t *testing.T) {
		x := require.New(t)
		s := erased(t, 0)
		x.Nil(sweeper(s), "nothing destroys what was agreed to")
		n, err := s.em.rs.Ent.Consent.Query().Count(t.Context())
		x.NoError(err)
		x.Equal(2, n, "the agreements outlive the account")
	})
	t.Run("destroyed once it is over", func(t *testing.T) {
		x := require.New(t)
		s := erased(t, time.Nanosecond)
		sweep := sweeper(s)
		x.NotNil(sweep, "auth.roster.consent_keep did not reach the roster in this process")

		run, stop := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- sweep.Spin(run) }()
		t.Cleanup(func() {
			stop()
			<-done
		})
		x.Eventually(func() bool {
			n, err := s.em.rs.Ent.Consent.Query().Count(t.Context())
			return err == nil && n == 0
		}, 10*time.Second, 50*time.Millisecond)
	})
	t.Run("and a roster of its own is not told it", func(t *testing.T) {
		x := require.New(t)
		_, err := Open(t.Context(), Config{Addr: "roster.invalid:443", Key: "rk_x", ConsentKeep: time.Hour}, "", slog.Default())
		x.ErrorContains(err, "auth.roster.consent_keep")
	})
}
