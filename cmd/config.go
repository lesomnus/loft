// Package cmd is this app's own wiring, and it is short on purpose.
//
// Everything that does not change from one app to the next is in payday. What
// is left is here, and it is deliberately **not** hidden behind a
// `payday.Serve(cfg)`: the stack, the order of the interceptors and which
// server the wall is on are the decisions a reader of an app most needs to be
// able to see, and a framework that hid them would be hiding the only part
// worth reading.
package cmd

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/lesomnus/payday/config"

	"github.com/lesomnus/rove/internal/identity"
	"github.com/lesomnus/rove/server/consent"
	"github.com/lesomnus/rove/server/mail"
	"github.com/lesomnus/rove/server/retention"
	"github.com/lesomnus/rove/server/sso"
)

// Name is what this app is called, and it is the only place it is written.
// The environment prefix and the names of the configuration files are derived
// from it -- APPTEST_DB_DSN, apptest.yaml -- by the loader `cli` makes with it,
// so there is nothing to keep in step.
const Name = "rove"

// Config is what this app is configured with.
//
// The framework cannot own this struct, since what an app is configured with is
// the app's. What it owns is the pieces: each of these is a payday type, and
// what is written here is only which of them this app has.
type Config struct {
	Server config.ServerConfig `yaml:"server"`
	Db     config.DbConfig     `yaml:"db"`
	Otel   config.OtelConfig   `yaml:"otel"`
	Watch  config.WatchConfig  `yaml:"watch"`

	// How long the trail keeps a row, per kind of thing. Every write this app
	// makes is recorded, and nothing else applies a window, so a deployment
	// that names none keeps all of it forever -- which is this field left
	// empty, and is the only honest default: a version upgrade is not the right
	// thing to decide how long somebody's evidence lasts.
	//
	// What the values are is what this app is regulated as, which payday cannot
	// know. `trail.Profiles` carries the sentence each number comes from.
	Audit config.AuditConfig `yaml:"audit"`

	// Auth is who people are (design 9.10).
	Auth AuthConfig `yaml:"auth"`

	// Spin is which process of the deployment runs the loops only one of them
	// may -- the sweeps, the mail, the outbox -- named rather than defaulted
	// for the reason the watch broker is (payday#54).
	Spin config.SpinConfig `yaml:"spin"`

	// App is what rove is configured with beyond payday's pieces.
	App AppConfig `yaml:"app"`

	// Admin is the operator's entry point, which `rove-admin serve` serves and
	// `rove serve` does not (design 6장, decision 17).
	Admin AdminConfig `yaml:"admin"`
}

// AdminConfig is the operator's entry point: a listener of its own that only
// the operator's network reaches.
type AdminConfig struct {
	// Addr is where it listens: the loopback by default, and an address on
	// the operators' network at most -- never one the public reaches.
	Addr string `yaml:"addr"`
	// Keys are the operators, by name, each with a key as `env:NAME`,
	// `file:PATH` or the key itself: a request carries one as a bearer token,
	// and what it did is logged with the name. None is a door nobody opens.
	Keys map[string]string `yaml:"keys"`
}

// ListenAddr is where the operator's entry point listens.
func (c AdminConfig) ListenAddr() string {
	if c.Addr == "" {
		return "127.0.0.1:8081"
	}
	return c.Addr
}

// AuthConfig is how people are known: roster, beside this process or inside
// it (design 9.10, D26).
type AuthConfig struct {
	// Roster is an external roster's address and the key this deployment acts
	// with there -- or nothing, which runs roster in this process on a
	// database of its own: `roster.db` beside this one's SQLite file, or
	// `auth.roster.db`.
	Roster identity.Config `yaml:"roster"`

	// Sso is the issuer people sign in through -- roster's, in front of the
	// directory -- and the buttons the sign-in page draws for it (design
	// 9.10). Empty is no single sign-on.
	Sso sso.Config `yaml:"sso"`

	// SsoOnly turns signing in with a password off, which is what a
	// deployment whose people all arrive through Sso says (D28).
	SsoOnly bool `yaml:"sso_only"`
}

// StateDir is the directory this deployment's SQLite file is in, which is
// where the roster in this process keeps its own; empty for a database that is
// not a file.
func (c Config) StateDir() string {
	if c.Db.Driver != "sqlite3" {
		return ""
	}
	v, _ := strings.CutPrefix(c.Db.Dsn, "file:")
	v, q, _ := strings.Cut(v, "?")
	if v == "" || strings.HasPrefix(v, ":memory:") || strings.Contains(q, "mode=memory") {
		return ""
	}

	return filepath.Dir(v)
}

