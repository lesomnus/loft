package cli

import (
	"context"
	"strings"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"

	"github.com/lesomnus/loft/cmd"
)

// flag is a string flag of a command, as it was given, without the spaces
// around it.
func flag(self *xli.Command, name string) string {
	v, _ := flg.Find[string](self, name)
	return strings.TrimSpace(v)
}

// or is v, or what to say where there is none.
func or(v, otherwise string) string {
	if v == "" {
		return otherwise
	}
	return v
}

// fromBeforeRoster says whether this deployment still has the table it kept
// passwords in before roster held them: one `loft-admin identity migrate` has
// not brought in, whose people cannot sign in.
func fromBeforeRoster(ctx context.Context, s *cmd.Server) (bool, error) {
	q := "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'credential'"
	if s.Dialect == "postgres" {
		q = "SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'credential'"
	}
	n := 0
	if err := s.Db.QueryRowContext(ctx, q).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}
