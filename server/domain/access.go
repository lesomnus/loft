package domain

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
	"uuid"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"google.golang.org/protobuf/proto"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	entaccess "github.com/lesomnus/rove/internal/ent/access"
	"github.com/lesomnus/rove/internal/ent/predicate"
	"github.com/lesomnus/rove/server/pd"
)

// The workspace's access record as its administrators review it (rove#136):
// PIPA's safety measures have a processor look over who read, wrote and took
// out whose data every month, and check what each download was for. Review
// reads a stretch of it out; an AccessReview says it was looked over.

func (s Domain) Access() app.AccessServiceServer { return domainAccess{s, s.Next().Access()} }

type domainAccess struct {
	Domain
	app.AccessServiceServer
}

// maxReviewed is the longest stretch one review reads: a quarter, which is
// three months to look over at once and no more.
const maxReviewed = 92 * 24 * time.Hour

// named is how many of the people a call carried an entry names; the rest it
// counts. A list of people carries hundreds, and the review reads a page of
// fifty calls.
const named = 5

// stretch is the period a review asks of, refused when it is none.
func stretch(from, to time.Time, fromSet, toSet bool) error {
	if !fromSet || !toSet {
		return invalid("from", "점검할 기간의 시작과 끝을 정하세요")
	}
	if !to.After(from) {
		return invalid("to", "기간의 끝은 시작보다 뒤여야 합니다")
	}
	if to.Sub(from) > maxReviewed {
		return invalid("to", "한 번에 %d일까지 봅니다", int(maxReviewed/(24*time.Hour)))
	}
	return nil
}

// Review is a stretch of the workspace's access record, newest first: whose
// record it is, which is the calls that carried this workspace's people's
// data, whoever made them.
func (s domainAccess) Review(ctx context.Context, req *app.AccessReviewRequest) (*app.AccessReviewResponse, error) {
	from, to := req.GetFrom().AsTime(), req.GetTo().AsTime()
	if err := stretch(from, to, req.HasFrom(), req.HasTo()); err != nil {
		return nil, err
	}
	switch req.GetKind() {
	case "", "read", "write", "download":
	default:
		return nil, invalid("kind", "종류는 read, write, download 가운데 하나입니다")
	}
	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}

	in := []predicate.Access{
		entaccess.TenantId(t.tenant.Uuid()),
		entaccess.DateCreatedGTE(from),
		entaccess.DateCreatedLT(to),
	}
	out := &app.AccessReviewResponse{}
	counts := []struct {
		set  func(uint32)
		also []predicate.Access
	}{
		{out.SetCalls, nil},
		{out.SetDownloads, []predicate.Access{entaccess.Kind("download")}},
		{out.SetRefused, []predicate.Access{entaccess.ResultNEQ("ok")}},
	}
	for _, c := range counts {
		n, err := t.db.Access.Query().Where(append(append([]predicate.Access{}, in...), c.also...)...).Count(ctx)
		if err != nil {
			return nil, err
		}
		c.set(uint32(n))
	}

	ps := append([]predicate.Access{}, in...)
	if v := req.GetActor(); len(v) > 0 {
		ps = append(ps, entaccess.ActorId(idOf(v).Uuid()))
	}
	if v := req.GetKind(); v != "" {
		ps = append(ps, entaccess.Kind(v))
	}
	// Where the page begins: after the row the one before ended at.
	var cursor []predicate.Access
	if v := req.GetAfter(); v != "" {
		at, id, err := afterOf(v)
		if err != nil {
			return nil, invalid("after", "페이지 표시가 올바르지 않습니다")
		}
		cursor = []predicate.Access{before(at, id)}
	}

	size := pageSize(req.GetSize())
	subject := req.GetSubject()
	names := namer{t: t}
	// A batch of rows at a time until a page of entries is had: a person's
	// calls are among everybody's, and the database is not asked which rows
	// name them (`subjects` is a list).
	for {
		vs, err := t.db.Access.Query().
			Where(append(append([]predicate.Access{}, ps...), cursor...)...).
			Order(ent.Desc(entaccess.FieldDateCreated), ent.Desc(entaccess.FieldId)).
			Limit(size).
			All(ctx)
		if err != nil {
			return nil, err
		}
		for i, v := range vs {
			if len(subject) > 0 && !carried(v, subject) {
				continue
			}
			out.SetItems(append(out.GetItems(), entryOf(&names, v, subject)))
			if len(out.GetItems()) < size {
				continue
			}
			// A page, and another after it if any row is left.
			if i < len(vs)-1 || len(vs) == size {
				out.SetNext(afterFor(v))
			}
			return out, nil
		}
		if len(vs) < size {
			return out, nil
		}
		last := vs[len(vs)-1]
		cursor = []predicate.Access{before(last.DateCreated, last.Id)}
	}
}

// before is the rows a list newest first reads after the row at `at`, `id`.
func before(at time.Time, id uuid.UUID) predicate.Access {
	return entaccess.Or(
		entaccess.DateCreatedLT(at),
		entaccess.And(entaccess.DateCreatedEQ(at), entaccess.IdLT(id)),
	)
}

// carried is whether a call carried the data of the person `id` names.
func carried(v *ent.Access, id []byte) bool {
	for _, s := range v.Subjects {
		if bytes.Equal(s, id) {
			return true
		}
	}
	return false
}

