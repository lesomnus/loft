package apphost

import (
	"context"
	"net"
	"net/url"
	"strings"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/tenantdomain"
)

// Links is where every link this deployment makes begins (rove#155, design
// 9.9), and the one place that is decided: nothing else puts a host in an
// address.
//
// A link to a workspace -- a page a mail opens, a label it prints, a file it
// serves -- begins with its active `app` domain, where the whole of its app
// is. A workspace with none, or with a domain that is not in use -- one not
// yet activated, one legacy since another took its place, one retired -- is
// linked to at the service's address, as it always was; and so is everything
// that is no one workspace's: an invitation, which an account answers in its
// inbox there, signing in, a terminal. A `label` domain is a workspace's for
// its labels alone, which print with it while it is active.
type Links struct {
	// App is where the service's page is -- `app.app_url`, or
	// `app.public_url` -- and Public where the service answers: a page's
	// link begins with the first, and a label's and a file's with the
	// second. A workspace's domain is reached as Public is: by its scheme,
	// on its port.
	App    string
	Public string
}

// Kind is what a link is to.
type Kind int

const (
	// Page is a page of a workspace's app.
	Page Kind = iota
	// File is a file a workspace serves, by a signed address.
	File
	// Label is a label a workspace prints.
	Label
)

// Of is where a link of `kind` to workspace `ws` begins -- a scheme and a
// host, with no slash after -- read through `db`, which may be the
// transaction the link is made in.
func (l Links) Of(ctx context.Context, db *ent.Client, ws pdid.Id, kind Kind) (string, error) {
	d, err := db.TenantDomain.Query().Where(
		tenantdomain.TenantId(ws.Uuid()),
		tenantdomain.State("active"),
		tenantdomain.DateErasedIsNil(),
	).First(ctx)
	switch {
	case ent.IsNotFound(err):
		return l.Home(kind), nil
	case err != nil:
		return "", err
	}
	if d.Purpose == "app" || (d.Purpose == "label" && kind == Label) {
		return l.On(d.Host), nil
	}
	return l.Home(kind), nil
}

// On is where a link on `host`, a workspace's domain, begins.
func (l Links) On(host string) string {
	scheme, port := "https", ""
	if u, err := url.Parse(l.Public); err == nil && u.Host != "" {
		scheme, port = u.Scheme, u.Port()
	}
	if port != "" && !(scheme == "https" && port == "443") && !(scheme == "http" && port == "80") {
		host = net.JoinHostPort(host, port)
	}
	return scheme + "://" + host
}

// Secure says whether a browser reaches this deployment over TLS, as a
// cookie of one host's alone (`__Host-`) needs it to.
func (l Links) Secure() bool {
	u, err := url.Parse(l.Public)
	return err == nil && u.Scheme == "https"
}

// Home is where a link of `kind` at the service's address begins: to a page,
// where the service's page is; to a label or a file, where it answers.
func (l Links) Home(kind Kind) string {
	if kind == Page && l.App != "" {
		return strings.TrimSuffix(l.App, "/")
	}
	return strings.TrimSuffix(l.Public, "/")
}
