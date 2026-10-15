// Package opensearch is what answers a search when a deployment names a
// search engine (D31, design 9.5): OpenSearch, with nori, the Korean analyzer
// Lucene ships. nori splits what was written and what a person types into
// the words they are made of -- 웹서버 into 웹 and 서버, 의자를 into 의자 --
// which is what a search of Korean names needs and a database's LIKE cannot
// do.
//
// It is the engine's HTTP API and no client library, since what this asks of
// it is a handful of calls: make an index, point an alias at it, write
// documents in bulk, and ask. A deployment that names no engine
// (`app.search.opensearch.addrs`) searches its database (`internal/search`).
//
// What the index holds is a copy. The database is what is true, every
// document can be made again from it (`rove search reindex`), and the index
// is never asked whether a caller may see a row: a search answers
// identifiers, and the rows are read through the wall like any other.
package opensearch

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

// Config is the cluster a deployment's search is answered from.
type Config struct {
	// Addrs are its nodes, each as `https://host:9200`. A request that one of
	// them does not answer goes to the next. Empty is no cluster, and the
	// search is the database's.
	Addrs []string `yaml:"addrs"`
	// Username and Password sign in to it, with the security plugin's basic
	// authentication; Password is `env:NAME`, `file:PATH`, or the password
	// itself. Empty signs in as nobody, which is a cluster without the
	// security plugin: a laptop.
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	// CaFile is the CA the nodes' certificates chain to; empty is the
	// system pool.
	CaFile string `yaml:"ca_file"`
	// Index is what the names of this deployment's indexes begin with: the
	// alias `<index>-assets`, and the index it points at,
	// `<index>-assets-<when it was made>`. Unset is `rove`. A cluster that
	// several deployments share gives each one its own.
	Index string `yaml:"index"`
}

// On says whether c names a cluster.
func (c Config) On() bool { return len(c.Addrs) > 0 }

// DefaultIndex is what index names begin with when the configuration says
// nothing.
const DefaultIndex = "rove"

// indexName is what OpenSearch takes as the start of an index name: lower
// case, and nothing it would refuse or read as a pattern.
var indexName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// Client is a deployment's connection to its cluster.
type Client struct {
	nodes  []*url.URL
	user   string
	pass   string
	prefix string
	http   *http.Client

	// next is the node a request tries first: the one after the last that
	// did not answer, so that a node which is down is not every request's
	// first try.
	next atomic.Int64
}

// New is a client of the cluster c names, or nil -- and no error -- when it
// names none. resolve reads the password's reference: `identity.ResolveRef`.
func New(c Config, resolve func(string) (string, error)) (*Client, error) {
	if !c.On() {
		return nil, nil
	}

	nodes := make([]*url.URL, 0, len(c.Addrs))
	for _, v := range c.Addrs {
		u, err := url.Parse(strings.TrimSpace(v))
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return nil, fmt.Errorf("app.search.opensearch.addrs: %q is not a node's address such as https://search-0:9200", v)
		}
		if u.User != nil {
			return nil, fmt.Errorf("app.search.opensearch.addrs: %q signs in in the address; say who in username and password", u.Redacted())
		}
		u.Path = strings.TrimSuffix(u.Path, "/")
		nodes = append(nodes, u)
	}

	prefix := c.Index
	if prefix == "" {
		prefix = DefaultIndex
	}
	if !indexName.MatchString(prefix) {
		return nil, fmt.Errorf("app.search.opensearch.index: %q is not the start of an index name: lower case letters, digits, '.', '_' and '-', from a letter or a digit", prefix)
	}

	pass := ""
	if c.Password != "" {
		v, err := resolve(c.Password)
		if err != nil {
			return nil, fmt.Errorf("app.search.opensearch.password: %w", err)
		}
		pass = v
	}

	tc := &tls.Config{MinVersion: tls.VersionTLS12}
	if c.CaFile != "" {
		b, err := os.ReadFile(c.CaFile)
		if err != nil {
			return nil, fmt.Errorf("app.search.opensearch.ca_file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(b) {
			return nil, fmt.Errorf("app.search.opensearch.ca_file: %s holds no certificate", c.CaFile)
		}
		tc.RootCAs = pool
	}

	// How long a node may take is said per request, by its context: a bulk
	// write of a reindex takes longer than a search should. What is bounded
	// here is a node that does not answer at all.
	t := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:       tc,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 2 * time.Minute,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   16,
		ForceAttemptHTTP2:     true,
	}

	return &Client{
		nodes:  nodes,
		user:   c.Username,
		pass:   pass,
		prefix: prefix,
		http:   &http.Client{Transport: t},
	}, nil
}