// Replicas refuses what a deployment of several processes cannot be run with.
//
// A deployment says it is several processes by what it names: a lease other
// than `local` hands its loops to whichever process holds them, and a broker
// other than `memory` or `none` carries a write from one process to the
// others. Said either way, what each process makes up for itself stops being
// true of the rest, and the thing here a process makes up is the key the
// download links are signed with. Left empty it is a key per process, so a link
// one replica signed is refused by the next one the browser reaches -- now and
// then, by whichever way the balancer went, with nothing in a log about why.
//
// And where it is: a process told no `app.public_url` answers as if it were
// http://localhost:8080 -- its session cookie without `Secure`, its download
// links and its labels' codes on an address nobody opens -- which is right for
// a checkout and is a deployment nobody noticed was misconfigured (rove#40).
func (c Config) Replicas() error {
	var said []string
	if c.Spin.Lease != config.LeaseLocal {
		said = append(said, fmt.Sprintf("spin.lease: %s", c.Spin.Lease))
	}
	if b := c.Watch.Broker; b != config.BrokerMemory && b != config.BrokerNone {
		said = append(said, fmt.Sprintf("watch.broker: %s", b))
	}
	if len(said) == 0 {
		return nil
	}
	if c.App.SigningKey == "" {
		return fmt.Errorf("app.signing_key: %s says this deployment is several processes, and with no key "+
			"each makes one of its own -- a download link one signed is refused by the next. "+
			"Set the same key on every one (ROVE_APP_SIGNING_KEY)", strings.Join(said, " and "))
	}
	if c.App.PublicUrl == "" {
		return fmt.Errorf("app.public_url: %s says this is a deployment, and with no address it answers as "+
			"http://localhost:8080 -- its session cookie without Secure, its links and labels on an address "+
			"nobody opens. Set the address people open (ROVE_APP_PUBLIC_URL)", strings.Join(said, " and "))
	}

	return nil
}

// AppConfig is rove's own part of the configuration.
type AppConfig struct {
	// PublicUrl is where a browser reaches this server's HTTP listener, such
	// as http://localhost:8080. Download links are made on it, and its scheme
	// says whether the session cookie needs HTTPS.
	PublicUrl string `yaml:"public_url"`
	// TrustedProxies is the proxies in front of this deployment whose
	// X-Forwarded-For is believed, each a network (`10.0.0.0/8`) or one
	// address (`server/clientaddr`). Empty believes nobody's, which is right
	// with nothing in front. Behind a proxy, it leaves every request coming
	// from the proxy, so a sign-in limit is one limit for everybody behind it.
	TrustedProxies []string `yaml:"trusted_proxies"`
	// AppUrl is where the UI is: PublicUrl when this server serves the built
	// UI, and the dev server's address during `npm run dev`. A scanned label
	// is sent there.
	AppUrl string `yaml:"app_url"`
	// Web is a directory holding the built UI, served at `/`. Empty serves
	// none, which is right while the UI runs on its own dev server.
	Web string `yaml:"web"`
	// Csp is how a page is held to its Content Security Policy
	// (`server/csp`): `report-only`, the default, has browsers report what a
	// page does that the policy does not allow to `/csp-report`, which logs
	// it; `enforce` stops it as well; `off` says neither. Framing is refused
	// whatever this says.
	Csp string `yaml:"csp"`

	// Files is the directory attachments are kept in.
	Files string `yaml:"files"`
	// SigningKey signs the short-lived download links. Empty makes one per
	// process, which means a link stops working when the process restarts --
	// and, in a deployment of several, on every process but the one that
	// signed it, which `serve` refuses (see [Config.Replicas]).
	SigningKey string `yaml:"signing_key"`

	Labels  LabelConfig   `yaml:"labels"`
	Session SessionConfig `yaml:"session"`

	// NoShowAfter is how long after a room reservation begins it is released
	// when nobody checked in. Zero holds nobody to checking in.
	NoShowAfter time.Duration `yaml:"no_show_after"`
	// Sweep is the wait between passes of the background work.
	Sweep time.Duration `yaml:"sweep"`

	// Retention is the deployment's half of design 8: what a tenant with no
	// contract keeps, and whether a window destroys anything at all.
	Retention RetentionConfig `yaml:"retention"`

	// Workspaces is what an account may make (design 9.11).
	Workspaces WorkspacesConfig `yaml:"workspaces"`

	// Mail is the SMTP server what goes to a mailbox is sent through -- an
	// invitation. With no host, nothing is sent and each message is logged.
	Mail mail.Config `yaml:"mail"`

	// Invitations is how long one waits and how many a workspace may have
	// out (design 9.11).
	Invitations InvitationsConfig `yaml:"invitations"`

	// Accounts is what becomes of the memberships of an account roster erased
	// (design 9.10).
	Accounts AccountsConfig `yaml:"accounts"`

	// Consent is what an account agrees to before it uses this deployment
	// (design 9.11).
	Consent ConsentConfig `yaml:"consent"`
}

