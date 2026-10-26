package cli

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/server/domain"
	"github.com/lesomnus/rove/server/pd"
)

// The tools that change the register (D33, rove#94): a plan, and applying it.
//
// A plan_* tool takes what to do, reads what it names, and answers what would
// happen in the words a person reads -- which asset, from where to where, to
// whom, until when -- with a token; it changes nothing. `apply` takes the
// token and that sentence, and makes the call. The sentence is apply's
// argument so that the prompt a client shows before it runs a tool says what
// this one does, and it has to be the plan's: what is applied is what the
// person read.
//
// What may be done is what the person may do. The policy table and the
// domain decide when apply calls; a terminal signed in narrowed (`rove login
// --allow`) is held to that by the server, and is told so here at the plan.
// The history names the person, and nothing of the assistant.
//
// A plan mints its operation's op, so that apply sent again is the same
// operation to the server, not a second one. A token is good for ten minutes,
// in this process, and is applied once.

// plan is what a plan_* tool made: the method it calls, the sentence the
// person says yes to, and the call itself, on the connection apply makes.
type plan struct {
	method  string
	summary string
	run     func(ctx context.Context, conn grpc.ClientConnInterface) (*mcpRef, error)
	// more is what a run answered beside what it made or changed, which apply
	// passes on: the codes and addresses of labels made.
	more    []string
	expires time.Time
	// running is an apply of it under way, and done one that succeeded: a
	// plan is carried out by one apply at a time, and once.
	running bool
	done    bool
}

// mcpPlans is the plans made and not yet applied.
type mcpPlans struct {
	mu sync.Mutex
	m  map[string]*plan
}

const planTtl = 10 * time.Minute

func (ps *mcpPlans) put(p *plan) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := "plan_" + base64.RawURLEncoding.EncodeToString(b)
	now := time.Now()
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for k, v := range ps.m {
		if now.After(v.expires) {
			delete(ps.m, k)
		}
	}
	p.expires = now.Add(planTtl)
	ps.m[token] = p
	return token, nil
}

// take is the plan a token names, if the sentence is its own, for one apply
// to carry out: [mcpPlans.finish] says how it went.
func (ps *mcpPlans) take(token, summary string) (*plan, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	p, ok := ps.m[token]
	switch {
	case !ok || time.Now().After(p.expires):
		return nil, errors.New("no such plan, or it is more than ten minutes old: plan it again")
	case p.done:
		return nil, errors.New("this plan was applied already")
	case p.running:
		return nil, errors.New("this plan is being applied already")
	case strings.TrimSpace(summary) != p.summary:
		return nil, fmt.Errorf("summary: not this plan's; it is %q", p.summary)
	}
	p.running = true
	return p, nil
}

// finish is an apply over: the plan done, or there to be applied again.
func (ps *mcpPlans) finish(p *plan, ok bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	p.running, p.done = false, ok
}

// op is a new operation's identifier: an Event's, which the domain takes as
// the operation's own (`domain.made`) and knows again when it is sent twice.
func op() []byte { return pdid.New(pd.EventDomain).Bytes() }

type mcpPlanned struct {
	Token   string `json:"token" jsonschema:"what apply takes"`
	Summary string `json:"summary" jsonschema:"what would happen, for the person to read and say yes to; apply takes it word for word"`
	Expires string `json:"expires" jsonschema:"when the plan stops being one"`
}

// allows says whether this terminal's sign-in may call method: what `rove
// login --allow` narrowed it to, as GET /account says. The server holds every
// call to it whatever this says; asked here, a plan the sign-in could not
// carry out is not put before the person.
func (c *mcpCall) allows(method string) (bool, error) {
	if c.allow == nil {
		var v struct {
			Allow []string `json:"allow"`
		}
		if _, err := c.client.do(c.ctx, http.MethodGet, "/account", nil, &v); err != nil {
			return false, err
		}
		c.allow = append([]string{}, v.Allow...)
	}
	if len(c.allow) == 0 {
		return true, nil
	}
	for _, p := range c.allow {
		if frame.Covers(p, method) {
			return true, nil
		}
	}
	return false, nil
}

// planned puts a plan made in a call before the person.
func (t mcpTools) planned(c *mcpCall, p *plan) (*mcp.CallToolResult, mcpPlanned, error) {
	out := mcpPlanned{}
	ok, err := c.allows(p.method)
	if err != nil {
		return nil, out, err
	}
	if !ok {
		return nil, out, fmt.Errorf("this terminal's sign-in may not call %s (rove whoami says what it may); signing in again without that --allow is the person's to do", p.method)
	}
	token, err := t.plans.put(p)
	if err != nil {
		return nil, out, err
	}
	out.Token, out.Summary, out.Expires = token, p.summary, p.expires.Local().Format(time.RFC3339)
	return nil, out, nil
}

type mcpApplyIn struct {
	Token   string `json:"token" jsonschema:"the plan's token"`
	Summary string `json:"summary" jsonschema:"the plan's summary, word for word: what the person said yes to"`
}

type mcpApplied struct {
	Done   string   `json:"done" jsonschema:"what was done"`
	Result *mcpRef  `json:"result,omitempty" jsonschema:"what it made or changed, by id"`
	More   []string `json:"more,omitempty" jsonschema:"what else it answered: the codes and addresses of labels made"`
}

func (t mcpTools) apply(ctx context.Context, _ *mcp.CallToolRequest, in mcpApplyIn) (*mcp.CallToolResult, mcpApplied, error) {
	out := mcpApplied{}
	p, err := t.plans.take(in.Token, in.Summary)
	if err != nil {
		return nil, out, err
	}
	c, err := t.call(ctx)
	if err != nil {
		t.plans.finish(p, false)
		return nil, out, err
	}
	res, err := p.run(ctx, c.conn)
	if err != nil {
		// Not applied, or not known to be: the same token sends the same op
		// again, which the server takes as the same operation.
		t.plans.finish(p, false)
		return nil, out, failed(err)
	}
	t.plans.finish(p, true)
	out.Done, out.Result, out.More = p.summary, res, p.more
	return nil, out, nil
}

