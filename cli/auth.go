package cli

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/arg"
	"github.com/lesomnus/xli/flg"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/server/clilogin"
)

// NewCmdAuth is `loft auth`: the connections this terminal keeps, a person's
// or an operator's, and who each is (design 9.16, D75).
func NewCmdAuth() *xli.Command {
	return &xli.Command{
		Name:  "auth",
		Brief: "the connections this terminal keeps -- a person's, or an operator's -- and who each is",
		Commands: xli.Commands{
			newCmdAuthLogin(),
			newCmdAuthLogout(),
			newCmdAuthWhoami(),
			newCmdAuthList(),
			newCmdAuthSwitch(),
		},
		Handler: xli.RequireSubcommand(),
	}
}

// newCmdAuthLogin is `loft auth login`: a connection kept, and made the one
// commands use. A person's is this terminal signed in by approving it in a
// browser where they are signed in already (`server/clilogin`); an
// operator's is the entry point and where their key is, which the entry
// point is asked who it is before it is kept.
func newCmdAuthLogin() *xli.Command {
	return &xli.Command{
		Name:  "login",
		Brief: "keep a connection: a person's, approved in a browser, or with --operator the entry point's and where its key is",
		Flags: flg.Flags{
			&flg.String{Name: "url", Brief: "the app's address, e.g. https://loft.example, or with --operator the entry point's; the connection in use's when left out, and for an operator's then http://127.0.0.1:8081"},
			&flg.String{Name: "name", Brief: "what the connection is called; the address's host by default"},
			&flg.Switch{Name: "operator", Brief: "the operator's entry point, opened with --key"},
			&flg.String{Name: "key", Brief: "with --operator, where the key is: env:NAME or file:PATH, never the key"},
			&flg.Switch{Name: "no-browser", Brief: "print the address to open rather than opening it"},
			&flg.Strings{Name: "allow", Brief: "a person's, only this method or pattern (/loft.ReservationService/*, /loft.*/Get), again for more; `read` is every method that only reads"},
		},
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			cs, err := loadConnections()
			if err != nil && !errors.Is(err, errOldFile) {
				return err
			}
			if errors.Is(err, errOldFile) {
				// What a terminal kept before connections is let go of here,
				// and only here: this is how a person signs in again.
				cs = &connections{Connections: map[string]*Connection{}}
			}
			operator, _ := flg.Find[bool](self, "operator")
			base := strings.TrimSuffix(strings.TrimSpace(flag(self, "url")), "/")
			name := flag(self, "name")
			if base == "" {
				// Signing in again: the connection in use, where it is and
				// by its name, when it is of the kind asked for.
				if c, err := cs.choose(choiceOf(ctx), os.Getenv); err == nil && c.Name != "" && (c.Kind == Operator) == operator {
					base = c.Url
					if name == "" {
						name = c.Name
					}
				}
			}
			if operator && base == "" {
				base = defaultEntryPoint
			}
			if base == "" {
				return errors.New("which deployment: --url https://loft.example")
			}
			if _, err := url.Parse(base); err != nil {
				return fmt.Errorf("--url: %w", err)
			}
			if name == "" {
				name = hostOf(base)
			}
			kind := Person
			if operator {
				kind = Operator
			}
			if was, ok := cs.Connections[name]; ok && was.Kind != kind {
				return fmt.Errorf("%q is a connection of another kind (%s): --name another", name, was.Kind)
			}

			var c *Connection
			if operator {
				if allow, _ := flg.Find[[]string](self, "allow"); len(allow) > 0 {
					return errors.New("--allow narrows a person's session; an operator's key holds the entry point's table")
				}
				ref := flag(self, "key")
				if ref == "" {
					return errors.New("--key: where the operator's key is, env:NAME or file:PATH")
				}
				if !isKeyRef(ref) {
					return fmt.Errorf("--key: %w", errLiteralKey)
				}
				c = &Connection{Kind: Operator, Url: base, Key: ref}
				who, err := whoOperator(ctx, c)
				if err != nil {
					return err
				}
				self.Printf("signed in as %s, an operator, at %s\n", who.GetName(), base)
			} else {
				if flag(self, "key") != "" {
					return errors.New("--key opens the operator's entry point: with --operator")
				}
				quiet, _ := flg.Find[bool](self, "no-browser")
				allow, _ := flg.Find[[]string](self, "allow")
				c, err = approve(ctx, self, base, !quiet, expand(allow))
				if err != nil {
					return err
				}
				if was, ok := cs.Connections[name]; ok && was.Kind == Person && was.Url == c.Url {
					c.Workspace = was.Workspace
				}
			}
			cs.Connections[name] = c
			cs.Current = name
			if err := cs.save(); err != nil {
				return err
			}
			if c.Kind == Person {
				var me struct {
					Login string `json:"login"`
				}
				_, err := c.do(ctx, http.MethodGet, "/account", nil, &me)
				mine, merr := app.NewTenantServiceClient(c.person("")).Mine(ctx, &app.TenantMineRequest{})
				switch {
				case err == nil && merr == nil:
					self.Printf("signed in as %s, in %d workspace(s)\n", me.Login, len(mine.GetWorkspaces()))
				case err == nil:
					self.Printf("signed in as %s\n", me.Login)
				default:
					self.Printf("signed in\n")
				}
			}
			return next(ctx)
		}),
	}
}