// ConsentConfig is what an account agrees to before it uses this deployment:
// its terms of use and its privacy policy, each at the version in force and
// read where it is published. A document with no version is not asked for,
// and asking for none is the default -- a team running its own Rove has no
// terms to put in front of itself.
//
// A new version is agreed to again: every account is asked at its next page,
// and refused every call until it has.
type ConsentConfig struct {
	Terms   DocumentConfig `yaml:"terms"`
	Privacy DocumentConfig `yaml:"privacy"`

	// Keep is how long an account's agreements are kept after it left this
	// deployment, to show afterwards that they were made; past it they are
	// destroyed. Unset keeps them for good, which is the default until the
	// period is decided with legal review (rove#35).
	Keep time.Duration `yaml:"keep"`
}

// DocumentConfig is one document an account agrees to.
type DocumentConfig struct {
	// Version is the one in force: a date it took effect, say. An agreement
	// records it.
	Version string `yaml:"version"`
	// Url is where it is read.
	Url string `yaml:"url"`
}

// Documents is what an account is asked to agree to, or an error for one
// that has a version and nowhere to be read.
func (c ConsentConfig) Documents() ([]consent.Document, error) {
	out := []consent.Document{}
	for _, d := range []struct {
		key, name, title string
		v                DocumentConfig
	}{
		{"terms", "terms", "이용약관", c.Terms},
		{"privacy", "privacy", "개인정보 처리방침", c.Privacy},
	} {
		v := strings.TrimSpace(d.v.Version)
		if v == "" {
			continue
		}
		u, err := url.Parse(strings.TrimSpace(d.v.Url))
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return nil, fmt.Errorf("app.consent.%s: a version with no url to read it at is nothing anybody can agree to", d.key)
		}
		out = append(out, consent.Document{Name: d.name, Title: d.title, Version: v, Url: u.String()})
	}
	return out, nil
}

// AccountsConfig is how long the memberships of an account roster erased
// wait before the account leaves every workspace it was in.
type AccountsConfig struct {
	// ForgetAfter is how long after roster erased an account it leaves here
	// -- its person records pseudonymized, its memberships ended -- if roster
	// has not restored it by then. It is roster's own grace
	// (`holder.forget_after`) and is kept equal to it: shorter, and an account
	// roster restores comes back to nothing; longer, and this keeps who
	// somebody was after roster has forgotten them. Unset is 30 days, which is
	// roster's default.
	ForgetAfter time.Duration `yaml:"forget_after"`
	// Every is how often the memberships still leaving are looked at. Unset
	// is an hour.
	Every time.Duration `yaml:"every"`
	// Reconcile is how often every account with a membership here is asked
	// about at roster, and the ones it has nobody by begin to leave: what
	// `rove identity reconcile --yes` does, done by the deployment, for an
	// erasure the stream did not carry (design 9.10). Unset is a day; zero or
	// negative turns it off.
	Reconcile *time.Duration `yaml:"reconcile"`
}

// DefaultForgetAfter is how long an erased account's memberships wait when
// the configuration says nothing: roster's own default grace.
const DefaultForgetAfter = 30 * 24 * time.Hour

// Grace is how long an erased account's memberships wait.
func (c AccountsConfig) Grace() time.Duration { return or(c.ForgetAfter, DefaultForgetAfter) }

// Interval is how often the memberships still leaving are looked at.
func (c AccountsConfig) Interval() time.Duration { return or(c.Every, time.Hour) }

// ReconcileEvery is how often every account is asked about at roster, and
// zero when it is not.
func (c AccountsConfig) ReconcileEvery() time.Duration {
	if c.Reconcile == nil {
		return 24 * time.Hour
	}
	return max(*c.Reconcile, 0)
}

// InvitationsConfig is how long an invitation waits, and how many a
// workspace may have out: what keeps one from mailing the world.
type InvitationsConfig struct {
	// Ttl is how long one waits for an answer; unset is 30 days.
	Ttl time.Duration `yaml:"ttl"`
	// Pending is how many may wait at once (unset 50), and Daily how many
	// may go out in a day (unset 100). Negative is no limit.
	Pending int `yaml:"pending"`
	Daily   int `yaml:"daily"`
}