// The words a person reads for what the server keeps, as the page has them
// (`ts/src/words.ts`).
var (
	kindWord      = map[string]string{"item": "물품", "space": "공간", "kit": "키트", "group": "그룹"}
	modeWord      = map[string]string{"located": "놓임", "installed": "장착", "part": "구성품"}
	stewardWord   = map[string]string{"owner": "소유자", "manager": "관리 담당자", "custodian": "보유자"}
	custodyWord   = map[string]string{"issue": "지급", "loan": "대여"}
	factWord      = map[string]string{"name": "이름", "desc": "설명", "status": "상태", "condition": "컨디션", "serial": "시리얼", "tag": "자산 번호", "type": "유형", "model": "모델"}
	statusWord    = map[string]string{"ordered": "주문", "active": "사용중", "in_repair": "수리중", "lost": "분실", "retired": "불용", "disposed": "폐기"}
	conditionWord = map[string]string{"good": "양호", "damaged": "손상", "broken": "고장"}
)

func said(m map[string]string, v string) string {
	if w, ok := m[v]; ok {
		return w
	}
	return v
}

// moment is a time an assistant gave, for a request and for the sentence;
// nil and "" when it gave none.
func moment(field, v string) (*timestamppb.Timestamp, string, error) {
	if strings.TrimSpace(v) == "" {
		return nil, "", nil
	}
	t, err := at(field, v)
	if err != nil {
		return nil, "", err
	}
	return timestamppb.New(t), t.Local().Format("2006-01-02 15:04"), nil
}

// asset is an asset an assistant named, read, and how a sentence names it.
func (c *mcpCall) asset(field, v string) (*app.Asset, string, error) {
	ref, err := c.assetRef(v)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", field, err)
	}
	a, err := app.NewAssetServiceClient(c.conn).Get(c.ctx, app.AssetGetRequest_builder{Ref: ref}.Build())
	if err != nil {
		return nil, "", fmt.Errorf("%s %s: %w", field, v, failed(err))
	}
	c.mu.Lock()
	c.assets[pid(a.GetId())] = a
	c.mu.Unlock()
	return a, strings.TrimSpace(a.GetTag() + " " + a.GetName()), nil
}

// place is where an asset is, the outermost first, ending in itself when
// `self`: "본관 › 3층 창고".
func (c *mcpCall) place(a *app.Asset, self bool) string {
	c.prefetch([]*app.Asset{a})
	parts := []string{}
	for _, r := range c.where(a) {
		parts = append(parts, r.Name)
	}
	if self {
		parts = append(parts, a.GetName())
	}
	if len(parts) == 0 {
		return "어디에도 없음"
	}
	return strings.Join(parts, " › ")
}

// party is a party an assistant named by id, read, and how a sentence names
// it: with the team it is in.
func (c *mcpCall) party(field, v string) (*app.Party, string, error) {
	id, err := idOf(field, v)
	if err != nil {
		return nil, "", err
	}
	p, err := app.NewPartyServiceClient(c.conn).Get(c.ctx, app.PartyGetRequest_builder{Ref: app.PartyById(id.Bytes())}.Build())
	if err != nil {
		return nil, "", fmt.Errorf("%s %s: %w", field, v, failed(err))
	}
	name := p.GetName()
	if up := pid(p.GetParentId()); !up.IsZero() {
		c.fetchParties([]pdid.Id{up})
		if n := c.parties[up]; n != "" {
			name += " (" + n + ")"
		}
	}
	return p, name, nil
}

func refOf(id []byte, name string) *mcpRef { return &mcpRef{Id: idString(id), Name: name} }

func clause(format string, v string) string {
	if v == "" {
		return ""
	}
	return fmt.Sprintf(format, v)
}

// The assets.

type mcpAssetPlan struct {
	Action string `json:"action" jsonschema:"add, move, set, assign, relate, correct or erase"`
	Asset  string `json:"asset,omitempty" jsonschema:"the asset, by id or tag: every action but add"`

	Name   string `json:"name,omitempty" jsonschema:"add: what it is called"`
	Kind   string `json:"kind,omitempty" jsonschema:"add: item if left out, space, kit or group"`
	Tag    string `json:"tag,omitempty" jsonschema:"add: the number people read, unique in the workspace"`
	Serial string `json:"serial,omitempty" jsonschema:"add: the maker's serial number"`
	Type   string `json:"type,omitempty" jsonschema:"add: what it is, a type id another answer gave"`
	Model  string `json:"model,omitempty" jsonschema:"add: its model, an id find_models answers with"`

	To   string `json:"to,omitempty" jsonschema:"add, move: what it goes into, by id or tag; move without it takes it out of wherever it is"`
	Mode string `json:"mode,omitempty" jsonschema:"add, move: how it is held there: located if left out, installed or part"`
	Slot string `json:"slot,omitempty" jsonschema:"move: where in what it goes into: a slot, a bay"`

	Set   map[string]string `json:"set,omitempty" jsonschema:"set: name, desc, status, condition, serial, tag, type (an id), model (an id), or an attribute's key, each to a value"`
	Clear []string          `json:"clear,omitempty" jsonschema:"set: attribute keys to clear"`
	Leave bool              `json:"leave_kits,omitempty" jsonschema:"set, with a status of lost, retired or disposed: take it out of every kit and group it is a member of as well, which the summary names, and the kits' bookings let it go; the summary of a plan without it names what it stays a member of"`

	Role  string `json:"role,omitempty" jsonschema:"assign: owner, manager or custodian"`
	Party string `json:"party,omitempty" jsonschema:"assign: who takes the role, an id find_people answers with; left out ends the role"`

	Target   string `json:"target,omitempty" jsonschema:"relate: the other asset, by id or tag"`
	Relation string `json:"relation,omitempty" jsonschema:"relate: member_of (a part of a kit or a group) or connected_to"`
	End      bool   `json:"end,omitempty" jsonschema:"relate: end the relation rather than begin it"`
	Required bool   `json:"required,omitempty" jsonschema:"relate, member_of a kit: a required member, which every booking of the kit takes with it; an optional one, which they leave, if left out. A member already is made what this says, which the summary names"`

	Row       string `json:"row,omitempty" jsonschema:"correct: the history row that is wrong, as asset_history's row"`
	Retract   bool   `json:"retract,omitempty" jsonschema:"correct: it never happened"`
	ValidFrom string `json:"valid_from,omitempty" jsonschema:"correct: it happened, but from this moment"`

	At     string `json:"at,omitempty" jsonschema:"when it happens, or happened: RFC 3339 or a date; now if left out. For add, since when it has been so"`
	Reason string `json:"reason,omitempty" jsonschema:"why, for the history"`
}