// whoOperator is who the entry point says the key of `c` is, refused when it
// is nobody's.
func whoOperator(ctx context.Context, c *Connection) (*app.OperatorAuthWhoamiResponse, error) {
	conn, err := c.dial("")
	if err != nil {
		return nil, err
	}
	return app.NewOperatorAuthServiceClient(conn).Whoami(ctx, &app.OperatorAuthWhoamiRequest{})
}

// approve is a person's session, made up here and approved in a browser:
// the terminal sends only the key's hash, and is the session once somebody
// signed in at the app approves it (D33).
func approve(ctx context.Context, self *xli.Command, base string, browser bool, allow []string) (*Connection, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	key := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(key))
	secret := base64.RawURLEncoding.EncodeToString(sum[:])

	host, _ := os.Hostname()
	c := &Connection{Kind: Person, Url: base}
	var started clilogin.Started
	if _, err := c.do(ctx, http.MethodPost, "/cli/logins", map[string]any{
		"secret": secret,
		"client": fmt.Sprintf("loft CLI on %s (%s/%s)", host, runtime.GOOS, runtime.GOARCH),
		"allow":  allow,
	}, &started); err != nil {
		return nil, err
	}
	if len(allow) > 0 {
		self.Printf("Asking to be allowed only:\n  %s\n\n", strings.Join(allow, "\n  "))
	}
	self.Printf("Open this address signed in, and approve it if it shows %s:\n\n  %s\n\n", started.Code, started.Approve)
	if browser {
		openBrowser(started.Approve)
	}

	every := time.Duration(max(started.Interval, 1)) * time.Second
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(every):
		}
		var got struct {
			State string `json:"state"`
		}
		if _, err := c.do(ctx, http.MethodPost, "/cli/logins/"+started.Id+"/poll", map[string]string{"secret": secret}, &got); err != nil {
			return nil, err
		}
		switch got.State {
		case "pending":
			continue
		case "approved":
			c.Cookie, c.Key = started.Cookie, key
			return c, nil
		case "denied":
			return nil, errors.New("turned away in the browser")
		default:
			return nil, errors.New("nobody approved it in time; `loft auth login` again")
		}
	}
}

// newCmdAuthLogout is `loft auth logout`: a connection forgotten -- a
// person's session ended where it lives first. An operator's key is
// `admin.keys`'s, and nothing here ends it.
func newCmdAuthLogout() *xli.Command {
	return &xli.Command{
		Name:  "logout",
		Brief: "forget a connection, ending a person's session at the app first",
		Args:  arg.Args{&arg.String{Name: "CONNECTION", Brief: "by name; the one in use by default", Optional: true}},
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			named, _ := arg.Get[string](self, "CONNECTION")
			cs, err := loadConnections()
			if err != nil {
				return err
			}
			c := chosen{Name: named, Connection: cs.Connections[named]}
			if named == "" {
				if c, err = cs.choose(choiceOf(ctx), os.Getenv); err != nil {
					return err
				}
				if c.Name == "" {
					return errors.New("nothing to forget: this connection is the command line's, or the environment's")
				}
			} else if c.Connection == nil {
				return fmt.Errorf("no connection %q (`loft auth list`)", named)
			}
			if c.signedIn() {
				if _, err := c.do(ctx, http.MethodDelete, "/session", nil, nil); err != nil {
					self.Printf("the deployment did not answer (%v); forgotten here anyway\n", err)
				}
			}
			delete(cs.Connections, c.Name)
			if cs.Current == c.Name {
				cs.Current = ""
			}
			if err := cs.save(); err != nil {
				return err
			}
			self.Printf("signed out of %s\n", c.Name)
			return next(ctx)
		}),
	}
}

