package cli

import (
	"context"
	"strings"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"

	app "github.com/lesomnus/loft"
)

// roleWritten is `loft role`, the roles Loft ships as the tenant at roster has
// them (D89, D90): the operator's.
func roleWritten() []written {
	return group(newCmdRole(), map[string][]string{
		"widen": {app.OperatorRoleService_Widen_FullMethodName},
	})
}

func newCmdRole() *xli.Command {
	return &xli.Command{
		Name:  "role",
		Brief: "the roles Loft ships, as the tenant at roster has them",
		Commands: xli.Commands{
			{
				Name:  "widen",
				Brief: "give the workspace's roles at roster what each release since theirs added, as serve does when it starts",
				Flags: flg.Flags{
					&flg.Uint32{Name: "from", Brief: "give what the releases after this one added, rather than after the one recorded: for roles the tenant had before anything here recorded them"},
					yesFlag(),
				},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					conn, err := operatorFor(ctx, self)
					if err != nil {
						return err
					}
					from, _ := flg.Find[uint32](self, "from")
					v, err := app.NewOperatorRoleServiceClient(conn).Widen(ctx, app.OperatorRoleWidenRequest_builder{
						DryRun: !yes(self),
						From:   from,
					}.Build())
					if err != nil {
						return err
					}
					if v.GetUnrecorded() {
						self.Printf("nothing here says which release the roles had; taken as release %d, the last, and given nothing\n", v.GetTo())
						self.Printf("(for roles written by an earlier release, `--from` names theirs)\n")
					} else {
						self.Printf("the roles are at release %d of %d\n", v.GetFrom(), v.GetTo())
					}
					for _, r := range v.GetRoles() {
						switch {
						case r.GetMissing():
							self.Printf("%s: the tenant has no role by this alias at roster, so it was given nothing\n", r.GetRole())
						case v.GetDryRun():
							self.Printf("%s: would be given %s\n", r.GetRole(), strings.Join(r.GetMethods(), ", "))
						default:
							self.Printf("%s: given %s\n", r.GetRole(), strings.Join(r.GetMethods(), ", "))
						}
					}
					switch {
					case len(v.GetRoles()) == 0 && v.GetFrom() == v.GetTo():
						self.Printf("nothing to give\n")
					case v.GetDryRun():
						nothing(self)
					default:
						self.Printf("the roles are at release %d now\n", v.GetTo())
					}
					return next(ctx)
				}),
			},
		},
		Handler: xli.RequireSubcommand(),
	}
}