func (t mcpTools) planAsset(ctx context.Context, _ *mcp.CallToolRequest, in mcpAssetPlan) (*mcp.CallToolResult, mcpPlanned, error) {
	c, err := t.call(ctx)
	if err != nil {
		return nil, mcpPlanned{}, err
	}
	var p *plan
	switch in.Action {
	case "add":
		p, err = c.assetAdd(in)
	case "move":
		p, err = c.assetMove(in)
	case "set":
		p, err = c.assetSet(in)
	case "assign":
		p, err = c.assetAssign(in)
	case "relate":
		p, err = c.assetRelate(in)
	case "correct":
		p, err = c.assetCorrect(in)
	case "erase":
		p, err = c.assetErase(in)
	default:
		err = errors.New("action: add, move, set, assign, relate, correct or erase")
	}
	if err != nil {
		return nil, mcpPlanned{}, err
	}
	return t.planned(c, p)
}

func (c *mcpCall) assetAdd(in mcpAssetPlan) (*plan, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, errors.New("name: what it is called")
	}
	kind := in.Kind
	if kind == "" {
		kind = "item"
	}
	req := app.AssetAddRequest_builder{Name: name, Kind: kind, Tag: in.Tag, Serial: in.Serial, Op: op()}.Build()
	what := []string{said(kindWord, kind)}
	if in.Type != "" {
		id, err := idOf("type", in.Type)
		if err != nil {
			return nil, err
		}
		v, err := app.NewAssetTypeServiceClient(c.conn).Get(c.ctx, app.AssetTypeGetRequest_builder{Ref: app.AssetTypeById(id.Bytes())}.Build())
		if err != nil {
			return nil, fmt.Errorf("type %s: %w", in.Type, failed(err))
		}
		req.SetType(app.AssetTypeById(id.Bytes()))
		what = append(what, "유형 "+v.GetName())
	}
	if in.Model != "" {
		id, err := idOf("model", in.Model)
		if err != nil {
			return nil, err
		}
		v, err := app.NewItemModelServiceClient(c.conn).Get(c.ctx, app.ItemModelGetRequest_builder{Ref: app.ItemModelById(id.Bytes())}.Build())
		if err != nil {
			return nil, fmt.Errorf("model %s: %w", in.Model, failed(err))
		}
		req.SetModel(app.ItemModelById(id.Bytes()))
		what = append(what, "모델 "+v.GetName())
	}
	where := "어디에도 두지 않음"
	if in.To != "" {
		to, _, err := c.asset("to", in.To)
		if err != nil {
			return nil, err
		}
		req.SetTo(app.AssetById(to.GetId()))
		where = c.place(to, true) + "에 " + said(modeWord, or(in.Mode, "located"))
		if in.Mode != "" {
			req.SetMode(in.Mode)
		}
	}
	since, when, err := moment("at", in.At)
	if err != nil {
		return nil, err
	}
	if since != nil {
		req.SetSince(since)
	}
	if in.Reason != "" {
		req.SetReason(in.Reason)
	}
	label := strings.TrimSpace(in.Tag + " " + name)
	summary := fmt.Sprintf("등록: %s (%s), %s%s%s", label, strings.Join(what, ", "), where, clause(", %s부터", when), clause(" — %s", in.Reason))
	return &plan{
		method:  app.AssetService_Add_FullMethodName,
		summary: summary,
		run: func(ctx context.Context, conn grpc.ClientConnInterface) (*mcpRef, error) {
			v, err := app.NewAssetServiceClient(conn).Add(ctx, req)
			if err != nil {
				return nil, err
			}
			return refOf(v.GetId(), v.GetName()), nil
		},
	}, nil
}

func (c *mcpCall) assetMove(in mcpAssetPlan) (*plan, error) {
	a, label, err := c.asset("asset", in.Asset)
	if err != nil {
		return nil, err
	}
	from := c.place(a, false)
	req := app.AssetMoveRequest_builder{Ref: app.AssetById(a.GetId()), Mode: in.Mode, Slot: in.Slot, Reason: in.Reason, Op: op()}.Build()
	at, when, err := moment("at", in.At)
	if err != nil {
		return nil, err
	}
	if at != nil {
		req.SetAt(at)
	}
	summary := ""
	if in.To == "" {
		summary = fmt.Sprintf("꺼내기: %s, %s에서", label, from)
	} else {
		to, _, err := c.asset("to", in.To)
		if err != nil {
			return nil, err
		}
		req.SetTo(app.AssetById(to.GetId()))
		how := said(modeWord, or(in.Mode, "located")) + clause(", %s", in.Slot)
		summary = fmt.Sprintf("이동: %s, %s → %s (%s)", label, from, c.place(to, true), how)
	}
	summary += clause(", %s에", when) + clause(" — %s", in.Reason)
	return &plan{
		method:  app.AssetService_Move_FullMethodName,
		summary: summary,
		run: func(ctx context.Context, conn grpc.ClientConnInterface) (*mcpRef, error) {
			v, err := app.NewAssetServiceClient(conn).Move(ctx, req)
			if err != nil {
				return nil, err
			}
			return refOf(v.GetId(), v.GetName()), nil
		},
	}, nil
}

