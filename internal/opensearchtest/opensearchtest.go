// Package opensearchtest is a cluster for a test to search on: the one
// PDTEST_OPENSEARCH names, the way PDTEST_POSTGRES names a database, or the
// test skipped when it names none.
//
//	PDTEST_OPENSEARCH=http://127.0.0.1:9200
//	PDTEST_OPENSEARCH=https://admin:secret@127.0.0.1:9200 PDTEST_OPENSEARCH_CA=root-ca.pem
//
// The cluster needs nori (`deploy/opensearch`).
package opensearchtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/server/opensearch"
)

// Config is the configuration of that cluster for one test: who it signs in
// as, the CA, and index names of the test's own, whose indexes are deleted
// when it ends.
func Config(t testing.TB) opensearch.Config {
	t.Helper()
	v := os.Getenv("PDTEST_OPENSEARCH")
	if v == "" {
		t.Skip("PDTEST_OPENSEARCH names no cluster to search on")
	}
	u, err := url.Parse(v)
	require.NoError(t, err)

	b := make([]byte, 4)
	_, _ = rand.Read(b)
	c := opensearch.Config{
		CaFile: os.Getenv("PDTEST_OPENSEARCH_CA"),
		Index:  "lofttest-" + hex.EncodeToString(b),
	}
	if u.User != nil {
		c.Username = u.User.Username()
		c.Password, _ = u.User.Password()
		u.User = nil
	}
	c.Addrs = []string{u.String()}

	t.Cleanup(func() {
		cl, err := opensearch.New(c, func(s string) (string, error) { return s, nil })
		if err != nil {
			t.Errorf("opensearch: %v", err)
			return
		}
		defer cl.Close()
		if err := cl.Remove(context.Background()); err != nil {
			t.Errorf("opensearch: %v", err)
		}
	})
	return c
}

// Made makes every index of c from db, as the loop does before its first
// pass: what a test that searches begins with.
func Made(t testing.TB, c *opensearch.Client, db *ent.Client) {
	t.Helper()
	for _, ix := range opensearch.Indexes {
		_, err := opensearch.Reindex(context.Background(), c, db, ix, time.Now(), nil)
		require.NoError(t, err, ix.Name)
	}
}
