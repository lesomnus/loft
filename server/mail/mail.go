// Package mail sends what this deployment writes to somebody's mailbox: an
// invitation, for now (design 9.11).
//
// It is SMTP and nothing more particular, so which service a deployment sends
// through -- every one of them speaks it -- is configuration and not code
// (`app.mail`). With no server named it sends nothing and writes the message
// to the log instead, which is a checkout, and a deployment that has not
// chosen one yet.
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Config is the SMTP server this deployment sends through.
type Config struct {
	// Host is the server; empty sends nothing and logs each message instead.
	Host string `yaml:"host"`
	// Port is 587 when left out, and 465 for `tls: implicit`.
	Port int `yaml:"port"`
	// Username and Password sign in to it; Password is `env:NAME`,
	// `file:PATH`, or the password itself. Empty signs in as nobody.
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	// From is who a message is from: `Loft <no-reply@example.com>`.
	From string `yaml:"from"`
	// Tls is how the connection is protected: `starttls` (the default),
	// `implicit` for a server that speaks TLS from the first byte, or `none`,
	// which is a lab and sends the password in the clear.
	Tls string `yaml:"tls"`
}

// On says whether messages go to a server.
func (c Config) On() bool { return c.Host != "" }

// Message is one mail, in plain text.
type Message struct {
	To      string
	Subject string
	Body    string
}

// Sender sends a message.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// New is the sender a configuration says. `secret` reads the password's
// reference, the way every secret of this deployment is read.
func New(c Config, secret func(string) (string, error), log *slog.Logger) (Sender, error) {
	if !c.On() {
		return Log{log}, nil
	}
	from, err := mail.ParseAddress(c.From)
	if err != nil {
		return nil, fmt.Errorf("app.mail.from: %q is not an address to send from: %w", c.From, err)
	}
	tlsMode := strings.ToLower(c.Tls)
	switch tlsMode {
	case "":
		tlsMode = "starttls"
	case "starttls", "implicit", "none":
	default:
		return nil, fmt.Errorf("app.mail.tls: %q is none of starttls, implicit, none", c.Tls)
	}
	port := c.Port
	if port == 0 {
		port = 587
		if tlsMode == "implicit" {
			port = 465
		}
	}
	password := ""
	if c.Password != "" {
		if password, err = secret(c.Password); err != nil {
			return nil, fmt.Errorf("app.mail.password: %w", err)
		}
	}

	return &Smtp{host: c.Host, port: port, username: c.Username, password: password, from: from, tls: tlsMode}, nil
}

// Log is a sender that sends nothing and says what it would have sent.
type Log struct{ Logger *slog.Logger }

func (l Log) Send(ctx context.Context, m Message) error {
	lg := l.Logger
	if lg == nil {
		lg = slog.Default()
	}
	lg.InfoContext(ctx, "mail: not sent, since app.mail names no server", "to", m.To, "subject", m.Subject, "body", m.Body)
	return nil
}

// Smtp sends through an SMTP server.
type Smtp struct {
	host     string
	port     int
	username string
	password string
	from     *mail.Address
	tls      string
}

// timeout bounds one message, connection and all.
const timeout = 30 * time.Second

func (s *Smtp) Send(ctx context.Context, m Message) error {
	to, err := mail.ParseAddress(m.To)
	if err != nil {
		return fmt.Errorf("mail: %q is not an address: %w", m.To, err)
	}
	msg, err := compose(s.from, to, m)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	addr := net.JoinHostPort(s.host, strconv.Itoa(s.port))
	d := &net.Dialer{}
	var conn net.Conn
	if s.tls == "implicit" {
		conn, err = (&tls.Dialer{NetDialer: d, Config: &tls.Config{ServerName: s.host}}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("mail: %s: %w", addr, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	c, err := smtp.NewClient(conn, s.host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("mail: %s: %w", addr, err)
	}
	defer c.Close()

	if s.tls == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return fmt.Errorf("mail: %s offers no STARTTLS; say `tls: implicit` for a server that speaks TLS from the start", addr)
		}
		if err := c.StartTLS(&tls.Config{ServerName: s.host}); err != nil {
			return fmt.Errorf("mail: %s: %w", addr, err)
		}
	}
	if s.username != "" {
		// net/smtp refuses to send a password over a connection that is
		// neither TLS nor to this machine, which is the refusal wanted.
		if err := c.Auth(smtp.PlainAuth("", s.username, s.password, s.host)); err != nil {
			return fmt.Errorf("mail: %s: %w", addr, err)
		}
	}
	if err := c.Mail(s.from.Address); err != nil {
		return fmt.Errorf("mail: %s: %w", addr, err)
	}
	if err := c.Rcpt(to.Address); err != nil {
		return fmt.Errorf("mail: %s: %w", addr, err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("mail: %s: %w", addr, err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("mail: %s: %w", addr, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mail: %s: %w", addr, err)
	}
	return c.Quit()
}

// compose is the message as it is sent: headers in ASCII, the subject and the
// body in UTF-8 and base64, which no server in between rewrites.
func compose(from, to *mail.Address, m Message) ([]byte, error) {
	if strings.ContainsAny(m.Subject, "\r\n") {
		return nil, errors.New("mail: a subject is one line")
	}
	id := make([]byte, 12)
	if _, err := rand.Read(id); err != nil {
		return nil, err
	}
	_, domain, _ := strings.Cut(from.Address, "@")

	var b bytes.Buffer
	header := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	header("From", from.String())
	header("To", to.String())
	header("Subject", mime.BEncoding.Encode("UTF-8", m.Subject))
	header("Date", time.Now().Format(time.RFC1123Z))
	header("Message-ID", "<"+hex.EncodeToString(id)+"@"+domain+">")
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=UTF-8")
	header("Content-Transfer-Encoding", "base64")
	b.WriteString("\r\n")

	body := base64.StdEncoding.EncodeToString([]byte(m.Body))
	for len(body) > 76 {
		b.WriteString(body[:76])
		b.WriteString("\r\n")
		body = body[76:]
	}
	b.WriteString(body)
	b.WriteString("\r\n")
	return b.Bytes(), nil
}