func (c *mcpCall) assetSet(in mcpAssetPlan) (*plan, error) {
	a, label, err := c.asset("asset", in.Asset)
	if err != nil {
		return nil, err
	}
	if len(in.Set) == 0 && len(in.Clear) == 0 {
		return nil, errors.New("set: what to change, or clear: what to clear")
	}
	keys := make([]string, 0, len(in.Set))
	for k := range in.Set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	changes := []string{}
	for _, k := range keys {
		v := in.Set[k]
		shown := v
		switch k {
		case "status":
			shown = said(statusWord, v)
		case "condition":
			shown = said(conditionWord, v)
		case "type":
			id, err := idOf("set.type", v)
			if err != nil {
				return nil, err
			}
			ty, err := app.NewAssetTypeServiceClient(c.conn).Get(c.ctx, app.AssetTypeGetRequest_builder{Ref: app.AssetTypeById(id.Bytes())}.Build())
			if err != nil {
				return nil, fmt.Errorf("set.type %s: %w", v, failed(err))
			}
			shown = ty.GetName()
		case "model":
			id, err := idOf("set.model", v)
			if err != nil {
				return nil, err
			}
			m, err := app.NewItemModelServiceClient(c.conn).Get(c.ctx, app.ItemModelGetRequest_builder{Ref: app.ItemModelById(id.Bytes())}.Build())
			if err != nil {
				return nil, fmt.Errorf("set.model %s: %w", v, failed(err))
			}
			shown = m.GetName()
		}
		if v == "" {
			changes = append(changes, said(factWord, k)+" 지움")
		} else {
			changes = append(changes, fmt.Sprintf("%s → %s", said(factWord, k), shown))
		}
	}
	for _, k := range in.Clear {
		changes = append(changes, said(factWord, k)+" 지움")
	}
	req := app.AssetSetAttributesRequest_builder{Ref: app.AssetById(a.GetId()), Set: in.Set, Clear: in.Clear, Reason: in.Reason, Op: op()}.Build()
	at, when, err := moment("at", in.At)
	if err != nil {
		return nil, err
	}
	if at != nil {
		req.SetAt(at)
	}
	stays, err := c.leaving(a, in.Set["status"], in.Leave, func(leave []*app.AssetRef, names string) {
		req.SetLeave(leave)
		changes = append(changes, names+"에서 빼기")
	})
	if err != nil {
		return nil, err
	}
	summary := fmt.Sprintf("속성 변경: %s: %s%s%s%s", label, strings.Join(changes, ", "), clause(", %s부터", when), stays, clause(" — %s", in.Reason))
	return &plan{
		method:  app.AssetService_SetAttributes_FullMethodName,
		summary: summary,
		run: func(ctx context.Context, conn grpc.ClientConnInterface) (*mcpRef, error) {
			v, err := app.NewAssetServiceClient(conn).SetAttributes(ctx, req)
			if err != nil {
				return nil, err
			}
			return refOf(v.GetId(), v.GetName()), nil
		},
	}, nil
}

// leaving is what going out of use does to what `a` is a member of
// (AssetSetAttributesRequest.leave), as a plan says it: with `leave`, it
// leaves each kit and group it is a member of now, named, which `take` puts
// in the request and the sentence; and without, the sentence's clause of what
// it stays a member of -- whose bookings still take it -- so that the person
// is told before they say yes, and the assistant may ask to take it out.
func (c *mcpCall) leaving(a *app.Asset, status string, leave bool, take func(leave []*app.AssetRef, names string)) (string, error) {
	if !slices.Contains(domain.OutOfUse, status) {
		if leave {
			return "", errors.New("leave_kits: only as it goes out of use, with a status of lost, retired or disposed")
		}
		return "", nil
	}
	ms, err := c.memberships(a)
	if err != nil || len(ms) == 0 {
		return "", err
	}
	names := []string{}
	refs := []*app.AssetRef{}
	for _, m := range ms {
		names = append(names, m.name)
		refs = append(refs, app.AssetById(m.id))
	}
	if leave {
		take(refs, strings.Join(names, ", "))
		return "", nil
	}
	return fmt.Sprintf(" (%s의 구성품으로 남음)", strings.Join(names, ", ")), nil
}

// membership is a kit or a group an asset is a member of now, as its history
// says it: the open `member_of` row it is the source of, and whether it is a
// required member, which a kit's bookings take with it.
type membership struct {
	id       []byte
	name     string
	required bool
}

// memberAs is what a kit's member is, as a person reads it.
func memberAs(required bool) string {
	if required {
		return "필수 구성품"
	}
	return "선택 구성품"
}

// memberships answers what `a` is a member of now, newest first.
func (c *mcpCall) memberships(a *app.Asset) ([]membership, error) {
	v, err := app.NewAssetServiceClient(c.conn).Timeline(c.ctx, app.AssetTimelineRequest_builder{Ref: app.AssetById(a.GetId())}.Build())
	if err != nil {
		return nil, failed(err)
	}
	out := []membership{}
	for _, e := range v.GetEntries() {
		d := e.GetDetail()
		if e.GetKind() != "link" || e.HasSupersededAt() || e.HasValidTo() || d["kind"] != "member_of" || d["side"] != "source" {
			continue
		}
		out = append(out, membership{id: e.GetOtherId(), name: e.GetOtherName(), required: d["required"] == "true"})
	}
	return out, nil
}

func (c *mcpCall) assetAssign(in mcpAssetPlan) (*plan, error) {
	a, label, err := c.asset("asset", in.Asset)
	if err != nil {
		return nil, err
	}
	if _, ok := stewardWord[in.Role]; !ok {
		return nil, errors.New("role: owner, manager or custodian")
	}
	req := app.AssetAssignRequest_builder{Ref: app.AssetById(a.GetId()), Role: in.Role, Reason: in.Reason, Op: op()}.Build()
	summary := ""
	if in.Party == "" {
		summary = fmt.Sprintf("%s 해제: %s", said(stewardWord, in.Role), label)
	} else {
		p, name, err := c.party("party", in.Party)
		if err != nil {
			return nil, err
		}
		req.SetParty(app.PartyById(p.GetId()))
		summary = fmt.Sprintf("%s 지정: %s → %s", said(stewardWord, in.Role), label, name)
	}
	at, when, err := moment("at", in.At)
	if err != nil {
		return nil, err
	}
	if at != nil {
		req.SetAt(at)
	}
	summary += clause(", %s부터", when) + clause(" — %s", in.Reason)
	return &plan{
		method:  app.AssetService_Assign_FullMethodName,
		summary: summary,
		run: func(ctx context.Context, conn grpc.ClientConnInterface) (*mcpRef, error) {
			v, err := app.NewAssetServiceClient(conn).Assign(ctx, req)
			if err != nil {
				return nil, err
			}
			return refOf(v.GetId(), v.GetName()), nil
		},
	}, nil
}