// entryOf is a row read out: what its value holds, and who it names by the
// names they have now -- `first` first, when it is the one asked after.
func entryOf(n *namer, v *ent.Access, first []byte) *app.AccessEntry {
	e := app.AccessEntry_builder{
		Id:           pdid.Id(v.Id).Bytes(),
		At:           ts(v.DateCreated),
		Action:       v.Action,
		Kind:         v.Kind,
		Result:       v.Result,
		SubjectCount: v.SubjectCount,
	}.Build()
	if v.ActorId != (uuid.UUID{}) {
		e.SetActor(pdid.Id(v.ActorId).Bytes())
		e.SetActorName(n.actor(&v.ActorId))
	} else {
		e.SetActorName(n.actor(nil))
	}
	d := &pdpb.AccessDetail{}
	if err := proto.Unmarshal(v.Value, d); err == nil {
		e.SetAddress(d.GetAddress())
		e.SetReason(d.GetReason())
	}

	subjects := make([][]byte, 0, named)
	if len(first) > 0 {
		subjects = append(subjects, first)
	}
	for _, s := range v.Subjects {
		if len(subjects) == named {
			break
		}
		if !bytes.Equal(s, first) {
			subjects = append(subjects, s)
		}
	}
	for _, s := range subjects {
		id, err := pdid.From(s)
		if err != nil {
			continue
		}
		who := app.AccessSubject_builder{Id: s, Kind: kindOf(id.Domain())}.Build()
		switch id.Domain() {
		case pd.PartyDomain:
			who.SetName(n.party(id.Uuid()))
		case pd.HolderDomain:
			u := id.Uuid()
			who.SetName(n.actor(&u))
		}
		e.SetSubjects(append(e.GetSubjects(), who))
	}
	return e
}

// subjectKinds are the kinds whose rows hold somebody's data (`personal`), by
// the names their schema gives them: the registry's own names are not one
// per number where the trail's kinds share theirs.
var subjectKinds = map[pdid.Domain]string{
	pd.PartyDomain:        "party",
	pd.HolderDomain:       "holder",
	pd.InvitationDomain:   "invitation",
	pd.NotificationDomain: "notification",
}

func kindOf(d pdid.Domain) string {
	if v, ok := subjectKinds[d]; ok {
		return v
	}
	return d.String()
}

// afterFor is where a page that ends at `v` ends, for the next to begin
// after: its time and its identifier, which together are one row.
func afterFor(v *ent.Access) string {
	return base64.RawURLEncoding.EncodeToString(fmt.Appendf(nil, "%d~%s", v.DateCreated.UnixNano(), v.Id))
}

func afterOf(v string) (time.Time, uuid.UUID, error) {
	b, err := base64.RawURLEncoding.DecodeString(v)
	if err != nil {
		return time.Time{}, uuid.UUID{}, err
	}
	at, id, ok := strings.Cut(string(b), "~")
	if !ok {
		return time.Time{}, uuid.UUID{}, fmt.Errorf("no row")
	}
	ns, err := strconv.ParseInt(at, 10, 64)
	if err != nil {
		return time.Time{}, uuid.UUID{}, err
	}
	u, err := uuid.Parse(id)
	if err != nil {
		return time.Time{}, uuid.UUID{}, err
	}
	return time.Unix(0, ns), u, nil
}

func (s Domain) AccessReview() app.AccessReviewServiceServer {
	return domainAccessReview{s, s.Next().AccessReview()}
}

type domainAccessReview struct {
	Domain
	app.AccessReviewServiceServer
}

// Add records that the caller reviewed a stretch of the workspace's access
// record, with what the stretch held then. Who reviewed it is the caller, and
// what it held is counted here; a review says no more of itself than what
// they found.
func (s domainAccessReview) Add(ctx context.Context, req *app.AccessReviewAddRequest) (*app.AccessReview, error) {
	from, to := req.GetPeriodFrom().AsTime(), req.GetPeriodTo().AsTime()
	if err := stretch(from, to, req.HasPeriodFrom(), req.HasPeriodTo()); err != nil {
		return nil, err
	}

	var out *app.AccessReview
	err := s.tx(ctx, func(t *Tx) error {
		if !from.Before(t.now) {
			return invalid("period_from", "아직 오지 않은 기간은 점검하지 못합니다")
		}
		in := []predicate.Access{
			entaccess.TenantId(t.tenant.Uuid()),
			entaccess.DateCreatedGTE(from),
			entaccess.DateCreatedLT(to),
		}
		count := func(also ...predicate.Access) (uint32, error) {
			n, err := t.db.Access.Query().Where(append(append([]predicate.Access{}, in...), also...)...).Count(t.ctx)
			return uint32(n), err
		}
		calls, err := count()
		if err != nil {
			return err
		}
		downloads, err := count(entaccess.Kind("download"))
		if err != nil {
			return err
		}
		refused, err := count(entaccess.ResultNEQ("ok"))
		if err != nil {
			return err
		}

		desc := strings.TrimSpace(req.GetDesc())
		period := fmt.Sprintf("%s – %s", from.In(t.zone()).Format("2006-01-02"), to.Add(-time.Nanosecond).In(t.zone()).Format("2006-01-02"))
		if err := t.begin(nil, "access.review", pdid.Nil, t.now,
			fmt.Sprintf("접속기록 점검: %s, 호출 %d건 (내려받기 %d건, 거부 %d건)", period, calls, downloads, refused), desc,
			map[string]string{"from": from.UTC().Format(time.RFC3339), "to": to.UTC().Format(time.RFC3339)},
		); err != nil {
			return err
		}
		out, err = t.next.AccessReview().Add(t.ctx, app.AccessReviewAddRequest_builder{
			Tenant:     t.tenantRef(),
			Desc:       desc,
			PeriodFrom: ts(from),
			PeriodTo:   ts(to),
			Reviewer:   app.HolderRef_builder{Id: t.actor.Bytes()}.Build(),
			Calls:      calls,
			Downloads:  downloads,
			Refused:    refused,
		}.Build())
		return err
	})
	return out, err
}
