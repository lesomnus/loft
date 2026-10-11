package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// connectConn is a connection to a deployment through the address a browser
// reaches it at, speaking the Connect protocol: what the page speaks, served
// by the same transcoder in front of the same gRPC server (payday `web`).
//
// It is what the terminal's commands use, rather than dialing gRPC, because
// the browser's address is the one a deployment certainly serves -- behind a
// proxy, under the app's own TLS -- and a gRPC port is one it may not expose
// at all. It speaks protobuf on the wire, so nothing here reads JSON a field
// at a time.
//
// Unary calls and server streams, which is every shape the API has.
type connectConn struct {
	base   string
	client *http.Client
	// header is sent with every call: the session cookie, the workspace.
	header http.Header
}

var _ grpc.ClientConnInterface = (*connectConn)(nil)

func (c *connectConn) request(ctx context.Context, method, contentType string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+method, body)
	if err != nil {
		return nil, err
	}
	for k, vs := range c.header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if md, ok := metadata.FromOutgoingContext(ctx); ok {
		for k, vs := range md {
			for _, v := range vs {
				req.Header.Add(k, v)
			}
		}
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Connect-Protocol-Version", "1")
	return req, nil
}

func (c *connectConn) Invoke(ctx context.Context, method string, args, reply any, _ ...grpc.CallOption) error {
	in, ok := args.(proto.Message)
	if !ok {
		return status.Errorf(codes.Internal, "connect: %T is not a message", args)
	}
	out, ok := reply.(proto.Message)
	if !ok {
		return status.Errorf(codes.Internal, "connect: %T is not a message", reply)
	}
	body, err := proto.Marshal(in)
	if err != nil {
		return status.Errorf(codes.Internal, "connect: %v", err)
	}
	req, err := c.request(ctx, method, "application/proto", bytes.NewReader(body))
	if err != nil {
		return status.Errorf(codes.Internal, "connect: %v", err)
	}
	res, err := c.client.Do(req)
	if err != nil {
		return status.Errorf(codes.Unavailable, "%v", err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return status.Errorf(codes.Unavailable, "%v", err)
	}
	if res.StatusCode != http.StatusOK {
		return errorOf(res.StatusCode, data)
	}
	if err := proto.Unmarshal(data, out); err != nil {
		return status.Errorf(codes.Internal, "connect: the answer: %v", err)
	}
	return nil
}

func (c *connectConn) NewStream(ctx context.Context, desc *grpc.StreamDesc, method string, _ ...grpc.CallOption) (grpc.ClientStream, error) {
	if desc.ClientStreams {
		return nil, status.Error(codes.Unimplemented, "connect: nothing here sends a stream")
	}
	return &serverStream{ctx: ctx, c: c, method: method}, nil
}

// serverStream is one server stream: the request goes out when it is closed
// for sending, and every message after that is one envelope of the answer.
type serverStream struct {
	ctx    context.Context
	c      *connectConn
	method string

	req  []byte
	res  *http.Response
	body *bufio.Reader
	done error
}

func (s *serverStream) Header() (metadata.MD, error) { return metadata.MD{}, nil }
func (s *serverStream) Trailer() metadata.MD         { return metadata.MD{} }
func (s *serverStream) Context() context.Context     { return s.ctx }

func (s *serverStream) SendMsg(m any) error {
	v, ok := m.(proto.Message)
	if !ok {
		return status.Errorf(codes.Internal, "connect: %T is not a message", m)
	}
	b, err := proto.Marshal(v)
	if err != nil {
		return status.Errorf(codes.Internal, "connect: %v", err)
	}
	s.req = b
	return nil
}

func (s *serverStream) CloseSend() error {
	env := make([]byte, 5+len(s.req))
	binary.BigEndian.PutUint32(env[1:5], uint32(len(s.req)))
	copy(env[5:], s.req)
	req, err := s.c.request(s.ctx, s.method, "application/connect+proto", bytes.NewReader(env))
	if err != nil {
		return status.Errorf(codes.Internal, "connect: %v", err)
	}
	res, err := s.c.client.Do(req)
	if err != nil {
		return status.Errorf(codes.Unavailable, "%v", err)
	}
	if res.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		return errorOf(res.StatusCode, data)
	}
	s.res = res
	s.body = bufio.NewReader(res.Body)
	return nil
}