func (c *mcpCall) assetRelate(in mcpAssetPlan) (*plan, error) {
	a, label, err := c.asset("asset", in.Asset)
	if err != nil {
		return nil, err
	}
	b, other, err := c.asset("target", in.Target)
	if err != nil {
		return nil, err
	}
	verb := ""
	switch {
	case in.Relation == "member_of" && !in.End:
		verb = "구성에 넣기: %s → %s"
	case in.Relation == "member_of":
		verb = "구성에서 빼기: %s ← %s"
	case in.Relation == "connected_to" && !in.End:
		verb = "연결: %s ↔ %s"
	case in.Relation == "connected_to":
		verb = "연결 끊기: %s ↔ %s"
	default:
		return nil, errors.New("relation: member_of or connected_to")
	}
	// A kit's member is required, and every booking of the kit takes it, or
	// optional, and they leave it: the sentence says which it is made, and
	// that a member already is made the other.
	as := ""
	switch {
	case in.Relation == "member_of" && !in.End && b.GetKind() == "kit":
		ms, err := c.memberships(a)
		if err != nil {
			return nil, err
		}
		as = fmt.Sprintf(" (%s)", memberAs(in.Required))
		for _, m := range ms {
			if pid(m.id) == pid(b.GetId()) && m.required != in.Required {
				verb, as = memberAs(in.Required)+"으로 바꾸기: %s → %s", ""
			}
		}
	case in.Required:
		return nil, errors.New("required: only of a member given to a kit, which its bookings take with it")
	}
	req := app.AssetRelateRequest_builder{Ref: app.AssetById(a.GetId()), Target: app.AssetById(b.GetId()), Kind: in.Relation, Required: in.Required, End: in.End, Reason: in.Reason, Op: op()}.Build()
	at, when, err := moment("at", in.At)
	if err != nil {
		return nil, err
	}
	if at != nil {
		req.SetAt(at)
	}
	summary := fmt.Sprintf(verb, label, other) + as + clause(", %s부터", when) + clause(" — %s", in.Reason)
	return &plan{
		method:  app.AssetService_Relate_FullMethodName,
		summary: summary,
		run: func(ctx context.Context, conn grpc.ClientConnInterface) (*mcpRef, error) {
			v, err := app.NewAssetServiceClient(conn).Relate(ctx, req)
			if err != nil {
				return nil, err
			}
			return refOf(v.GetId(), v.GetName()), nil
		},
	}, nil
}

func (c *mcpCall) assetCorrect(in mcpAssetPlan) (*plan, error) {
	a, label, err := c.asset("asset", in.Asset)
	if err != nil {
		return nil, err
	}
	row, err := idOf("row", in.Row)
	if err != nil {
		return nil, err
	}
	// The row as the history says it, so that the person reads which one.
	v, err := app.NewAssetServiceClient(c.conn).Timeline(c.ctx, app.AssetTimelineRequest_builder{Ref: app.AssetById(a.GetId()), Limit: 500}.Build())
	if err != nil {
		return nil, failed(err)
	}
	what := ""
	for _, e := range v.GetEntries() {
		if pid(e.GetRowId()) == row && e.GetKind() != "event" {
			what = e.GetSummary()
		}
	}
	if what == "" {
		return nil, fmt.Errorf("row %s: not a row of %s's history that can be corrected", in.Row, label)
	}
	req := app.AssetCorrectRequest_builder{Ref: app.AssetById(a.GetId()), RowId: row.Bytes(), Retract: in.Retract, Reason: in.Reason, Op: op()}.Build()
	to := ""
	switch {
	case in.Retract && in.ValidFrom != "":
		return nil, errors.New("retract or valid_from, not both")
	case in.Retract:
		to = "없었던 일로"
	case in.ValidFrom != "":
		from, when, err := moment("valid_from", in.ValidFrom)
		if err != nil {
			return nil, err
		}
		req.SetValidFrom(from)
		to = when + "부터로"
	default:
		return nil, errors.New("retract: it never happened, or valid_from: when it did")
	}
	summary := fmt.Sprintf("정정: %s의 기록 '%s'을(를) %s%s", label, what, to, clause(" — %s", in.Reason))
	return &plan{
		method:  app.AssetService_Correct_FullMethodName,
		summary: summary,
		run: func(ctx context.Context, conn grpc.ClientConnInterface) (*mcpRef, error) {
			v, err := app.NewAssetServiceClient(conn).Correct(ctx, req)
			if err != nil {
				return nil, err
			}
			return refOf(v.GetId(), v.GetName()), nil
		},
	}, nil
}

func (c *mcpCall) assetErase(in mcpAssetPlan) (*plan, error) {
	a, label, err := c.asset("asset", in.Asset)
	if err != nil {
		return nil, err
	}
	ref := app.AssetById(a.GetId())
	return &plan{
		method:  app.AssetService_Erase_FullMethodName,
		summary: fmt.Sprintf("등록 취소: %s (잘못 등록한 것으로 지웁니다)", label),
		run: func(ctx context.Context, conn grpc.ClientConnInterface) (*mcpRef, error) {
			if _, err := app.NewAssetServiceClient(conn).Erase(ctx, ref); err != nil {
				return nil, err
			}
			return refOf(a.GetId(), a.GetName()), nil
		},
	}, nil
}

// Hand-overs.

type mcpStockLine struct {
	Stock    string `json:"stock" jsonschema:"the stock, by id"`
	Quantity int64  `json:"quantity" jsonschema:"how many"`
}

type mcpReturnLine struct {
	Line      string `json:"line" jsonschema:"the hand-over's line, as list_custody's line id"`
	Quantity  int64  `json:"quantity,omitempty" jsonschema:"for stock: how many come back; all that is out if left out"`
	Condition string `json:"condition,omitempty" jsonschema:"what it comes back in: good, damaged or broken"`
}

type mcpCustodyPlan struct {
	Action  string `json:"action" jsonschema:"issue, return, extend or acknowledge"`
	Custody string `json:"custody,omitempty" jsonschema:"return, extend, acknowledge: the hand-over, by list_custody's id"`

	Party  string         `json:"party,omitempty" jsonschema:"issue: who is given it, an id find_people answers with"`
	Assets []string       `json:"assets,omitempty" jsonschema:"issue: the assets handed over, by id or tag"`
	Stock  []mcpStockLine `json:"stock,omitempty" jsonschema:"issue: supplies handed over by quantity"`
	Kind   string         `json:"kind,omitempty" jsonschema:"issue: issue, to keep, or loan, due back; a due date makes it a loan"`
	Due    string         `json:"due,omitempty" jsonschema:"issue: when it is due back; extend: the new date it is due"`
	Desc   string         `json:"desc,omitempty" jsonschema:"issue: a note"`

	Lines []mcpReturnLine `json:"lines,omitempty" jsonschema:"return: some of what is out, by line; everything still out if left out"`
	To    string          `json:"to,omitempty" jsonschema:"return: where the returned assets go, by id or tag"`
	At    string          `json:"at,omitempty" jsonschema:"return: when it came back; now if left out"`
}

