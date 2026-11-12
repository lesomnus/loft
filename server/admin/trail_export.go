package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"time"
	"uuid"

	"filippo.io/age"
	"filippo.io/age/plugin"
	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/trail"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/loft"
	"github.com/lesomnus/loft/internal/ent"
	enttenant "github.com/lesomnus/loft/internal/ent/tenant"
	"github.com/lesomnus/loft/internal/identity"
	"github.com/lesomnus/loft/server/operator"
	"github.com/lesomnus/loft/server/pd"
)

// Export is a workspace's trail taken out, encrypted to whoever keeps it, for
// a demand to preserve it (loft#153). Nothing holds what a workspace that
// retired left past the deployment's floors -- a legal hold is on a workspace
// there is -- so a demand that comes while it is still there is answered by
// taking it out, and a live workspace's the same way. What goes is payday's to
// say (`trail.Policy.ExportTenant`), written encrypted to `to` and never
// otherwise.
//
// It hands people's data out, so it is a download: what it is for is said in
// `payday-reason-bin`, and kept in the bundle as why, with the operator as
// who; and its row of the access record is written before its first byte,
// naming everybody whose data it carries by the identifiers its rows carry --
// a destroyed workspace's people are no rows any more.
func (s operatorTrail) Export(req *app.OperatorTrailExportRequest, out grpc.ServerStreamingServer[app.OperatorTrailExportResponse]) error {
	ctx := out.Context()
	md, _ := metadata.FromIncomingContext(ctx)
	why := strings.TrimSpace(strings.Join(md.Get(access.ReasonKey), " "))
	if why == "" {
		return status.Errorf(codes.InvalidArgument, "%s: what it is for -- it hands people's data out, and is recorded", access.ReasonKey)
	}
	to, err := recipientsOf(req.GetTo())
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "to: %v", err)
	}
	tenant, alias, err := trailOf(ctx, s.a.S.Ent, s.a.S.Identity, req.GetTenant())
	if err != nil {
		return err
	}

	people, err := carried(ctx, s.a, tenant)
	if err != nil {
		return err
	}
	access.Saw(ctx, people...)
	name := tenant.String()
	if alias != "" {
		name = alias
	}
	if err := out.Send(app.OperatorTrailExportResponse_builder{Name: fmt.Sprintf("%s-trail-%s.age", name, time.Now().UTC().Format("20060102"))}.Build()); err != nil {
		return err
	}

	// The bundle as it is written, a part at a time, on this goroutine alone,
	// and a part with nothing in it while the trail is read and nothing
	// written: what keeps a long one's connection from being taken for idle.
	r, w := io.Pipe()
	type made struct {
		v   trail.Exported
		err error
	}
	done := make(chan made, 1)
	o, _ := operator.From(ctx)
	go func() {
		v, err := s.a.S.Trail.ExportTenant(ctx, pd.TrailStore(s.a.S.Ent), tenant, trail.Export{By: o.Name, Why: why, To: to}, w)
		w.CloseWithError(err)
		done <- made{v, err}
	}()
	parts := make(chan []byte)
	failed := make(chan error, 1)
	go func() {
		defer close(parts)
		for {
			buf := make([]byte, exportPart)
			n, err := io.ReadFull(r, buf)
			if n > 0 {
				parts <- buf[:n]
			}
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return
			}
			if err != nil {
				failed <- err
				return
			}
		}
	}()
	tick := time.NewTicker(Heartbeat)
	defer tick.Stop()
	for sending := true; sending; {
		select {
		case b, ok := <-parts:
			if !ok {
				sending = false
				break
			}
			if err := out.Send(app.OperatorTrailExportResponse_builder{Data: b}.Build()); err != nil {
				r.CloseWithError(err)
				<-done
				return err
			}
		case <-tick.C:
			if err := out.Send(&app.OperatorTrailExportResponse{}); err != nil {
				r.CloseWithError(err)
				<-done
				return err
			}
		}
	}
	m := <-done
	select {
	case err := <-failed:
		if m.err == nil {
			m.err = err
		}
	default:
	}
	if m.err != nil {
		return m.err
	}

	v := m.v
	x := app.OperatorTrailExported_builder{
		Tenant:   tenant.Bytes(),
		Alias:    alias,
		Database: uint32(v.Database),
		Archive:  uint32(v.Archive),
		Chunks:   uint32(len(v.Chunks)),
		Files:    uint32(len(v.Files)),
		Digest:   string(v.Digest),
		People:   uint32(len(people)),
	}.Build()
	if cp := v.Checkpoint; cp != nil {
		x.SetCheckpoint(uint32(cp.K))
		x.SetCheckpointDigest(string(cp.Digest))
		for _, w := range cp.Witnessed {
			x.SetWitnesses(append(x.GetWitnesses(), w.Witness))
		}
		x.SetUncovered(slices.ContainsFunc(v.Chunks, func(c trail.ExportChunk) bool { return !c.Covered }))
	}
	if r := v.Retired; r != nil {
		x.SetDateRetired(timestamppb.New(r.At))
		x.SetDateKeptUntil(timestamppb.New(r.Until))
	}
	log.From(ctx).InfoContext(ctx, "trail: a workspace's trail taken out", "operator", o.Name, "tenant", tenant, "manifest", v.Digest, "people", len(people))
	return out.Send(app.OperatorTrailExportResponse_builder{Exported: x}.Build())
}

