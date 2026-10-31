package offboard

import (
	"archive/zip"
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/lesomnus/payday/trail"
)

// Format is what an export says it is, in its manifest. A restore reads every
// format it knows, this one and the ones before it: an export is a contract
// with whoever kept it (design D34).
const Format = "loft-export/1"

// The names in an export that are not in its digests: the manifest that lists
// them, and the signature over the manifest.
const (
	manifestName  = "manifest.json"
	signatureName = "signature.json"
)

// Signature is what the deployment's key said of an export's manifest: the
// public half of the key, PKIX and base64, and its signature over the
// manifest's bytes ([statement]), base64.
//
// The key is the trail's (`audit.checkpoints.key`): the one a deployment
// already keeps where its database credentials are not, and the one its
// operators already know how to trust and to rotate (`audit.checkpoints.trust`).
type Signature struct {
	Key string `json:"key"`
	Sig string `json:"signature"`
}

var (
	// ErrUnsigned is an export with no signature: one written by a deployment
	// with no key, which nothing can say has not been changed since.
	ErrUnsigned = errors.New("the export is not signed")

	// ErrUntrusted is an export signed by a key nobody said to believe.
	ErrUntrusted = errors.New("the export is signed by a key that is not trusted")

	// ErrForged is a manifest its signature is not over.
	ErrForged = errors.New("the export's manifest is not what its key signed")

	// ErrAltered is a file that is not what the manifest says it is, or one
	// it does not list, or one it lists that is not there.
	ErrAltered = errors.New("the export is not what its manifest says")

	// ErrFormat is an export of a format this build does not read.
	ErrFormat = errors.New("the export is of a format this build does not read")
)

// statement is the bytes a manifest's signature is over: the manifest, after
// a line that says what is signed. The same key signs the trail's checkpoints,
// and a statement that is never also a checkpoint cannot be passed off as one.
func statement(manifest []byte) []byte {
	return append([]byte("loft export manifest\n"), manifest...)
}

// sign signs `b` the way the trail signs a checkpoint: Ed25519 over the bytes
// themselves, ECDSA and RSA over their SHA-256 -- what a device is asked for.
func sign(k crypto.Signer, b []byte) (Signature, error) {
	pub := k.Public()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return Signature{}, err
	}

	var sig []byte
	if _, ok := pub.(ed25519.PublicKey); ok {
		sig, err = k.Sign(rand.Reader, b, crypto.Hash(0))
	} else {
		d := sha256.Sum256(b)
		sig, err = k.Sign(rand.Reader, d[:], crypto.SHA256)
	}
	if err != nil {
		return Signature{}, err
	}

	return Signature{
		Key: base64.StdEncoding.EncodeToString(der),
		Sig: base64.StdEncoding.EncodeToString(sig),
	}, nil
}

// verify answers whether `s` is a signature over `b` by one of `trust`.
func verify(s Signature, b []byte, trust []crypto.PublicKey) error {
	der, err := base64.StdEncoding.DecodeString(s.Key)
	if err != nil {
		return fmt.Errorf("%w: its key does not read: %v", ErrForged, err)
	}
	sig, err := base64.StdEncoding.DecodeString(s.Sig)
	if err != nil {
		return fmt.Errorf("%w: its signature does not read: %v", ErrForged, err)
	}
	if !slices.ContainsFunc(trust, func(k crypto.PublicKey) bool {
		v, err := x509.MarshalPKIXPublicKey(k)
		return err == nil && bytes.Equal(v, der)
	}) {
		return ErrUntrusted
	}

	k, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return fmt.Errorf("%w: its key does not read: %v", ErrForged, err)
	}
	ok := false
	switch k := k.(type) {
	case ed25519.PublicKey:
		ok = ed25519.Verify(k, b, sig)
	case *ecdsa.PublicKey:
		d := sha256.Sum256(b)
		ok = ecdsa.VerifyASN1(k, d[:], sig)
	case *rsa.PublicKey:
		// PKCS #1 v1.5 is what a signer is asked for, and PSS what a device
		// whose key allows nothing else answers with instead.
		d := sha256.Sum256(b)
		ok = rsa.VerifyPKCS1v15(k, crypto.SHA256, d[:], sig) == nil ||
			rsa.VerifyPSS(k, crypto.SHA256, d[:], sig, nil) == nil
	}
	if !ok {
		return ErrForged
	}

	return nil
}

// Trusted is the keys an export of this deployment's is believed by: the one
// it signs with, and the ones its operators said to trust besides -- a key
// rotated out, or another deployment's, for an export that came from there.
func (d Deployment) Trusted() []crypto.PublicKey { return TrustedBy(d.Trail) }

