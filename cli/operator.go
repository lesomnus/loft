package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// What the operator asks of the entry point (D74) is written here at its
// schema paths, beside the commands the tree builds (design 9.16): what
// `loft-admin` asked before, each saying what it would do until `--yes`, what
// a long call is at on stderr and what it did on stdout, and failing when a
// check found something, so that a schedule can run it. Most are the
// operator's alone and say so in their help -- a workspace's closing,
// restoring and export among them (D83); a few -- a workspace's list, its
// access reviews -- are either kind's, as the RPCs they call are.

// called is how the command running was written: `loft tenant name-owner`.
func called(self *xli.Command) string {
	names := []string{}
	for _, c := range self.Tree() {
		names = append(names, c.Name)
	}
	return "`" + strings.Join(names, " ") + "`"
}

// operatorFor is a connection to the entry point as the operator the
// connection in use is. `self` is the operator's alone, so a person's
// connection is refused before anything is asked, in the words a call of the
// other kind is told ([refusal]).
func operatorFor(ctx context.Context, self *xli.Command) (grpc.ClientConnInterface, error) {
	_, c, err := chooseFor(ctx)
	if err != nil {
		return nil, err
	}
	if c.Kind != Operator {
		return nil, status.Errorf(codes.PermissionDenied, "%s is the operator's, and this is a person's connection: use an operator's (--key, or `loft auth login --operator`)", called(self))
	}
	return c.dial()
}

// yes is whether the command was told to do it: without it, a command says
// what it would do and does nothing.
func yes(self *xli.Command) bool {
	v, _ := flg.Find[bool](self, "yes")
	return v
}

// yesFlag is `--yes`, for a command that says what it would do without it.
func yesFlag() *flg.Switch {
	return &flg.Switch{Name: "yes", Brief: "do it; without this it only says what it would do"}
}

// nothing says that nothing was done, and how to do it.
func nothing(self *xli.Command) {
	self.Printf("(nothing was done: `--yes` does it)\n")
}

func str(self *xli.Command, name string) string {
	v, _ := flg.Find[string](self, name)
	return strings.TrimSpace(v)
}

// dayOf is the day of a moment as the operator's commands say it: in UTC, as
// the shell's do, whatever zone the operator's own machine is in -- and so
// the day a date given to one (`--effective 2026-01-01`) was given as.
func dayOf(t *timestamppb.Timestamp) string {
	if t == nil {
		return "-"
	}
	return t.AsTime().UTC().Format(time.DateOnly)
}

// momentOf is a moment as the operator's commands say one, in UTC.
func momentOf(t *timestamppb.Timestamp) string {
	if t == nil {
		return "-"
	}
	return t.AsTime().UTC().Format(time.RFC3339)
}

// days is a count of days as a command says it, and what 0 means in words.
func days(n uint32, zero string) string {
	if n == 0 {
		return zero
	}
	return fmt.Sprintf("%dd", n)
}

// idSaid is an identifier as a command says it, and `-` for none.
func idSaid(b []byte) string {
	id, err := pdid.From(b)
	if err != nil {
		return "-"
	}
	return id.String()
}

// instant reads a moment as RFC 3339, or a date as the start of that day in
// UTC: the day an operator's command says it back as, whatever zone the
// operator's machine is in.
func instant(v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	if t, err := time.Parse(time.DateOnly, v); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, v)
}