// Close lets go of the connections the client keeps; a nil client, which is
// no cluster, has none.
func (c *Client) Close() {
	if c != nil {
		c.http.CloseIdleConnections()
	}
}

// Error is a request the cluster answered and refused.
type Error struct {
	Status int
	// Type is what OpenSearch calls it, such as
	// `resource_already_exists_exception`; empty when the answer did not
	// say.
	Type   string
	Reason string
}

func (e *Error) Error() string {
	if e.Type == "" {
		return fmt.Sprintf("opensearch: %d: %s", e.Status, e.Reason)
	}
	return fmt.Sprintf("opensearch: %d %s: %s", e.Status, e.Type, e.Reason)
}

// IsNotFound says whether err is the cluster answering that what was named is
// not there.
func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

// errorOf is the refusal in an answer's body, which OpenSearch writes either
// as an object or, for a few calls, as a sentence.
func errorOf(status int, body []byte) *Error {
	var v struct {
		Error json.RawMessage `json:"error"`
	}
	e := &Error{Status: status}
	if json.Unmarshal(body, &v) != nil || len(v.Error) == 0 {
		e.Reason = strings.TrimSpace(string(body))
		return e
	}
	var o struct {
		Type   string `json:"type"`
		Reason string `json:"reason"`
	}
	if json.Unmarshal(v.Error, &o) == nil {
		e.Type, e.Reason = o.Type, o.Reason
		return e
	}
	var s string
	if json.Unmarshal(v.Error, &s) == nil {
		e.Reason = s
		return e
	}
	e.Reason = string(v.Error)
	return e
}

// do sends one request -- a JSON body, or the NDJSON a bulk write is, by
// ctype -- and decodes the answer into out when there is one to decode.
//
// A node that does not answer is passed over for the next one, and every node
// is tried once. A node that answers with a refusal is not: the next one
// would refuse the same, and what the cluster said is the error.
func (c *Client) do(ctx context.Context, method, path string, body []byte, ctype string, out any) error {
	first := int(c.next.Load())
	var last error
	for i := range c.nodes {
		n := (first + i) % len(c.nodes)

		var r io.Reader
		if body != nil {
			r = bytes.NewReader(body)
		}
		// The path as written, escaped where it has to be, so that a pattern
		// such as `rove-assets-*` reaches the node as one.
		req, err := http.NewRequestWithContext(ctx, method, c.nodes[n].String()+path, r)
		if err != nil {
			return err
		}
		if body != nil {
			req.Header.Set("Content-Type", ctype)
		}
		req.Header.Set("Accept", "application/json")
		if c.user != "" {
			req.SetBasicAuth(c.user, c.pass)
		}

		res, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			last = fmt.Errorf("opensearch %s: %w", c.nodes[n].Host, err)
			c.next.Store(int64((n + 1) % len(c.nodes)))
			continue
		}
		b, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			return fmt.Errorf("opensearch %s: %w", c.nodes[n].Host, err)
		}
		if res.StatusCode >= 300 {
			return errorOf(res.StatusCode, b)
		}
		if out == nil || len(b) == 0 {
			return nil
		}
		if err := json.Unmarshal(b, out); err != nil {
			p, _, _ := strings.Cut(path, "?")
			return fmt.Errorf("opensearch %s: %s %s answered what is not JSON: %w", c.nodes[n].Host, method, p, err)
		}
		return nil
	}
	return last
}

// call is a request with a JSON body, or none when in is nil.
func (c *Client) call(ctx context.Context, method, path string, in, out any) error {
	var body []byte
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = b
	}
	return c.do(ctx, method, path, body, "application/json", out)
}
