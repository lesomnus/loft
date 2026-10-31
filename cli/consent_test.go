package cli_test

import (
	"context"
	"testing"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/loft/cli"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/ent/holder"
)

// TestATerminalAgreesBeforeItIsServed: what a deployment asks to be agreed to
// is asked of a terminal as of a page -- every call refused, `loft consent`
// says what and where to read it, and `--agree` agrees, recorded as agreed at
// a terminal.
func TestATerminalAgreesBeforeItIsServed(t *testing.T) {
	s := serveWith(t, func(c *cmd.Config) {
		c.App.Consent.Terms = cmd.DocumentConfig{Version: "2026-10-01", Url: "https://loft.example/terms"}
		c.App.Consent.Privacy = cmd.DocumentConfig{Version: "2026-10-01", Url: "https://loft.example/privacy"}
	})
	x := s.x
	s.signIn("owner", "owner1234")

	err := cli.NewCmdApi().Run(context.Background(), []string{"party", "me"})
	x.ErrorContains(err, "loft consent")

	out := run(t, cli.NewCmdConsent())
	x.Contains(out, "owed\t이용약관 (terms)\t2026-10-01\thttps://loft.example/terms")
	x.Contains(out, "owed\t개인정보 처리방침 (privacy)\t2026-10-01\thttps://loft.example/privacy")
	x.Contains(out, "loft consent --agree")
	err = cli.NewCmdApi().Run(context.Background(), []string{"party", "me"})
	x.ErrorContains(err, "loft consent", "reading what is owed agrees to nothing")

	x.Contains(run(t, cli.NewCmdConsent(), "--agree"), "agreed")
	run(t, cli.NewCmdApi(), "party", "me")
	out = run(t, cli.NewCmdConsent())
	x.Contains(out, "agreed\tterms\t2026-10-01")
	x.Contains(out, "agreed\tprivacy\t2026-10-01")

	h, err := s.s.Ent.Holder.Query().Where(holder.Role("owner")).First(context.Background())
	x.NoError(err)
	vs, err := s.s.Identity.Agreements(context.Background(), pdid.Id(*h.Account))
	x.NoError(err)
	x.Len(vs, 2)
	for _, v := range vs {
		x.Equal("cli", v.Client, "agreed at a terminal")
	}
}
