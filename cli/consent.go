package cli

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"
)

// NewCmdConsent is `rove consent`: what this deployment asks of the account
// before it is used -- its terms and its privacy policy, at the versions in
// force -- and agreeing to them, having read them (design 9.11). Until it has
// agreed, every call this terminal makes is refused, as every page is.
func NewCmdConsent() *xli.Command {
	return &xli.Command{
		Name:  "consent",
		Brief: "what this account has to agree to before it uses the deployment, and agreeing",
		Flags: flg.Flags{
			&flg.Switch{Name: "agree", Brief: "agree to every document owed, at the version printed, having read it"},
		},
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			c, err := loadClient()
			if err != nil {
				return err
			}
			if !c.signedIn() {
				return errNoLogin
			}
			var v struct {
				Owed []struct {
					Name, Title, Version, Url string
				} `json:"owed"`
				Agreed []struct {
					Document, Version string
					At                time.Time
				} `json:"agreed"`
			}
			if _, err := c.do(ctx, http.MethodGet, "/account/consents", nil, &v); err != nil {
				return err
			}
			agree, _ := flg.Find[bool](self, "agree")
			if len(v.Owed) == 0 {
				for _, a := range v.Agreed {
					self.Printf("agreed\t%s\t%s\t%s\n", a.Document, a.Version, a.At.Local().Format(time.DateTime))
				}
				if len(v.Agreed) == 0 {
					self.Printf("this deployment asks nothing to be agreed to\n")
				}
				return next(ctx)
			}
			for _, d := range v.Owed {
				self.Printf("owed\t%s (%s)\t%s\t%s\n", d.Title, d.Name, d.Version, d.Url)
			}
			if !agree {
				self.Printf("read them, then `rove consent --agree`\n")
				return next(ctx)
			}
			body := map[string]map[string]string{"agree": {}}
			for _, d := range v.Owed {
				body["agree"][d.Name] = d.Version
			}
			if _, err := c.do(ctx, http.MethodPost, "/account/consents", body, nil); err != nil {
				return fmt.Errorf("agreeing: %w", err)
			}
			self.Printf("agreed\n")
			return next(ctx)
		}),
	}
}
