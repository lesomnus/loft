package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lesomnus/payday/trail"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"

	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/server/pd"
)

// NewCmdTrail is `rove trail`: the trail's archive, checked against the account
// the database keeps of it (payday's `trail.Policy.Verify`).
//
// `verify` is what every pass of the trail does lightly at its start -- the
// archive's chunks by name and labels against the manifest, and the latest
// checkpoint against the rows it was taken over -- and `--full` also reads
// every chunk whole and checks its bytes. It changes nothing, and fails when it
// found something, so a schedule can run it. `accept` is an operator who has
// looked at what it found and decided the archive is right -- a pass a crash
// stopped, a chunk restored from a backup -- and says why.
func NewCmdTrail(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "trail",
		Brief: "the trail's archive, checked against the account the database keeps of it",

		Commands: xli.Commands{
			{
				Name:  "verify",
				Brief: "compare the archive with its manifest and its checkpoints, changing nothing",
				Flags: flg.Flags{
					&flg.Switch{Name: "full", Brief: "also read every chunk whole and check its bytes"},
				},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					full, _ := flg.Find[bool](self, "full")
					return verify(ctx, self, c, full)
				}),
			},
			{
				Name:  "accept",
				Brief: "make the manifest say what the archive holds, once somebody has looked",
				Flags: flg.Flags{
					&flg.String{Name: "why", Brief: "what was looked at and decided; it is logged"},
				},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					why, _ := flg.Find[string](self, "why")
					return accept(ctx, self, c, why)
				}),
			},
		},
	}
}

// archived is the trail's policy as `serve` builds it, refused when it names no
// archive to check.
func archived(ctx context.Context, c *cmd.Config) (*cmd.Server, trail.Policy, error) {
	s, err := cmd.Build(ctx, *c)
	if err != nil {
		return nil, trail.Policy{}, err
	}
	if s.Trail.Archive == nil {
		s.Close()
		return nil, trail.Policy{}, errors.New("audit.archive names no archive: the trail is all in the database, and there is nothing to verify")
	}

	return s, s.Trail, nil
}

func verify(ctx context.Context, self *xli.Command, c *cmd.Config, full bool) error {
	s, p, err := archived(ctx, c)
	if err != nil {
		return err
	}
	defer s.Close()

	v, err := p.Verify(ctx, pd.TrailStore(s.Ent), full)
	if err != nil {
		return err
	}

	read := fmt.Sprintf("%d rows in the manifest, %d blobs in the archive", v.Rows, v.Blobs)
	if full {
		read += fmt.Sprintf(", %d read whole", v.Hashed)
	}
	if v.Checkpoint > 0 {
		read += fmt.Sprintf("; checkpoint %d compared", v.Checkpoint)
	}
	self.Printf("%s\n", read)

	if v.Ok() {
		self.Printf("the archive is what the database says it is\n")
		return nil
	}
	for _, f := range v.Findings {
		self.Printf("  %s\n", f.String())
	}

	return fmt.Errorf("%d findings: look at each, and `rove trail accept --why ...` once the archive is right", len(v.Findings))
}

func accept(ctx context.Context, self *xli.Command, c *cmd.Config, why string) error {
	if strings.TrimSpace(why) == "" {
		return errors.New("--why says what was looked at and decided")
	}

	s, p, err := archived(ctx, c)
	if err != nil {
		return err
	}
	defer s.Close()

	v, err := p.Accept(ctx, pd.TrailStore(s.Ent), why)
	for _, f := range v.Findings {
		self.Printf("  %s\n", f.String())
	}
	if err != nil {
		return err
	}

	self.Printf("accepted %d findings; the manifest says what the archive holds\n", len(v.Findings))
	return nil
}