func (t mcpTools) planCustody(ctx context.Context, _ *mcp.CallToolRequest, in mcpCustodyPlan) (*mcp.CallToolResult, mcpPlanned, error) {
	c, err := t.call(ctx)
	if err != nil {
		return nil, mcpPlanned{}, err
	}
	var p *plan
	switch in.Action {
	case "issue":
		p, err = c.custodyIssue(in)
	case "return", "extend", "acknowledge":
		p, err = c.custodyAnswer(in)
	default:
		err = errors.New("action: issue, return, extend or acknowledge")
	}
	if err != nil {
		return nil, mcpPlanned{}, err
	}
	return t.planned(c, p)
}

func (c *mcpCall) custodyIssue(in mcpCustodyPlan) (*plan, error) {
	p, who, err := c.party("party", in.Party)
	if err != nil {
		return nil, err
	}
	if len(in.Assets) == 0 && len(in.Stock) == 0 {
		return nil, errors.New("assets or stock: what is handed over")
	}
	req := app.CustodyAddRequest_builder{Party: app.PartyById(p.GetId()), Kind: in.Kind, Desc: in.Desc, Op: op()}.Build()
	what := []string{}
	for _, v := range in.Assets {
		a, label, err := c.asset("assets", v)
		if err != nil {
			return nil, err
		}
		req.SetLines(append(req.GetLines(), app.CustodyLineSpec_builder{Asset: app.AssetById(a.GetId())}.Build()))
		what = append(what, label)
	}
	for _, v := range in.Stock {
		id, err := idOf("stock", v.Stock)
		if err != nil {
			return nil, err
		}
		if v.Quantity <= 0 {
			return nil, errors.New("stock.quantity: how many, more than none")
		}
		s, err := app.NewStockServiceClient(c.conn).Get(c.ctx, app.StockGetRequest_builder{Ref: app.StockById(id.Bytes())}.Build())
		if err != nil {
			return nil, fmt.Errorf("stock %s: %w", v.Stock, failed(err))
		}
		req.SetLines(append(req.GetLines(), app.CustodyLineSpec_builder{Stock: app.StockById(id.Bytes()), Quantity: v.Quantity}.Build()))
		what = append(what, fmt.Sprintf("%s %d%s", s.GetName(), v.Quantity, or(s.GetUnit(), "개")))
	}
	due, until, err := moment("due", in.Due)
	if err != nil {
		return nil, err
	}
	kind := in.Kind
	if due != nil {
		req.SetDueAt(due)
		if kind == "" {
			kind = "loan"
		}
	}
	if kind == "" {
		kind = "issue"
	}
	summary := fmt.Sprintf("%s: %s → %s%s%s", said(custodyWord, kind), strings.Join(what, ", "), who, clause(", 반납 기한 %s", until), clause(" — %s", in.Desc))
	return &plan{
		method:  app.CustodyService_Add_FullMethodName,
		summary: summary,
		run: func(ctx context.Context, conn grpc.ClientConnInterface) (*mcpRef, error) {
			v, err := app.NewCustodyServiceClient(conn).Add(ctx, req)
			if err != nil {
				return nil, err
			}
			return refOf(v.GetId(), who), nil
		},
	}, nil
}

// custodyAnswer plans what is done to a hand-over that is out: its return,
// a later due date, or the receiver saying they have it.
func (c *mcpCall) custodyAnswer(in mcpCustodyPlan) (*plan, error) {
	id, err := idOf("custody", in.Custody)
	if err != nil {
		return nil, err
	}
	k, err := app.NewCustodyServiceClient(c.conn).Get(c.ctx, app.CustodyGetRequest_builder{Ref: app.CustodyById(id.Bytes())}.Build())
	if err != nil {
		return nil, fmt.Errorf("custody %s: %w", in.Custody, failed(err))
	}
	c.fetchParties([]pdid.Id{pid(k.GetParty().GetId())})
	whose := fmt.Sprintf("%s에게 %s한 것", or(c.parties[pid(k.GetParty().GetId())], "?"), said(custodyWord, k.GetKind()))
	ref := app.CustodyById(id.Bytes())
	result := refOf(k.GetId(), whose)

	switch in.Action {
	case "extend":
		due, until, err := moment("due", in.Due)
		if err != nil {
			return nil, err
		}
		if due == nil {
			return nil, errors.New("due: the new date it is due")
		}
		req := app.CustodyExtendRequest_builder{Ref: ref, DueAt: due}.Build()
		return &plan{
			method:  app.CustodyService_Extend_FullMethodName,
			summary: fmt.Sprintf("반납 기한 연장: %s → %s", whose, until),
			run: func(ctx context.Context, conn grpc.ClientConnInterface) (*mcpRef, error) {
				_, err := app.NewCustodyServiceClient(conn).Extend(ctx, req)
				return result, err
			},
		}, nil
	case "acknowledge":
		req := app.CustodyAcknowledgeRequest_builder{Ref: ref}.Build()
		return &plan{
			method:  app.CustodyService_Acknowledge_FullMethodName,
			summary: fmt.Sprintf("인수 확인: %s을(를) 받았음", whose),
			run: func(ctx context.Context, conn grpc.ClientConnInterface) (*mcpRef, error) {
				_, err := app.NewCustodyServiceClient(conn).Acknowledge(ctx, req)
				return result, err
			},
		}, nil
	}

	// return: which lines, named.
	ls, err := app.NewCustodyLineServiceClient(c.conn).List(c.ctx, app.CustodyLineListRequest_builder{
		Filters: []*app.CustodyLineFilter{app.CustodyLineFilter_builder{Custody: ref}.Build()},
		Size:    100,
	}.Build())
	if err != nil {
		return nil, failed(err)
	}
	named := map[pdid.Id]string{}
	for _, l := range ls.GetItems() {
		name := "?"
		if a := pid(l.GetAsset().GetId()); !a.IsZero() {
			if v, label, err := c.asset("line", a.String()); err == nil {
				_ = v
				name = label
			}
		} else if s := pid(l.GetStock().GetId()); !s.IsZero() {
			if v, err := app.NewStockServiceClient(c.conn).Get(c.ctx, app.StockGetRequest_builder{Ref: app.StockById(s.Bytes())}.Build()); err == nil {
				name = fmt.Sprintf("%s %d%s", v.GetName(), l.GetQuantity()-l.GetReturnedQuantity(), or(v.GetUnit(), "개"))
			}
		}
		named[pid(l.GetId())] = name
	}
	req := app.CustodyReturnRequest_builder{Ref: ref, Op: op()}.Build()
	what := []string{}
	for _, l := range in.Lines {
		lid, err := idOf("lines.line", l.Line)
		if err != nil {
			return nil, err
		}
		name, ok := named[lid]
		if !ok {
			return nil, fmt.Errorf("lines.line %s: not a line of this hand-over", l.Line)
		}
		req.SetLines(append(req.GetLines(), app.CustodyReturnLine_builder{Line: app.CustodyLineById(lid.Bytes()), Quantity: l.Quantity, Condition: l.Condition}.Build()))
		if l.Quantity > 0 {
			name = fmt.Sprintf("%s 중 %d", name, l.Quantity)
		}
		what = append(what, name+clause(" (%s)", said(conditionWord, l.Condition)))
	}
	if len(what) == 0 {
		what = append(what, "아직 나가 있는 것 전부")
	}
	into := ""
	if in.To != "" {
		to, _, err := c.asset("to", in.To)
		if err != nil {
			return nil, err
		}
		req.SetTo(app.AssetById(to.GetId()))
		into = " → " + c.place(to, true)
	}
	at, when, err := moment("at", in.At)
	if err != nil {
		return nil, err
	}
	if at != nil {
		req.SetAt(at)
	}
	return &plan{
		method:  app.CustodyService_Return_FullMethodName,
		summary: fmt.Sprintf("반납: %s: %s%s%s", whose, strings.Join(what, ", "), into, clause(", %s에", when)),
		run: func(ctx context.Context, conn grpc.ClientConnInterface) (*mcpRef, error) {
			_, err := app.NewCustodyServiceClient(conn).Return(ctx, req)
			return result, err
		},
	}, nil
}