// WorkspacesConfig is what an account may make.
type WorkspacesConfig struct {
	// Personal says whether an account in no workspace is given one of its
	// own when it signs in, which is what signing up to SaaS is. Unset is yes.
	Personal *bool `yaml:"personal"`
	// Owned is how many workspaces one account may own before another needs
	// paying for. Zero is [DefaultOwned]; a negative number is no limit.
	Owned int `yaml:"owned"`

	// AliasLength is how many characters a new workspace's address is made
	// of: 7 unless said. It grows by itself when that many are crowded.
	AliasLength int `yaml:"alias_length"`
	// Reserved is addresses no workspace may have, beside the ones this app
	// keeps for itself (`tenancy.Reserved`).
	Reserved []string `yaml:"reserved"`

	// RestoreWithin is how long a closed workspace may be restored before it
	// is destroyed, and AliasHold how long its address is kept from anybody
	// else after that, or after it moved (D29). Unset are 30 days and a year.
	RestoreWithin time.Duration `yaml:"restore_within"`
	AliasHold     time.Duration `yaml:"alias_hold"`
	// Every is how often closed workspaces are looked at. Unset is an hour.
	Every time.Duration `yaml:"every"`
}

// Restore is how long a closed workspace may be restored.
func (c WorkspacesConfig) Restore() time.Duration { return or(c.RestoreWithin, 30*24*time.Hour) }

// Hold is how long an address is kept from anybody else.
func (c WorkspacesConfig) Hold() time.Duration { return or(c.AliasHold, 365*24*time.Hour) }

// Length is how many characters a new address is made of.
func (c WorkspacesConfig) Length() int { return or(c.AliasLength, 7) }

// DefaultOwned is how many workspaces an account owns before another needs
// paying for, when the configuration says nothing.
const DefaultOwned = 3

// PersonalOn says whether an account in no workspace is given one.
func (c WorkspacesConfig) PersonalOn() bool { return c.Personal == nil || *c.Personal }

// OwnedMax is how many workspaces one account may own; negative is no limit.
func (c WorkspacesConfig) OwnedMax() int {
	if c.Owned == 0 {
		return DefaultOwned
	}
	return c.Owned
}

// RetentionConfig is the plan a tenant with no contract is on, and the switch
// that lets a keep window destroy.
//
// A tenant's own windows are its contract's (`rove contract`). What is here is
// the deployment's default and its consent.
type RetentionConfig struct {
	// Apply lets a keep window destroy what is past it: rove's own history,
	// and the trail of the writes that made it. Off, which is the default, the
	// windows are still read -- the view window applies, a dry run answers --
	// and nothing is removed. Design 8 makes destruction wait until notice,
	// export, grace and holds are settled, and this is the deployment saying
	// they are.
	Apply bool `yaml:"apply"`

	// View and Keep are what a tenant with no contract gets, e.g. `8760h` for
	// a year. Empty is all of its history shown, and all of it kept.
	View time.Duration `yaml:"view"`
	Keep time.Duration `yaml:"keep"`

	// Every is how often what the keep windows no longer reach is taken out of
	// the history, when Apply lets it be. An hour by default.
	Every time.Duration `yaml:"every"`
}

// Defaults is this as what `server/retention` reads.
func (c RetentionConfig) Defaults() retention.Defaults {
	return retention.Defaults{View: c.View, Keep: c.Keep}
}

// LabelConfig is how printed labels are addressed (design 9.9).
type LabelConfig struct {
	// Suffix is what a tenant's default label host ends with: a tenant that
	// asks for `acme` prints `acme.<suffix>`. Empty offers no default
	// subdomain, and a tenant has labels only with a domain of its own.
	Suffix string `yaml:"suffix"`
	// Target is what a custom label domain's CNAME points at.
	Target string `yaml:"target"`
	// TenantDomains prints a tenant's labels with a domain of its own (D23),
	// which is Dedicated's. Off, which is SaaS and the default, every label
	// prints with this deployment's own address and a code unique in it
	// (D28), and no tenant needs a domain to have labels.
	TenantDomains bool `yaml:"tenant_domains"`
}

// Default is the suffix a workspace is given a default label host under when
// it is made: none where workspaces have no domains of their own.
func (c LabelConfig) Default() string {
	if !c.TenantDomains {
		return ""
	}
	return c.Suffix
}

// SessionConfig is how long a browser stays signed in.
type SessionConfig struct {
	// Idle ends a session nobody used for this long.
	Idle time.Duration `yaml:"idle"`
	// Lifetime ends any session this long after it began.
	Lifetime time.Duration `yaml:"lifetime"`
}

// Public is PublicUrl, parsed, with the default a checkout runs on.
func (c AppConfig) Public() *url.URL {
	u, err := url.Parse(c.PublicUrl)
	if err != nil || u.Host == "" {
		u, _ = url.Parse("http://localhost:8080")
	}
	return u
}

// App is AppUrl, or the public address when the UI is served from it.
func (c AppConfig) App() string {
	if c.AppUrl != "" {
		return c.AppUrl
	}
	return c.Public().String()
}
