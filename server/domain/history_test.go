package domain_test

import (
	"github.com/lesomnus/z"

	app "github.com/lesomnus/loft"
)

// keep gives the workspace a history window of `days` in force, as one said
// long ago and past its grace: written beneath the domain, as an operator's
// shell would, since Configure would hold a shorter one to the grace.
func (e *env) keep(days uint32) {
	_, err := e.s.Base.Tenant().Patch(e.owner, app.TenantPatchRequest_builder{
		Ref:              app.TenantRef_builder{Id: e.tenant.Bytes()}.Build(),
		KeepDays:         z.Ptr(days),
		DateUpdatedForce: z.Ptr(true),
	}.Build())
	e.x.NoError(err)
}

// aYearOfALaptop lives a laptop through most of a year and a half, the clock
// moving with it: in A, then B, broken and mended, and back in A.
func (e *env) aYearOfALaptop() (hq, a, b, laptop *app.Asset) {
	start := e.now
	e.now = start.Add(-400 * day)
	hq = e.space("본사", nil)
	a = e.space("A", hq)
	b = e.space("B", hq)
	laptop = e.item("노트북", "NB-1", a, 0)

	e.now = start.Add(-300 * day)
	_, err := e.app().Asset().Move(e.owner, app.AssetMoveRequest_builder{Ref: assetRef(laptop), To: assetRef(b)}.Build())
	e.x.NoError(err)

	e.now = start.Add(-250 * day)
	_, err = e.app().Asset().SetAttributes(e.owner, app.AssetSetAttributesRequest_builder{Ref: assetRef(laptop), Set: map[string]string{"status": "in_repair"}}.Build())
	e.x.NoError(err)

	e.now = start.Add(-200 * day)
	_, err = e.app().Asset().SetAttributes(e.owner, app.AssetSetAttributesRequest_builder{Ref: assetRef(laptop), Set: map[string]string{"status": "active"}}.Build())
	e.x.NoError(err)

	e.now = start.Add(-100 * day)
	_, err = e.app().Asset().Move(e.owner, app.AssetMoveRequest_builder{Ref: assetRef(laptop), To: assetRef(a)}.Build())
	e.x.NoError(err)

	e.now = start
	return hq, a, b, laptop
}

func (e *env) timeline(a *app.Asset, superseded bool) []*app.TimelineEntry {
	v, err := e.app().Asset().Timeline(e.owner, app.AssetTimelineRequest_builder{Ref: assetRef(a), Superseded: superseded}.Build())
	e.x.NoError(err)
	return v.GetEntries()
}

// documents is a year of things begun and finished, and things begun and not.
type documents struct {
	meeting     *app.Reservation
	back, still *app.Custody
	done, open  *app.WorkOrder
	paper       *app.Stock

	// receipt is a file kept for the loan given back.
	receipt *app.Attachment
}

// aYearOfDocuments makes, four hundred days ago, a meeting called off, two
// loans and two work orders; ten days later one of each is over.
func (e *env) aYearOfDocuments() documents {
	x := e.x
	start := e.now
	e.now = start.Add(-400 * day)

	room := e.space("회의실", nil)
	e.bookable(room, nil)
	store := e.space("창고", nil)
	laptop := e.item("대여용 노트북", "LN-1", store, 0)
	phone := e.item("대여용 휴대폰", "LP-1", store, 0)
	_, kim := e.person("member")

	d := documents{paper: e.stock(e.model("A4 용지"), store, 10, 0)}

	var err error
	d.meeting, err = e.reserve(e.owner, room, e.tomorrow(10, 0), e.tomorrow(11, 0), nil)
	x.NoError(err)
	_, err = e.app().Reservation().Cancel(e.owner, app.ReservationDecideRequest_builder{Ref: ref[app.ReservationRef](d.meeting.GetId())}.Build())
	x.NoError(err)

	lend := func(a *app.Asset) *app.Custody {
		v, err := e.app().Custody().Add(e.owner, app.CustodyAddRequest_builder{
			Party: ref[app.PartyRef](kim.GetId()),
			Lines: []*app.CustodyLineSpec{app.CustodyLineSpec_builder{Asset: assetRef(a)}.Build()},
		}.Build())
		x.NoError(err)
		return v
	}
	d.back, d.still = lend(laptop), lend(phone)
	d.receipt, err = e.app().Attachment().Upload(e.owner, app.AttachmentUploadRequest_builder{
		SubjectId: d.back.GetId(),
		Name:      "인수증.pdf",
		Data:      []byte("signed"),
	}.Build())
	x.NoError(err)

	work := func(a *app.Asset) *app.WorkOrder {
		v, err := e.app().WorkOrder().Add(e.owner, app.WorkOrderAddRequest_builder{Asset: assetRef(a), Name: "점검"}.Build())
		x.NoError(err)
		return v
	}
	d.done, d.open = work(laptop), work(phone)

	e.now = start.Add(-390 * day)
	_, err = e.app().Custody().Return(e.owner, app.CustodyReturnRequest_builder{Ref: ref[app.CustodyRef](d.back.GetId())}.Build())
	x.NoError(err)
	_, err = e.app().WorkOrder().Complete(e.owner, app.WorkOrderCompleteRequest_builder{Ref: ref[app.WorkOrderRef](d.done.GetId())}.Build())
	x.NoError(err)

	e.now = start
	return d
}