// Reservations.

type mcpReservationPlan struct {
	Action      string `json:"action" jsonschema:"reserve, confirm, approve, reject, cancel, check_in or complete"`
	Reservation string `json:"reservation,omitempty" jsonschema:"every action but reserve: the reservation's id, as reservations answers it"`

	Assets   []string `json:"assets,omitempty" jsonschema:"reserve: what is reserved -- a room, a piece of equipment -- by id or tag"`
	Begins   string   `json:"begins,omitempty" jsonschema:"reserve: when it begins, RFC 3339"`
	Ends     string   `json:"ends,omitempty" jsonschema:"reserve: when it ends, RFC 3339"`
	Name     string   `json:"name,omitempty" jsonschema:"reserve: what it is for"`
	For      string   `json:"for,omitempty" jsonschema:"reserve: who it is for, an id find_people answers with; the person signed in if left out"`
	Repeat   string   `json:"repeat,omitempty" jsonschema:"reserve: again by this rule (RFC 5545): FREQ=WEEKLY;COUNT=4"`
	Hold     bool     `json:"hold,omitempty" jsonschema:"reserve: hold the time for a few minutes while the rest is decided"`
	Override bool     `json:"override,omitempty" jsonschema:"reserve: book over a conflict on purpose, which needs a reason"`

	Series bool   `json:"series,omitempty" jsonschema:"cancel: the whole series it belongs to"`
	Reason string `json:"reason,omitempty" jsonschema:"why: reject and override want one"`
}

var decisions = map[string]struct {
	word   string
	method string
	call   func(app.ReservationServiceClient, context.Context, *app.ReservationDecideRequest, ...grpc.CallOption) (*app.Reservation, error)
}{
	"confirm":  {"예약 확정", app.ReservationService_Confirm_FullMethodName, app.ReservationServiceClient.Confirm},
	"approve":  {"예약 승인", app.ReservationService_Approve_FullMethodName, app.ReservationServiceClient.Approve},
	"reject":   {"예약 반려", app.ReservationService_Reject_FullMethodName, app.ReservationServiceClient.Reject},
	"cancel":   {"예약 취소", app.ReservationService_Cancel_FullMethodName, app.ReservationServiceClient.Cancel},
	"check_in": {"체크인", app.ReservationService_CheckIn_FullMethodName, app.ReservationServiceClient.CheckIn},
	"complete": {"이용 완료", app.ReservationService_Complete_FullMethodName, app.ReservationServiceClient.Complete},
}

// outOfUseRefused is what the server will not do to a reservation that
// booked what is out of use (D43), as a plan says it: the rest -- rejecting
// it, cancelling it, ending it -- it does.
var outOfUseRefused = map[string]string{
	"confirm":  "confirm the hold; cancel it instead",
	"approve":  "approve it; reject or cancel it instead",
	"check_in": "check it in",
}

func (t mcpTools) planReservation(ctx context.Context, _ *mcp.CallToolRequest, in mcpReservationPlan) (*mcp.CallToolResult, mcpPlanned, error) {
	c, err := t.call(ctx)
	if err != nil {
		return nil, mcpPlanned{}, err
	}
	var p *plan
	if in.Action == "reserve" {
		p, err = c.reserve(in)
	} else if _, ok := decisions[in.Action]; ok {
		p, err = c.decide(in)
	} else {
		err = errors.New("action: reserve, confirm, approve, reject, cancel, check_in or complete")
	}
	if err != nil {
		return nil, mcpPlanned{}, err
	}
	return t.planned(c, p)
}

func between(begins, ends time.Time) string {
	b, e := begins.Local(), ends.Local()
	if b.Format(time.DateOnly) == e.Format(time.DateOnly) {
		return b.Format("2006-01-02 15:04") + "–" + e.Format("15:04")
	}
	return b.Format("2006-01-02 15:04") + " – " + e.Format("2006-01-02 15:04")
}