func (s *serverStream) RecvMsg(m any) error {
	if s.done != nil {
		return s.done
	}
	if s.body == nil {
		if err := s.CloseSend(); err != nil {
			s.done = err
			return err
		}
	}
	out, ok := m.(proto.Message)
	if !ok {
		return status.Errorf(codes.Internal, "connect: %T is not a message", m)
	}
	head := make([]byte, 5)
	if _, err := io.ReadFull(s.body, head); err != nil {
		s.finish(status.Errorf(codes.Unavailable, "connect: the stream ended without saying so: %v", err))
		return s.done
	}
	n := binary.BigEndian.Uint32(head[1:5])
	if n > 64<<20 {
		s.finish(status.Error(codes.ResourceExhausted, "connect: a message too large"))
		return s.done
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(s.body, data); err != nil {
		s.finish(status.Errorf(codes.Unavailable, "connect: %v", err))
		return s.done
	}
	if head[0]&0x02 != 0 {
		// The end of the stream, and how it ended.
		var end struct {
			Error *connectError `json:"error"`
		}
		_ = json.Unmarshal(data, &end)
		if end.Error != nil {
			s.finish(end.Error.status())
		} else {
			s.finish(io.EOF)
		}
		return s.done
	}
	if err := proto.Unmarshal(data, out); err != nil {
		s.finish(status.Errorf(codes.Internal, "connect: %v", err))
		return s.done
	}
	return nil
}

func (s *serverStream) finish(err error) {
	s.done = err
	if s.res != nil {
		s.res.Body.Close()
	}
}

// connectError is an error as the Connect protocol writes one.
type connectError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *connectError) status() error {
	return status.Error(codeOf(e.Code), e.Message)
}

// errorOf is a failed unary answer as the status it says.
func errorOf(httpStatus int, body []byte) error {
	var e connectError
	if err := json.Unmarshal(body, &e); err == nil && e.Code != "" {
		return e.status()
	}
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = http.StatusText(httpStatus)
	}
	switch httpStatus {
	case http.StatusUnauthorized:
		return status.Error(codes.Unauthenticated, msg)
	case http.StatusForbidden:
		return status.Error(codes.PermissionDenied, msg)
	case http.StatusNotFound:
		return status.Error(codes.Unimplemented, fmt.Sprintf("%s (is this a Rove deployment?)", msg))
	case http.StatusTooManyRequests:
		return status.Error(codes.ResourceExhausted, msg)
	case http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout:
		return status.Error(codes.Unavailable, msg)
	}
	return status.Error(codes.Unknown, msg)
}

var connectCodes = map[string]codes.Code{
	"canceled":            codes.Canceled,
	"unknown":             codes.Unknown,
	"invalid_argument":    codes.InvalidArgument,
	"deadline_exceeded":   codes.DeadlineExceeded,
	"not_found":           codes.NotFound,
	"already_exists":      codes.AlreadyExists,
	"permission_denied":   codes.PermissionDenied,
	"resource_exhausted":  codes.ResourceExhausted,
	"failed_precondition": codes.FailedPrecondition,
	"aborted":             codes.Aborted,
	"out_of_range":        codes.OutOfRange,
	"unimplemented":       codes.Unimplemented,
	"internal":            codes.Internal,
	"unavailable":         codes.Unavailable,
	"data_loss":           codes.DataLoss,
	"unauthenticated":     codes.Unauthenticated,
}

func codeOf(v string) codes.Code {
	if c, ok := connectCodes[v]; ok {
		return c
	}
	return codes.Unknown
}

var errNoLogin = errors.New("not signed in: run `rove login --url <the app's address>`")
