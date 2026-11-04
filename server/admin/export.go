package admin

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/pdid"
	"google.golang.org/grpc"

	app "github.com/lesomnus/loft"
)

// exportPart is how much of an export one message carries.
const exportPart = 512 << 10

// Export is everything a workspace has, as one zip (D29, D34), of any
// workspace: what its owner takes out from inside, taken out by the operator
// -- for a workspace that is leaving, before its retirement or purge (design
// 8.2). A download of everybody in it: what it is for is said in
// `payday-reason-bin`, and the access record names whose data it carries, by
// the identifiers its rows carry, before its first message leaves. Made whole
// before its bytes are sent, as the owner's is.
func (s tenants) Export(req *app.TenantExportRequest, out grpc.ServerStreamingServer[app.TenantExportResponse]) error {
	ctx := out.Context()
	t, err := s.a.workspace(ctx, req.GetRef())
	if err != nil {
		return err
	}
	id := pdid.Id(t.Id)
	off := s.a.S.Offboard(s.a.C)
	people, err := off.People(ctx, id)
	if err != nil {
		return err
	}
	access.Saw(ctx, people...)
	if err := out.Send(app.TenantExportResponse_builder{
		Name:        fmt.Sprintf("%s-%s.zip", t.Alias, time.Now().UTC().Format("20060102")),
		ContentType: "application/zip",
	}.Build()); err != nil {
		return err
	}

	f, err := os.CreateTemp("", "loft-export-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := off.Export(ctx, id, f); err != nil {
		return fmt.Errorf("export of @%s: %w", t.Alias, err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return send(f, func(b []byte) error {
		return out.Send(app.TenantExportResponse_builder{Data: b}.Build())
	})
}

// send hands what `r` holds to `part`, [exportPart] at a time, each part its
// own: a message sent is not the sender's to change, whatever reads it after.
func send(r io.Reader, part func([]byte) error) error {
	for {
		buf := make([]byte, exportPart)
		n, err := io.ReadFull(r, buf)
		if n > 0 {
			if err := part(buf[:n]); err != nil {
				return err
			}
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