// trailOf is the workspace `v` names: by its identifier, or by its address --
// a live workspace's, or, for one destroyed here, the tenant's at roster this
// deployment serves, which is the address it went by (D86).
func trailOf(ctx context.Context, db *ent.Client, people *identity.Store, v string) (pdid.Id, string, error) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "@")
	if v == "" {
		return pdid.Nil, "", status.Error(codes.InvalidArgument, "tenant: which workspace -- its identifier, or its address")
	}
	if id, err := pdid.Parse(v); err == nil {
		return id, "", nil
	}

	t, err := db.Tenant.Query().Where(enttenant.Alias(v)).Only(ctx)
	if err == nil {
		return pdid.Id(t.Id), t.Alias, nil
	}
	if !ent.IsNotFound(err) {
		return pdid.Nil, "", err
	}

	if people != nil {
		served, err := people.Tenant(ctx)
		if err == nil && served.Alias == v {
			return served.Id, v, nil
		}
		if err != nil && !errors.Is(err, identity.ErrNoTenant) {
			return pdid.Nil, "", fmt.Errorf("roster: %w", err)
		}
	}
	return pdid.Nil, "", status.Errorf(codes.NotFound, "no workspace is @%s, nor is the tenant this deployment serves at roster: name a destroyed one by its identifier", v)
}

// carried is everybody a workspace's trail carries the data of, by the
// identifiers its rows carry: whoever acted, the people its rows are about --
// a row of a kind that holds a field declared personal -- and in the access
// record whose data each call carried.
func carried(ctx context.Context, a Routes, tenant pdid.Id) ([]access.Subject, error) {
	seen := map[pdid.Id]bool{}
	out := []access.Subject{}
	add := func(id pdid.Id) {
		if id.IsZero() || seen[id] || operator.Is(id) {
			return
		}
		seen[id] = true
		out = append(out, access.Subject{Id: id, Tenant: tenant})
	}
	read := func(doc []byte) error {
		var v struct {
			Actor    []byte   `json:"actorId"`
			Object   []byte   `json:"objectId"`
			Domain   uint32   `json:"domain"`
			Subjects [][]byte `json:"subjects"`
		}
		if err := json.Unmarshal(doc, &v); err != nil {
			return err
		}
		add(rowId(v.Actor))
		if personal[pdid.Domain(v.Domain)] {
			add(rowId(v.Object))
		}
		for _, b := range v.Subjects {
			add(rowId(b))
		}
		return nil
	}

	store := pd.TrailStore(a.S.Ent)
	until := time.Now().Add(time.Hour)
	for _, whose := range []trail.Whose{trail.Alone, trail.Together} {
		at := trail.Cursor{}
		for {
			vs, err := store.Older(ctx, trail.Scope{Whose: whose, Tenant: tenant}, until, at, trail.Batch)
			if err != nil {
				return nil, err
			}
			for _, v := range vs {
				if err := read(v.Doc); err != nil {
					return nil, err
				}
				at = trail.Cursor{Created: v.Created, Key: v.Key}
			}
			if len(vs) < trail.Batch {
				break
			}
		}
	}
	if a.S.Trail.Archive != nil {
		if err := a.S.Trail.ReadTenant(ctx, store, tenant, read); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// rowId is an identifier as protojson carries one, base64 of its bytes, and
// nil when it is none.
func rowId(b []byte) pdid.Id {
	if len(b) != len(uuid.UUID{}) {
		return pdid.Nil
	}
	return pdid.Id(uuid.UUID(b))
}

// personal is the kinds whose rows hold a person: a field of theirs declared
// personal (`payday.field`), read off the schema.
var personal = func() map[pdid.Domain]bool {
	out := map[pdid.Domain]bool{}
	for name, d := range pd.Domains {
		mt, err := protoregistry.GlobalTypes.FindMessageByName(protoreflect.FullName(name))
		if err != nil {
			continue
		}
		fs := mt.Descriptor().Fields()
		for i := range fs.Len() {
			o, _ := proto.GetExtension(fs.Get(i).Options(), pdpb.E_Field).(*pdpb.Field)
			if o.GetPersonal() {
				out[d] = true
				break
			}
		}
	}
	return out
}()

// recipientsOf is whoever the lines `vs` name, each as age reads a recipient
// -- `age1…`, an SSH key -- or a plugin's, sealed to by running
// `age-plugin-<name>` here, without its device. Each is a line the caller
// sends: a file of them is read where the caller is, never here.
func recipientsOf(vs []string) ([]age.Recipient, error) {
	out := []age.Recipient{}
	for _, line := range vs {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if rs, err := age.ParseRecipients(strings.NewReader(line)); err == nil {
			out = append(out, rs...)
			continue
		}
		name, _, err := plugin.ParseRecipient(line)
		if err != nil {
			return nil, fmt.Errorf("%q is no age recipient", line)
		}
		if _, err := exec.LookPath("age-plugin-" + name); err != nil {
			return nil, fmt.Errorf("age-plugin-%s is not on the entry point's PATH, and it is what this key is sealed to through", name)
		}
		r, err := plugin.NewRecipient(line, sealing)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		return nil, errors.New("whoever keeps it: an age recipient, or more")
	}
	return out, nil
}

// sealing is what a plugin may ask of a call that only seals: nothing, since
// the device is for opening.
var sealing = &plugin.ClientUI{
	DisplayMessage: func(name, message string) error { return nil },
	RequestValue: func(name, prompt string, _ bool) (string, error) {
		return "", fmt.Errorf("age-plugin-%s asks %q, and sealing asks nothing", name, prompt)
	},
	Confirm: func(name, prompt, _, _ string) (bool, error) {
		return false, fmt.Errorf("age-plugin-%s asks %q, and sealing asks nothing", name, prompt)
	},
}
