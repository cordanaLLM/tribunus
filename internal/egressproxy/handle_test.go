package egressproxy

import (
	"bytes"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// scriptedConn is a client connection that sends one request and fails where the test says.
type scriptedConn struct {
	net.Conn
	in  *strings.Reader
	out bytes.Buffer
	// deadlineErrs are the answers of SetDeadline, call by call; past its end the answer is nil.
	deadlineErrs []error
	// set are the deadlines it was given, in order.
	set              []time.Time
	writeDeadlineErr error
	// writeErr is the answer of every Write after the first goodWrites.
	writeErr   error
	goodWrites int
	writes     int
}

func (c *scriptedConn) Read(p []byte) (int, error) { return c.in.Read(p) }

func (c *scriptedConn) Write(p []byte) (int, error) {
	c.writes++
	if c.writeErr != nil && c.writes > c.goodWrites {
		return 0, c.writeErr
	}
	return c.out.Write(p)
}

func (c *scriptedConn) Close() error { return nil }

func (c *scriptedConn) SetDeadline(deadline time.Time) error {
	c.set = append(c.set, deadline)
	if len(c.set) <= len(c.deadlineErrs) {
		return c.deadlineErrs[len(c.set)-1]
	}
	return nil
}

func (c *scriptedConn) SetWriteDeadline(time.Time) error { return c.writeDeadlineErr }

// TestServerHandleFaultsOfTheClientConnection: what the server does when the connection of
// the client itself fails, at each place it can.
func TestServerHandleFaultsOfTheClientConnection(t *testing.T) {
	target := upstream(t)
	gone := errors.New("the client is gone")
	noDeadline := errors.New("no deadline")
	tests := []struct {
		name        string
		conn        scriptedConn
		target      string
		wantAnswer  string
		wantLog     string
		wantRefused int
		wantFault   string
	}{
		{"refusal that cannot be written", scriptedConn{writeErr: gone}, "other.example:443", "",
			`egress proxy: refused "other.example:443": other.example:443 is not a declared destination (the answer did not reach the client: the client is gone)` + "\n", 1, ""},
		{"refusal without a write deadline", scriptedConn{writeDeadlineErr: noDeadline}, "other.example:443", "",
			`egress proxy: refused "other.example:443": other.example:443 is not a declared destination (the answer did not reach the client: no deadline)` + "\n", 1, ""},
		{"no request deadline", scriptedConn{deadlineErrs: []error{noDeadline}}, target, "", "", 0, "egress proxy: request deadline: no deadline"},
		{"no tunnel deadline", scriptedConn{deadlineErrs: []error{nil, noDeadline}}, target, "HTTP/1.1 500 Internal Server Error\r\n",
			`egress proxy: refused "` + target + `": no deadline` + "\n", 1, ""},
		{"answer that cannot be written", scriptedConn{writeErr: gone}, target, "",
			"egress proxy: tunnel to " + target + ": answer the request: the client is gone\n", 0, ""},
	}
	for i := 0; i < len(tests); i++ {
		server := startServer(t, Options{Destinations: []string{target}})
		conn := &tests[i].conn
		conn.in = strings.NewReader(connectRequest(tests[i].target))
		server.handle(t.Context(), conn, time.Now().Add(testWait))
		if got := conn.out.String(); !strings.HasPrefix(got, tests[i].wantAnswer) || (tests[i].wantAnswer == "" && got != "") {
			t.Errorf("%s: answer %q, want %q", tests[i].name, got, tests[i].wantAnswer)
		}
		if got := server.log.String(); got != tests[i].wantLog {
			t.Errorf("%s: log %q, want %q", tests[i].name, got, tests[i].wantLog)
		}
		if got := server.Refused(); got != tests[i].wantRefused {
			t.Errorf("%s: Refused() = %d, want %d", tests[i].name, got, tests[i].wantRefused)
		}
		err := server.stop()
		if (err == nil) != (tests[i].wantFault == "") || (err != nil && err.Error() != tests[i].wantFault) {
			t.Errorf("%s: the server stopped with %v, want %q", tests[i].name, err, tests[i].wantFault)
		}
	}
}

// A request that ends inside its header lines is refused, not waited for.
func TestServerRefusesARequestThatEndsInItsHeader(t *testing.T) {
	server := startServer(t, Options{Destinations: []string{"gateway.example:443"}})
	tun := dialServer(t, server.Server)
	if _, err := io.WriteString(tun.conn, "CONNECT gateway.example:443 HTTP/1.1\r\nHost: gateway.example:443\r\n"); err != nil {
		t.Fatalf("write request = %v, want nil", err)
	}
	if got := finish(t, tun); !strings.HasPrefix(got, "HTTP/1.1 400 Bad Request\r\n") {
		t.Fatalf("answer = %q, want 400", got)
	}
	want := `egress proxy: refused "gateway.example:443": read the request: EOF` + "\n"
	if !waitFor(func() bool { return server.log.String() == want }) {
		t.Fatalf("log = %q, want %q", server.log.String(), want)
	}
	if server.Refused() != 1 {
		t.Errorf("Refused() = %d, want 1", server.Refused())
	}
}
