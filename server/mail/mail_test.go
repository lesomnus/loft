package mail_test

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"mime"
	"net"
	"net/mail"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	rovemail "github.com/lesomnus/rove/server/mail"
)

// server is an SMTP server that takes whatever it is sent, as far as a client
// that speaks the protocol needs it to: EHLO, AUTH PLAIN, MAIL, RCPT, DATA.
type server struct {
	addr string

	mu   sync.Mutex
	auth string
	from string
	to   []string
	data string
}

func newServer(t *testing.T) *server {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { l.Close() })
	s := &server{addr: l.Addr().String()}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s
}

func (s *server) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	say := func(line string) { io.WriteString(c, line+"\r\n") }
	say("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
		s.mu.Lock()
		switch verb {
		case "EHLO", "HELO":
			say("250-fake")
			say("250 AUTH PLAIN")
		case "AUTH":
			s.auth = line
			say("235 ok")
		case "MAIL":
			s.from = line
			say("250 ok")
		case "RCPT":
			s.to = append(s.to, line)
			say("250 ok")
		case "DATA":
			say("354 go on")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil || l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			s.data = b.String()
			say("250 kept")
		case "QUIT":
			say("221 bye")
			s.mu.Unlock()
			return
		default:
			say("250 ok")
		}
		s.mu.Unlock()
	}
}

func TestAMessageIsSentAsTheConfigurationSays(t *testing.T) {
	x := require.New(t)
	s := newServer(t)
	host, port, err := net.SplitHostPort(s.addr)
	x.NoError(err)
	p := 0
	for _, r := range port {
		p = p*10 + int(r-'0')
	}

	t.Setenv("ROVE_TEST_MAIL_PASSWORD", "the password")
	sender, err := rovemail.New(rovemail.Config{
		Host: host, Port: p, Username: "rove", Password: "env:ROVE_TEST_MAIL_PASSWORD",
		From: "Rove <no-reply@rove.example>", Tls: "none",
	}, func(ref string) (string, error) { return "the password", nil }, nil)
	x.NoError(err)

	body := "김민수님이 Rove의 연구실 워크스페이스에 구성원으로 초대했습니다.\n\nhttps://rove.example/invitations?code=abc\n"
	x.NoError(sender.Send(context.Background(), rovemail.Message{
		To: "kim@acme.example", Subject: "김민수님이 Rove의 연구실에 초대했습니다", Body: body,
	}))

	s.mu.Lock()
	defer s.mu.Unlock()
	x.Contains(s.from, "<no-reply@rove.example>")
	x.Equal([]string{"RCPT TO:<kim@acme.example>"}, s.to)
	plain, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s.auth, "AUTH PLAIN "))
	x.NoError(err)
	x.Equal("\x00rove\x00the password", string(plain))

	m, err := mail.ReadMessage(strings.NewReader(s.data))
	x.NoError(err)
	subject, err := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	x.NoError(err)
	x.Equal("김민수님이 Rove의 연구실에 초대했습니다", subject)
	x.Equal("text/plain; charset=UTF-8", m.Header.Get("Content-Type"))
	raw, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, m.Body))
	x.NoError(err)
	x.Equal(body, string(raw))
}

// What cannot work is said when the configuration is read, and not at the
// first invitation.
func TestAConfigurationThatCannotSendIsRefused(t *testing.T) {
	x := require.New(t)
	none := func(string) (string, error) { return "", nil }
	_, err := rovemail.New(rovemail.Config{Host: "smtp.example", From: "not an address"}, none, nil)
	x.ErrorContains(err, "app.mail.from")
	_, err = rovemail.New(rovemail.Config{Host: "smtp.example", From: "a@b.example", Tls: "sometimes"}, none, nil)
	x.ErrorContains(err, "app.mail.tls")

	// And no server is no mail, which is not a mistake.
	s, err := rovemail.New(rovemail.Config{}, none, nil)
	x.NoError(err)
	x.IsType(rovemail.Log{}, s)
}

// A server that would be talked to in the clear when TLS was asked for is not.
func TestStartTlsIsNotSkipped(t *testing.T) {
	x := require.New(t)
	s := newServer(t)
	host, port, _ := net.SplitHostPort(s.addr)
	p := 0
	for _, r := range port {
		p = p*10 + int(r-'0')
	}
	sender, err := rovemail.New(rovemail.Config{Host: host, Port: p, From: "a@b.example"}, nil, nil)
	x.NoError(err)
	err = sender.Send(context.Background(), rovemail.Message{To: "kim@acme.example", Subject: "s", Body: "b"})
	x.ErrorContains(err, "STARTTLS")
	s.mu.Lock()
	defer s.mu.Unlock()
	x.Empty(s.to, "a message went out over a connection that was never protected")
}
