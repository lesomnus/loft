package session_test

import (
	"crypto/sha256"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/loft/server/session"
)

// TestAKeyIsKeptUnderTheSigningKey: what a key is kept as is the deployment's
// signing key's, through a key of its own (HKDF): the same key is kept the
// same under the same signing key, otherwise under another -- which is why
// rotating `app.signing_key` signs everybody out -- and never as its SHA-256.
// A terminal's SHA-256 of its key is kept as the key itself would be.
func TestAKeyIsKeptUnderTheSigningKey(t *testing.T) {
	x := require.New(t)
	a := session.Store{Key: session.KeyFrom([]byte("one deployment's signing key"))}
	b := session.Store{Key: session.KeyFrom([]byte("another deployment's signing key"))}
	again := session.Store{Key: session.KeyFrom([]byte("one deployment's signing key"))}

	x.Len(a.Key, sha256.Size)
	x.NotEqual([]byte("one deployment's signing key"), a.Key, "a key of its own, and not the signing key")
	x.Equal(a.Kept("a session's key"), again.Kept("a session's key"))
	x.NotEqual(a.Kept("a session's key"), b.Kept("a session's key"), "under another signing key, another")
	sum := sha256.Sum256([]byte("a session's key"))
	x.NotEqual(sum[:], a.Kept("a session's key"))
	x.Equal(a.Kept("a session's key"), a.KeptSum(sum[:]), "a terminal's, kept from what it sent")
	x.NotEqual(a.Kept("a session's key"), a.Kept("another key"))
}
