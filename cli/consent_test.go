package cli_test

import (
	"context"
	"testing"

	"github.com/lesomnus/rove/cli"
	"github.com/lesomnus/rove/cmd"
	entconsent "github.com/lesomnus/rove/internal/ent/consent"
)

// TestATerminalAgreesBeforeItIsServed: what a deployment asks to be agreed to
// is asked of a terminal as of a page -- every call refused, `rove consent`
// says what and where to read it, and `--agree` agrees, recorded as agreed at
// a terminal.
func TestATerminalAgreesBeforeItIsServed(t *testing.T) {
	s := serveWith(t, func(c *cmd.Config) {
		c.App.Consent.Terms = cmd.DocumentConfig{Version: "2026-10-01", Url: "https://rove.example/terms"}
		c.App.Consent.Privacy = cmd.DocumentConfig{Version: "2026-10-01", Url: "https://rove.example/privacy"}
	})
	x := s.x
	s.signIn("owner", "owner1234")

	err := cli.NewCmdApi().Run(context.Background(), []string{"party", "me"})
	x.ErrorContains(err, "rove consent")

	out := run(t, cli.NewCmdConsent())
	x.Contains(out, "owed\t이용약관 (terms)\t2026-10-01\thttps://rove.example/terms")
	x.Contains(out, "owed\t개인정보 처리방침 (privacy)\t2026-10-01\thttps://rove.example/privacy")
	x.Contains(out, "rove consent --agree")
	err = cli.NewCmdApi().Run(context.Background(), []string{"party", "me"})
	x.ErrorContains(err, "rove consent", "reading what is owed agrees to nothing")

	x.Contains(run(t, cli.NewCmdConsent(), "--agree"), "agreed")
	run(t, cli.NewCmdApi(), "party", "me")
	out = run(t, cli.NewCmdConsent())
	x.Contains(out, "agreed\tterms\t2026-10-01")
	x.Contains(out, "agreed\tprivacy\t2026-10-01")

	rows, err := s.s.Ent.Consent.Query().Where(entconsent.Client("cli")).Count(context.Background())
	x.NoError(err)
	x.Equal(2, rows, "agreed at a terminal")
}
