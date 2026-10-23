package domain_test

import (
	"github.com/lesomnus/payday/pdid"
	"google.golang.org/protobuf/proto"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
)

// An event keeps the message of its kind and no sentence (payday#151): what
// it says is made of the message as it is read. A test reads both as a person
// and a program would: the sentence through the feed (`EventService.Recent`),
// said for the workspace's owner, and the message unpacked.

// statusField is an asset's status, by the number payday's past keeps facts
// of it by.
const statusField = 16

// say is the sentence the workspace's feed says `ev` with.
func (e *env) say(ev *ent.Event) string {
	e.t.Helper()
	v, err := e.app().Event().Recent(e.owner, app.EventRecentRequest_builder{Size: 500, Kind: ev.Kind, Children: true}.Build())
	e.x.NoError(err)
	for _, it := range v.GetItems() {
		if string(it.GetId()) == string(pdid.Id(ev.Id).Bytes()) {
			return it.GetDesc()
		}
	}
	e.x.Failf("not in the feed", "%s %s", ev.Kind, pdid.Id(ev.Id))
	return ""
}

// value is the message `ev` holds, as a `M`.
func value[M proto.Message](e *env, ev *ent.Event) M {
	e.t.Helper()
	var m M
	m = m.ProtoReflect().Type().New().Interface().(M)
	e.x.NotNil(ev.Value, "%s holds no message", ev.Kind)
	e.x.NoError(ev.Value.UnmarshalTo(m))
	return m
}

// eventOf is the one event of `kind` in the env's workspace.
func (e *env) eventOf(kind string) *ent.Event {
	e.t.Helper()
	vs := e.eventsOf(kind)
	e.x.Len(vs, 1, kind)
	return vs[0]
}
