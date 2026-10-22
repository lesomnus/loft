package domain_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/internal/ent/tenantdomain"
	"github.com/lesomnus/rove/server/pd"
)

// TestALinkToAWorkspaceIsOnItsAppDomain: every link made to a workspace
// begins with its active app domain, and with the service's address while it
// has none (rove#155) -- a label printed, a file's signed address, the link a
// notification's mail carries. An invitation's is the service's whatever
// domain the workspace has: the account answers it in its inbox there, as
// itself. A label domain is a workspace's for its labels alone, and a domain
// that is no longer active -- legacy since another took its place, or
// retired -- is no link's, which fall back to the service's: a page to where
// its page is (`app.app_url`), a label and a file to where it answers
// (`app.public_url`).
func TestALinkToAWorkspaceIsOnItsAppDomain(t *testing.T) {
	e := newEnvWith(t, func(c *cmd.Config) {
		c.App.PublicUrl = "https://rove.example"
		c.App.AppUrl = "https://app.rove.example"
	})
	x := e.x
	ctx := context.Background()
	box := e.mailing()
	e.s.Deps.Links = e.cfg.App.Links()

	laptop, err := e.app().Asset().Add(e.owner, app.AssetAddRequest_builder{Name: "노트북"}.Build())
	x.NoError(err)
	file, err := e.app().Attachment().Upload(e.owner, app.AttachmentUploadRequest_builder{
		SubjectId: laptop.GetId(),
		Name:      "보증서.pdf",
		Data:      []byte("warranty"),
	}.Build())
	x.NoError(err)
	owner, err := e.s.Ent.Holder.Query().Where(holder.Alias("owner"), holder.TenantId(e.tenant.Uuid())).Only(ctx)
	x.NoError(err)

	label := func() string {
		v, err := e.app().Label().Print(e.owner, app.LabelPrintRequest_builder{Count: 1}.Build())
		x.NoError(err)
		return v.GetUrls()[0]
	}
	signed := func() string {
		v, err := e.app().Attachment().Url(e.owner, app.AttachmentUrlRequest_builder{Ref: ref[app.AttachmentRef](file.GetId())}.Build())
		x.NoError(err)
		return v.GetUrl()
	}
	n := 0
	mailed := func() string {
		n++
		e.x.NoError(e.s.Ent.Notification.Create().
			SetId(pdid.New(pd.NotificationDomain).Uuid()).
			SetTenantId(e.tenant.Uuid()).
			SetHolderId(owner.Id).
			SetName("반납 기한").
			SetDesc("").
			SetKind("custody.late").
			SetLink("/assets").
			SetMail(true).
			SetMailTries(0).
			SetDateUpdated(time.Now()).
			Exec(ctx))
		e.mail()
		got := box.to("owner@acme.example")
		x.Len(got, n)
		link, _, _ := strings.Cut(got[n-1].Body, "\n")
		return link
	}
	invited := 0
	invitation := func() string {
		invited++
		to := "guest" + string(rune('0'+invited)) + "@example.com"
		_, err := e.app().Invitation().Add(e.owner, app.InvitationAddRequest_builder{Email: to, Role: "member"}.Build())
		x.NoError(err)
		got := box.to(to)
		x.Len(got, 1)
		_, rest, ok := strings.Cut(got[0].Body, "https://")
		x.True(ok, got[0].Body)
		link, _, _ := strings.Cut(rest, "?")
		return "https://" + link
	}
	domain := func(host, purpose, state string) pdid.Id {
		id := pdid.New(pd.TenantDomainDomain)
		x.NoError(e.s.Ent.TenantDomain.Create().
			SetId(id.Uuid()).
			SetTenantId(e.tenant.Uuid()).
			SetHost(host).
			SetPurpose(purpose).
			SetState(state).
			SetToken("rove-test").
			SetSource("custom").
			SetDateUpdated(time.Now()).
			Exec(ctx))
		return id
	}
	moved := func(id pdid.Id, state string) {
		x.NoError(e.s.Ent.TenantDomain.Update().Where(tenantdomain.Id(id.Uuid())).SetState(state).Exec(ctx))
	}
	all := func(want map[string]string) {
		t.Helper()
		x.True(strings.HasPrefix(label(), want["label"]+"/l/"), "a label: %s", want["label"])
		x.True(strings.HasPrefix(signed(), want["file"]+"/files/"), "a file: %s", want["file"])
		x.Equal(want["page"]+"/@acme/assets", mailed(), "a notification's mail")
		x.Equal("https://app.rove.example/invitations", invitation(), "an invitation's, at the service's whatever the workspace has")
	}

	// No domain of its own: the service's.
	all(map[string]string{"label": "https://rove.example", "file": "https://rove.example", "page": "https://app.rove.example"})

	// An active app domain: every link to the workspace is on it.
	own := domain("assets.acme.example", "app", "active")
	all(map[string]string{"label": "https://assets.acme.example", "file": "https://assets.acme.example", "page": "https://assets.acme.example"})

	// That one legacy, and a label domain active instead: labels print with
	// it, and nothing else is on it.
	moved(own, "legacy")
	labels := domain("acme.l.test", "label", "active")
	all(map[string]string{"label": "https://acme.l.test", "file": "https://rove.example", "page": "https://app.rove.example"})

	// And none active at all: the service's again.
	moved(labels, "retired")
	all(map[string]string{"label": "https://rove.example", "file": "https://rove.example", "page": "https://app.rove.example"})

	// One verified and not yet in use is no link's either.
	domain("next.acme.example", "app", "ready")
	all(map[string]string{"label": "https://rove.example", "file": "https://rove.example", "page": "https://app.rove.example"})
}
