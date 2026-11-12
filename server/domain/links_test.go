package domain_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/cmd"
	"github.com/lesomnus/loft/internal/ent/holder"
	"github.com/lesomnus/loft/server/pd"
)

// TestALinkIsAtTheServicesAddress: every link made here begins with this
// deployment's own address (design 9.9, D82) -- a label printed and a file's
// signed address where it answers (`app.public_url`), and a page -- the link
// a notification's mail carries, an invitation's -- where its page is
// (`app.app_url`).
func TestALinkIsAtTheServicesAddress(t *testing.T) {
	e := newEnvWith(t, func(c *cmd.Config) {
		c.App.PublicUrl = "https://loft.example"
		c.App.AppUrl = "https://app.loft.example"
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
	mailed := func() string {
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
		x.Len(got, 1)
		link, _, _ := strings.Cut(got[0].Body, "\n")
		return link
	}
	invitation := func() string {
		const to = "guest@example.com"
		_, err := e.app().Invitation().Add(e.owner, app.InvitationAddRequest_builder{Email: to, Role: "member"}.Build())
		x.NoError(err)
		got := box.to(to)
		x.Len(got, 1)
		_, rest, ok := strings.Cut(got[0].Body, "https://")
		x.True(ok, got[0].Body)
		link, _, _ := strings.Cut(rest, "?")
		return "https://" + link
	}

	x.True(strings.HasPrefix(label(), "https://loft.example/l/"), "a label")
	x.True(strings.HasPrefix(signed(), "https://loft.example/files/"), "a file")
	x.Equal("https://app.loft.example/@acme/assets", mailed(), "a notification's mail")
	x.Equal("https://app.loft.example/invitations", invitation(), "an invitation's")
}