func (c *mcpCall) reserve(in mcpReservationPlan) (*plan, error) {
	if len(in.Assets) == 0 {
		return nil, errors.New("assets: what is reserved")
	}
	begins, err := at("begins", in.Begins)
	if err != nil {
		return nil, err
	}
	ends, err := at("ends", in.Ends)
	if err != nil {
		return nil, err
	}
	req := app.ReservationAddRequest_builder{Name: in.Name, BeginsAt: timestamppb.New(begins), EndsAt: timestamppb.New(ends), Op: op()}.Build()
	what := []string{}
	gone := []string{}
	for _, v := range in.Assets {
		a, label, err := c.asset("assets", v)
		if err != nil {
			return nil, err
		}
		if slices.Contains(domain.OutOfUse, a.GetStatus()) {
			gone = append(gone, fmt.Sprintf("%s (%s)", label, a.GetStatus()))
			continue
		}
		req.SetItems(append(req.GetItems(), app.ReservationItemSpec_builder{Resource: app.AssetById(a.GetId())}.Build()))
		what = append(what, label)
	}
	if len(gone) > 0 {
		// What the server refuses, said before it is put to the person
		// (D33, D43).
		return nil, fmt.Errorf("assets: what is out of use is not booked anew, and the server would refuse it: %s", strings.Join(gone, ", "))
	}
	whom := ""
	if in.For != "" {
		p, name, err := c.party("for", in.For)
		if err != nil {
			return nil, err
		}
		req.SetParty(app.PartyById(p.GetId()))
		whom = name
	}
	if in.Repeat != "" {
		req.SetRepeat(in.Repeat)
	}
	if in.Hold {
		req.SetHold(true)
	}
	if in.Override {
		req.SetOverride(true)
	}
	if in.Reason != "" {
		req.SetReason(in.Reason)
	}
	summary := fmt.Sprintf("예약: %s, %s%s%s%s%s", strings.Join(what, ", "), between(begins, ends),
		clause(", %s", in.Name), clause(", %s", whom), clause(", 반복 %s", in.Repeat), clause(", 겹쳐도 예약: %s", map[bool]string{true: or(in.Reason, "-")}[in.Override]))
	return &plan{
		method:  app.ReservationService_Add_FullMethodName,
		summary: summary,
		run: func(ctx context.Context, conn grpc.ClientConnInterface) (*mcpRef, error) {
			v, err := app.NewReservationServiceClient(conn).Add(ctx, req)
			if err != nil {
				return nil, err
			}
			return refOf(v.GetId(), v.GetName()), nil
		},
	}, nil
}

// voided names what `r` booked of `ids` that was deleted, as the calendar
// says it is out of use to it: "G-1 카메라 풀 (voided)" (D47).
func (c *mcpCall) voided(r *app.Reservation, ids [][]byte) ([]string, error) {
	v, err := app.NewReservationServiceClient(c.conn).Calendar(c.ctx, app.ReservationCalendarRequest_builder{
		From: timestamppb.New(r.GetBeginsAt().AsTime().Add(-time.Minute)),
		To:   timestamppb.New(r.GetEndsAt().AsTime().Add(time.Minute)),
	}.Build())
	if err != nil {
		return nil, failed(err)
	}
	out := []string{}
	for _, e := range v.GetEntries() {
		if string(e.GetReservation().GetId()) != string(r.GetId()) {
			continue
		}
		for _, g := range e.GetOutOfUse() {
			if g.GetStatus() == "voided" && slices.ContainsFunc(ids, func(id []byte) bool { return string(id) == string(g.GetResourceId()) }) {
				out = append(out, fmt.Sprintf("%s (%s)", strings.TrimSpace(g.GetResourceTag()+" "+g.GetResourceName()), g.GetStatus()))
			}
		}
	}
	return out, nil
}

// decide plans one of what is done to a reservation that is there.
func (c *mcpCall) decide(in mcpReservationPlan) (*plan, error) {
	id, err := idOf("reservation", in.Reservation)
	if err != nil {
		return nil, err
	}
	r, err := app.NewReservationServiceClient(c.conn).Get(c.ctx, app.ReservationGetRequest_builder{Ref: app.ReservationById(id.Bytes())}.Build())
	if err != nil {
		return nil, fmt.Errorf("reservation %s: %w", in.Reservation, failed(err))
	}
	items, err := app.NewReservationItemServiceClient(c.conn).List(c.ctx, app.ReservationItemListRequest_builder{
		Filters: []*app.ReservationItemFilter{app.ReservationItemFilter_builder{Reservation: app.ReservationById(id.Bytes())}.Build()},
		Size:    100,
	}.Build())
	if err != nil {
		return nil, failed(err)
	}
	what := []string{}
	gone := []string{}
	unread := [][]byte{}
	for _, it := range items.GetItems() {
		a, label, err := c.asset("resource", idString(it.GetResource().GetId()))
		if err != nil {
			unread = append(unread, it.GetResource().GetId())
			continue
		}
		what = append(what, label)
		if slices.Contains(domain.OutOfUse, a.GetStatus()) {
			gone = append(gone, fmt.Sprintf("%s (%s)", label, a.GetStatus()))
		}
	}
	if _, ok := outOfUseRefused[in.Action]; ok && len(unread) > 0 {
		// What is no longer there to read was deleted as registered by
		// mistake: out of use to what booked it, for good (D47), and named
		// as the reservation's calendar entry names it.
		vs, err := c.voided(r, unread)
		if err != nil {
			return nil, err
		}
		gone = append(gone, vs...)
	}
	if refused, ok := outOfUseRefused[in.Action]; ok && len(gone) > 0 {
		// Saying yes to it is booking it, and checking into it is using it,
		// which the server refuses of what is out of use (D43): said before
		// it is put to the person (D33).
		return nil, fmt.Errorf("reservation: what it booked is out of use, %s, and the server would not %s", strings.Join(gone, ", "), refused)
	}
	d := decisions[in.Action]
	req := app.ReservationDecideRequest_builder{Ref: app.ReservationById(id.Bytes()), Reason: in.Reason, Series: in.Series}.Build()
	summary := fmt.Sprintf("%s: %s, %s%s%s%s", d.word, strings.Join(what, ", "), between(r.GetBeginsAt().AsTime(), r.GetEndsAt().AsTime()),
		clause(", %s", r.GetName()), map[bool]string{true: ", 반복 전체"}[in.Series], clause(" — %s", in.Reason))
	return &plan{
		method:  d.method,
		summary: summary,
		run: func(ctx context.Context, conn grpc.ClientConnInterface) (*mcpRef, error) {
			v, err := d.call(app.NewReservationServiceClient(conn), ctx, req)
			if err != nil {
				return nil, err
			}
			return refOf(v.GetId(), v.GetName()), nil
		},
	}, nil
}