// TrustedBy is [Deployment.Trusted] of a trail's policy, which is where the
// keys are configured (`audit.checkpoints.key` and `.trust`).
func TrustedBy(p trail.Policy) []crypto.PublicKey {
	keys := []crypto.PublicKey{}
	if p.Signer != nil {
		keys = append(keys, p.Signer.Public())
	}

	return append(keys, p.Trust...)
}

// Verify reads an export and answers its manifest when it is what one of
// `trust` signed and every file in it is what the manifest says -- nothing
// changed, nothing added and nothing taken out. An export that is not answers
// why ([ErrUnsigned], [ErrUntrusted], [ErrForged], [ErrAltered], [ErrFormat]).
//
// Nothing in an export is to be believed before this answers: a restore of a
// file anybody could have edited is a history anybody could have written.
func Verify(r io.ReaderAt, size int64, trust []crypto.PublicKey) (Manifest, error) {
	var m Manifest
	z, err := zip.NewReader(r, size)
	if err != nil {
		return m, fmt.Errorf("%w: not a zip archive: %v", ErrAltered, err)
	}

	read := func(name string) ([]byte, bool, error) {
		f, err := z.Open(name)
		if err != nil {
			return nil, false, nil
		}
		defer f.Close()
		b, err := io.ReadAll(f)
		return b, true, err
	}

	mb, ok, err := read(manifestName)
	if err != nil {
		return m, err
	}
	if !ok {
		return m, fmt.Errorf("%w: it has no %s", ErrAltered, manifestName)
	}
	sb, ok, err := read(signatureName)
	if err != nil {
		return m, err
	}
	if !ok {
		return m, ErrUnsigned
	}
	var s Signature
	if err := json.Unmarshal(sb, &s); err != nil {
		return m, fmt.Errorf("%w: its signature does not read: %v", ErrForged, err)
	}
	if err := verify(s, statement(mb), trust); err != nil {
		return m, err
	}

	// Believed from here: the key signed these bytes.
	if err := json.Unmarshal(mb, &m); err != nil {
		return m, fmt.Errorf("%w: its manifest does not read: %v", ErrFormat, err)
	}
	if m.Format != Format {
		return m, fmt.Errorf("%w: %q", ErrFormat, m.Format)
	}

	seen := map[string]bool{}
	for _, f := range z.File {
		if f.Name == manifestName || f.Name == signatureName {
			continue
		}
		want, ok := m.Digests[f.Name]
		if !ok {
			return m, fmt.Errorf("%w: %s is not in its manifest", ErrAltered, f.Name)
		}
		if seen[f.Name] {
			return m, fmt.Errorf("%w: %s is in it twice", ErrAltered, f.Name)
		}
		seen[f.Name] = true

		rc, err := f.Open()
		if err != nil {
			return m, fmt.Errorf("%w: %s: %v", ErrAltered, f.Name, err)
		}
		h := sha256.New()
		_, err = io.Copy(h, rc)
		rc.Close()
		if err != nil {
			return m, fmt.Errorf("%w: %s: %v", ErrAltered, f.Name, err)
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != want {
			return m, fmt.Errorf("%w: %s is not what was exported", ErrAltered, f.Name)
		}
	}
	for name := range m.Digests {
		if !seen[name] {
			return m, fmt.Errorf("%w: %s is not in it", ErrAltered, name)
		}
	}

	return m, nil
}

// Fingerprints names an export and the key it is signed by, as a restore
// records them and `loft tenant verify` prints them: the SHA-256 of its
// manifest, which holds the digest of every other file in it, and of the
// public key its signature is by -- each `sha256:` and hex. Whoever keeps the
// export can show by them that it is the one a workspace was restored from,
// and the trail in it what the deployment it came from wrote (design D34).
//
// It believes nothing: [Verify] is what says the export is what its key
// signed.
func Fingerprints(r io.ReaderAt, size int64) (manifest, key string, err error) {
	z, err := zip.NewReader(r, size)
	if err != nil {
		return "", "", fmt.Errorf("%w: not a zip archive: %v", ErrAltered, err)
	}
	read := func(name string) ([]byte, error) {
		f, err := z.Open(name)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return io.ReadAll(f)
	}

	mb, err := read(manifestName)
	if err != nil {
		return "", "", fmt.Errorf("%w: %s: %v", ErrAltered, manifestName, err)
	}
	sb, err := read(signatureName)
	if err != nil {
		return "", "", ErrUnsigned
	}
	var s Signature
	if err := json.Unmarshal(sb, &s); err != nil {
		return "", "", fmt.Errorf("%w: its signature does not read: %v", ErrForged, err)
	}
	der, err := base64.StdEncoding.DecodeString(s.Key)
	if err != nil {
		return "", "", fmt.Errorf("%w: its key does not read: %v", ErrForged, err)
	}

	m, k := sha256.Sum256(mb), sha256.Sum256(der)
	return "sha256:" + hex.EncodeToString(m[:]), "sha256:" + hex.EncodeToString(k[:]), nil
}
