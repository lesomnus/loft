package opensearch_test

import (
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/server/opensearch"
)

func plain(s string) (string, error) { return s, nil }

// cluster is a node that answers the two calls `Status` makes for a
// deployment with no index yet, and counts the requests it was sent.
func cluster(t *testing.T, check func(r *http.Request) int) (*httptest.Server, *int) {
	n := 0
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if check != nil {
			if code := check(r); code != 0 {
				w.WriteHeader(code)
				_, _ = io.WriteString(w, `{"error":{"type":"security_exception","reason":"no"},"status":401}`)
				return
			}
		}
		switch {
		case strings.Contains(r.URL.Path, "/_alias/"):
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"alias [rove-assets] missing","status":404}`)
		case strings.HasSuffix(r.URL.Path, "/_settings/index.creation_date"):
			_, _ = io.WriteString(w, `{}`)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(s.Close)
	return s, &n
}

// TestNoAddressIsNoCluster: a deployment that names none searches its
// database, and is not refused for it.
func TestNoAddressIsNoCluster(t *testing.T) {
	c, err := opensearch.New(opensearch.Config{}, plain)
	require.NoError(t, err)
	require.Nil(t, c)
}

// TestAConfigurationItCannotUseIsRefused, with what to write instead.
func TestAConfigurationItCannotUseIsRefused(t *testing.T) {
	for _, v := range []struct {
		c    opensearch.Config
		says string
	}{
		{opensearch.Config{Addrs: []string{"search-0:9200"}}, "not a node's address"},
		{opensearch.Config{Addrs: []string{"ftp://search-0"}}, "not a node's address"},
		{opensearch.Config{Addrs: []string{"https://admin:secret@search-0:9200"}}, "username and password"},
		{opensearch.Config{Addrs: []string{"https://search-0:9200"}, Index: "Rove"}, "not the start of an index name"},
		{opensearch.Config{Addrs: []string{"https://search-0:9200"}, Index: "-rove"}, "not the start of an index name"},
		{opensearch.Config{Addrs: []string{"https://search-0:9200"}, CaFile: filepath.Join(t.TempDir(), "none.pem")}, "ca_file"},
	} {
		_, err := opensearch.New(v.c, plain)
		require.ErrorContains(t, err, v.says, "%+v", v.c)
	}

	t.Setenv("ROVE_TEST_NO_SUCH_PASSWORD", "")
	_, err := opensearch.New(opensearch.Config{Addrs: []string{"https://search-0:9200"}, Password: "env:ROVE_TEST_NO_SUCH_PASSWORD"}, func(s string) (string, error) {
		return "", os.ErrNotExist
	})
	require.ErrorContains(t, err, "app.search.opensearch.password")
}

// TestARequestGoesOnToANodeThatAnswers, and the next one starts there.
func TestARequestGoesOnToANodeThatAnswers(t *testing.T) {
	x := require.New(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)
	down := "http://" + l.Addr().String()
	x.NoError(l.Close())

	up, n := cluster(t, nil)
	up.Start()
	c, err := opensearch.New(opensearch.Config{Addrs: []string{down, up.URL}}, plain)
	x.NoError(err)
	defer c.Close()

	s, err := c.Status(t.Context(), opensearch.Assets)
	x.NoError(err)
	x.Equal("rove-assets", s.Alias)
	x.Empty(s.Index, "an alias that is not there is a deployment with no index yet")
	x.Equal(2, *n)

	_, err = c.Status(t.Context(), opensearch.Assets)
	x.NoError(err)
	x.Equal(4, *n)
}

// TestItSignsInAndTrustsTheCaItIsGiven -- and no other: the system pool does
// not vouch for a cluster's own CA.
func TestItSignsInAndTrustsTheCaItIsGiven(t *testing.T) {
	x := require.New(t)
	s, _ := cluster(t, func(r *http.Request) int {
		if u, p, ok := r.BasicAuth(); !ok || u != "rove" || p != "secret" {
			return http.StatusUnauthorized
		}
		return 0
	})
	s.StartTLS()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	x.NoError(os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw}), 0o600))

	t.Setenv("ROVE_TEST_SEARCH_PASSWORD", "secret")
	resolve := func(ref string) (string, error) {
		v, ok := strings.CutPrefix(ref, "env:")
		x.True(ok)
		return os.Getenv(v), nil
	}
	good := opensearch.Config{Addrs: []string{s.URL}, Username: "rove", Password: "env:ROVE_TEST_SEARCH_PASSWORD", CaFile: ca}
	c, err := opensearch.New(good, resolve)
	x.NoError(err)
	_, err = c.Status(t.Context(), opensearch.Assets)
	x.NoError(err)

	bad := good
	bad.Password = "env:NOT_THE_PASSWORD"
	t.Setenv("NOT_THE_PASSWORD", "guess")
	c, err = opensearch.New(bad, resolve)
	x.NoError(err)
	_, err = c.Status(t.Context(), opensearch.Assets)
	var e *opensearch.Error
	x.ErrorAs(err, &e)
	x.Equal(http.StatusUnauthorized, e.Status)
	x.Equal("security_exception", e.Type)

	untrusted := good
	untrusted.CaFile = ""
	c, err = opensearch.New(untrusted, resolve)
	x.NoError(err)
	_, err = c.Status(t.Context(), opensearch.Assets)
	x.ErrorContains(err, "certificate")
}

// TestAnOlderDocumentIsStaleAndNotAFailure: the index kept the newer one,
// which is what it is for. Taking out what is not there is not one either;
// anything else the cluster refused is.
func TestAnOlderDocumentIsStaleAndNotAFailure(t *testing.T) {
	x := require.New(t)
	answer := ""
	var sent []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		x.Equal("/_bulk", r.URL.Path)
		x.Equal("application/x-ndjson", r.Header.Get("Content-Type"))
		b, _ := io.ReadAll(r.Body)
		sent = strings.Split(strings.TrimSpace(string(b)), "\n")
		_, _ = io.WriteString(w, answer)
	}))
	defer s.Close()
	c, err := opensearch.New(opensearch.Config{Addrs: []string{s.URL}}, plain)
	x.NoError(err)

	ops := []opensearch.Op{
		{Index: "rove-assets", Id: "a", Version: 7, Doc: &opensearch.Doc{Tenant: "t", Name: "웹서버"}},
		{Index: "rove-assets", Id: "b", Version: 3, Doc: &opensearch.Doc{Tenant: "t"}},
		{Index: "rove-assets", Id: "c", Version: 9},
	}
	answer = `{"errors":true,"items":[
		{"index":{"_id":"a","status":201}},
		{"index":{"_id":"b","status":409,"error":{"type":"version_conflict_engine_exception","reason":"newer"}}},
		{"delete":{"_id":"c","status":404}}]}`
	stale, err := c.Bulk(t.Context(), ops)
	x.NoError(err)
	x.Equal(1, stale)
	x.Len(sent, 5, "a delete has no document line")
	var meta map[string]map[string]any
	x.NoError(json.Unmarshal([]byte(sent[0]), &meta))
	x.Equal(map[string]any{"_index": "rove-assets", "_id": "a", "version": float64(7), "version_type": "external_gte"}, meta["index"])
	x.Contains(sent[1], `"name":"웹서버"`)
	x.Contains(sent[4], `"delete"`)

	answer = `{"errors":true,"items":[
		{"index":{"_id":"a","status":400,"error":{"type":"strict_dynamic_mapping_exception","reason":"no field x"}}},
		{"index":{"_id":"b","status":201}},
		{"delete":{"_id":"c","status":200}}]}`
	_, err = c.Bulk(t.Context(), ops)
	x.ErrorContains(err, "1 of 3 writes refused")
	x.ErrorContains(err, "strict_dynamic_mapping_exception")
}

// TestADocumentIsFoldedAndVersionedByItsNewestRow: a model renamed after
// the asset was last written is a newer document of the asset.
func TestADocumentIsFoldedAndVersionedByItsNewestRow(t *testing.T) {
	x := require.New(t)
	t0 := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	parent := uuid.New()
	a := &ent.Asset{
		Id: uuid.New(), TenantId: uuid.New(), Name: "ＧＰＵ서버", Tag: "SV-001", Serial: "Ab12",
		Kind: "item", Status: "active", DateUpdated: t0, ParentId: &parent,
	}
	a.Edges.Model = &ent.ItemModel{Id: uuid.New(), Name: "PowerEdge R660", Maker: "Dell", DateUpdated: t0.Add(time.Hour)}
	a.Edges.Custodian = &ent.Party{Id: uuid.New(), Name: "김민수", DateUpdated: t0.Add(-time.Hour)}

	d, v := opensearch.DocOf(a)
	x.Equal(t0.Add(time.Hour).UnixMicro(), v)
	x.Equal("gpu서버", d.Name, "full-width letters are found by the letters typed")
	x.Equal("sv-001", d.Tag)
	x.Equal("ab12", d.Serial)
	x.Equal("poweredge r660", d.Model)
	x.Equal("dell", d.Maker)
	x.Equal("김민수", d.Custodian)
	x.Equal(a.Edges.Model.Id.String(), d.ModelId)
	x.Equal(parent.String(), d.ParentId)
	x.Empty(d.Type)
	x.Empty(d.TypeId)
	x.Equal(a.TenantId.String(), d.Tenant)
}