// newCmdAuthWhoami is `loft auth whoami`: who the connection in use is -- a
// person's account and the workspaces it is in, or the operator the entry
// point says its key is.
func newCmdAuthWhoami() *xli.Command {
	return &xli.Command{
		Name:  "whoami",
		Brief: "who the connection in use is: a person's account and its workspaces, or the operator its key is",
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			return whoami(ctx, self)
		}),
	}
}

func whoami(ctx context.Context, self *xli.Command) error {
	_, c, err := chooseFor(ctx)
	if err != nil {
		return err
	}
	if c.Kind == Operator {
		who, err := whoOperator(ctx, c.Connection)
		if err != nil {
			return err
		}
		id, _ := pdid.From(who.GetId())
		self.Printf("%s, an operator (%s), at %s\n", who.GetName(), id, c.Url)
		return nil
	}
	if !c.signedIn() {
		return errNoLogin
	}
	var me struct {
		Id    string   `json:"id"`
		Login string   `json:"login"`
		Name  string   `json:"name"`
		Allow []string `json:"allow"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/account", nil, &me); err != nil {
		return err
	}
	self.Printf("%s (%s) at %s\n", me.Login, me.Name, c.Url)
	if len(me.Allow) > 0 {
		self.Printf("  allowed only: %s\n", strings.Join(me.Allow, ", "))
	}
	// What it is in, asked as the account (D73).
	mine, err := app.NewTenantServiceClient(c.person("")).Mine(ctx, &app.TenantMineRequest{})
	if err != nil {
		return err
	}
	for _, w := range mine.GetWorkspaces() {
		mark := " "
		if w.GetAlias() == c.Workspace {
			mark = "*"
		}
		self.Printf("%s @%s\t%s\t%s\n", mark, w.GetAlias(), w.GetRole(), w.GetName())
	}
	return nil
}

// newCmdAuthList is `loft auth list`: the connections the file keeps, the
// one in use marked.
func newCmdAuthList() *xli.Command {
	return &xli.Command{
		Name:  "list",
		Brief: "the connections this terminal keeps, the one in use marked",
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			cs, err := loadConnections()
			if err != nil {
				return err
			}
			for _, name := range cs.names() {
				c := cs.Connections[name]
				mark := " "
				if name == cs.Current {
					mark = "*"
				}
				ws := ""
				if c.Workspace != "" {
					ws = "\t@" + c.Workspace
				}
				self.Printf("%s %s\t%s\t%s%s\n", mark, name, c.Kind, c.Url, ws)
			}
			if len(cs.Connections) == 0 {
				self.Printf("none: `loft auth login`\n")
			}
			return next(ctx)
		}),
	}
}

// newCmdAuthSwitch is `loft auth switch`: which connection commands use
// when they are told none.
func newCmdAuthSwitch() *xli.Command {
	return &xli.Command{
		Name:  "switch",
		Brief: "the connection commands use when they are told none",
		Args:  arg.Args{&arg.String{Name: "CONNECTION", Brief: "by name (`loft auth list`)"}},
		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			named, _ := arg.Get[string](self, "CONNECTION")
			cs, err := loadConnections()
			if err != nil {
				return err
			}
			c, ok := cs.Connections[named]
			if !ok {
				return fmt.Errorf("no connection %q (`loft auth list`)", named)
			}
			cs.Current = named
			if err := cs.save(); err != nil {
				return err
			}
			self.Printf("using %s, %s at %s\n", named, c.Kind, c.Url)
			return next(ctx)
		}),
	}
}
