// Package storage keeps attachment files and hands out short-lived signed
// addresses for them (design 9.2, D25).
//
// A file is written once under a key the app makes up and is never read back
// by the browser through the API: the API answers a signed URL, and the HTTP
// handler checks the signature and streams the file. That keeps file bytes
// out of gRPC answers and lets a browser cache and range-request them.
package storage

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Store is where file bytes live. The local filesystem is the first one; an
// S3-compatible store is the next, behind the same three methods.
type Store interface {
	Put(ctx context.Context, key string, r io.Reader) (int64, error)
	Open(ctx context.Context, key string) (io.ReadSeekCloser, error)
	Delete(ctx context.Context, key string) error
}

// ErrNoFile is a key nothing was stored under.
var ErrNoFile = errors.New("storage: no such file")

// Dir is a Store on the local filesystem.
type Dir string

func (d Dir) path(key string) (string, error) {
	if key == "" || strings.Contains(key, "..") || strings.HasPrefix(key, "/") {
		return "", fmt.Errorf("storage: %q is not a key", key)
	}
	return filepath.Join(string(d), filepath.FromSlash(key)), nil
}

func (d Dir) Put(_ context.Context, key string, r io.Reader) (int64, error) {
	p, err := d.path(key)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return 0, err
	}

	// Written beside itself and renamed, so a reader never sees half a file.
	tmp := p + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, r)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return 0, err
	}

	return n, os.Rename(tmp, p)
}

func (d Dir) Open(_ context.Context, key string) (io.ReadSeekCloser, error) {
	p, err := d.path(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoFile
	}
	return f, err
}

func (d Dir) Delete(_ context.Context, key string) error {
	p, err := d.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Signer makes and checks download addresses.
type Signer struct {
	Key  []byte
	Base string // where the HTTP listener answers, such as http://localhost:8080
	TTL  time.Duration
}

func (s Signer) mac(key string, exp int64, name string) string {
	m := hmac.New(sha256.New, s.Key)
	fmt.Fprintf(m, "%s\n%d\n%s", key, exp, name)
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// URL answers an address that serves `key` until it expires, under a file
// name for the browser to save it as.
func (s Signer) URL(key, name string, now time.Time) (string, time.Time) {
	ttl := s.TTL
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	exp := now.Add(ttl)
	v := fmt.Sprintf("%s/files/%s?e=%d&n=%s&s=%s",
		strings.TrimSuffix(s.Base, "/"),
		key,
		exp.Unix(),
		urlEscape(name),
		s.mac(key, exp.Unix(), name))
	return v, exp
}

// Handler serves what [Signer.URL] signed, and nothing else.
func (s Signer) Handler(files Store, now func() time.Time) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/files/")
		q := r.URL.Query()
		exp, err := strconv.ParseInt(q.Get("e"), 10, 64)
		if err != nil || now().Unix() > exp {
			http.Error(w, "this link has expired", http.StatusGone)
			return
		}
		name := q.Get("n")
		if !hmac.Equal([]byte(q.Get("s")), []byte(s.mac(key, exp, name))) {
			http.Error(w, "not a link this server made", http.StatusForbidden)
			return
		}

		f, err := files.Open(r.Context(), key)
		if err != nil {
			http.Error(w, "no such file", http.StatusNotFound)
			return
		}
		defer f.Close()

		// A file somebody uploaded is served from this origin, so it is never
		// allowed to be a page: nothing is sniffed into HTML, and whatever a
		// browser does render runs sandboxed with nothing to load.
		h := w.Header()
		h.Set("Cache-Control", "private, max-age=600")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'; sandbox")
		disposition := "attachment"
		if inline(name) {
			disposition = "inline"
		}
		h.Set("Content-Disposition", disposition+"; filename*=UTF-8''"+urlEscape(name))
		http.ServeContent(w, r, name, time.Time{}, f)
	})
}

func urlEscape(v string) string {
	b := strings.Builder{}
	for _, c := range []byte(v) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// inline answers whether a file is shown in the browser rather than saved:
// pictures and documents a browser shows without running anything.
func inline(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".avif", ".heic", ".pdf", ".txt", ".mp4", ".webm":
		return true
	}
	return false
}
