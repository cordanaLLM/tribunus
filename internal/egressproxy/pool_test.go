package egressproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// scriptedListener is a listener whose Accept answers what the test says for each call.
type scriptedListener struct {
	deadlineErr error
	accept      func(call int) (net.Conn, error)
	calls       int
	closes      atomic.Int64
}

func (l *scriptedListener) Accept() (net.Conn, error) {
	l.calls++
	return l.accept(l.calls)
}

func (l *scriptedListener) Close() error                { l.closes.Add(1); return nil }
func (l *scriptedListener) Addr() net.Addr              { return &net.UnixAddr{Name: "scripted", Net: "unix"} }
func (l *scriptedListener) SetDeadline(time.Time) error { return l.deadlineErr }

// idleConn is a connection nothing is read from or written to; closing it is all a pool
// does with a connection itself.
type idleConn struct{ net.Conn }

func (idleConn) Close() error { return nil }

func TestPoolKeepsAFixedNumberOfFaults(t *testing.T) {
	tests := []struct {
		faults int
		kept   int
	}{
		{0, 0},
		{1, 1},
		{maxFaults - 1, maxFaults - 1},
		{maxFaults, maxFaults},
		{maxFaults + 1, maxFaults},
		{4 * maxFaults, maxFaults},
	}
	for i := 0; i < len(tests); i++ {
		p := newPool()
		p.fault(nil)
		for n := 0; n < tests[i].faults; n++ {
			p.fault(fmt.Errorf("fault %d", n))
			p.fault(nil)
		}
		if len(p.faults) != tests[i].kept {
			t.Errorf("%d faults: %d kept, want %d", tests[i].faults, len(p.faults), tests[i].kept)
		}
		if tests[i].kept > 0 && (p.faults[0].Error() != "fault 0" || p.faults[tests[i].kept-1].Error() != fmt.Sprintf("fault %d", tests[i].kept-1)) {
			t.Errorf("%d faults: kept %v, want the first %d", tests[i].faults, p.faults, tests[i].kept)
		}
	}
}

func TestPoolServeEndsOfAccept(t *testing.T) {
	broken := errors.New("the listener broke")
	tests := []struct {
		name        string
		deadlineErr error
		acceptErr   error
		want        string
		wantCalls   int
	}{
		{"closed listener", nil, net.ErrClosed, "", 1},
		{"wrapped closed listener", nil, &net.OpError{Op: "accept", Net: "unix", Err: net.ErrClosed}, "", 1},
		{"deadline passed", nil, os.ErrDeadlineExceeded, "", 1},
		{"broken listener", nil, broken, "accept: the listener broke", 1},
		{"no deadline to set", errors.New("no deadline"), broken, "accept deadline: no deadline", 0},
	}
	for i := 0; i < len(tests); i++ {
		listener := &scriptedListener{
			deadlineErr: tests[i].deadlineErr,
			accept:      func(int) (net.Conn, error) { return nil, tests[i].acceptErr },
		}
		err := newPool().serve(context.Background(), listener, time.Now().Add(testWait), func(net.Conn) {}, func(net.Conn) {})
		if (err == nil) != (tests[i].want == "") || (err != nil && err.Error() != tests[i].want) {
			t.Errorf("%s: serve() = %v, want %q", tests[i].name, err, tests[i].want)
		}
		if listener.calls != tests[i].wantCalls {
			t.Errorf("%s: %d calls of Accept, want %d", tests[i].name, listener.calls, tests[i].wantCalls)
		}
	}
}

// A context that has ended makes every accept error the end of serving, whatever it says.
func TestPoolServeAfterItsContextEnded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	listener := &scriptedListener{accept: func(int) (net.Conn, error) { return nil, errors.New("the listener broke") }}
	if err := newPool().serve(ctx, listener, time.Now().Add(testWait), func(net.Conn) {}, func(net.Conn) {}); err != nil {
		t.Fatalf("serve() = %v, want nil after the context ended", err)
	}
}

func TestPoolAcceptsAFixedNumberOfConnections(t *testing.T) {
	const limit = 1000
	var handled, refused atomic.Int64
	listener := &scriptedListener{accept: func(int) (net.Conn, error) { return idleConn{}, nil }}
	p := newPool()
	if p.limit != MaxConnections {
		t.Fatalf("a new pool accepts %d connections, want MaxConnections (%d)", p.limit, MaxConnections)
	}
	p.limit = limit
	err := p.serve(context.Background(), listener, time.Now().Add(testWait),
		func(net.Conn) { handled.Add(1) },
		func(net.Conn) { refused.Add(1) })
	want := fmt.Sprintf("accepted %d connections, the most one job may open", limit)
	if err == nil || err.Error() != want {
		t.Fatalf("serve() = %v, want %q", err, want)
	}
	if listener.calls != limit {
		t.Errorf("%d calls of Accept, want exactly %d", listener.calls, limit)
	}
	if got := handled.Load() + refused.Load(); got != limit || handled.Load() == 0 {
		t.Errorf("%d served and %d answered as busy, want %d together and some served", handled.Load(), refused.Load(), limit)
	}
	// A listener left open would take the next connection and never answer it.
	if got := listener.closes.Load(); got != 1 {
		t.Errorf("%d closes of the listener at the bound, want 1", got)
	}
}

// halfEnd is a connection end whose CloseWrite answers what the test says.
type halfEnd struct{ err error }

func (h halfEnd) CloseWrite() error { return h.err }

func TestEndWriteBothWays(t *testing.T) {
	other := errors.New("no space left")
	tests := []struct {
		name string
		err  error
		want error
	}{
		{"ended", nil, nil},
		{"peer gone already", syscall.ENOTCONN, nil},
		{"peer gone already, wrapped", &net.OpError{Op: "shutdown", Net: "tcp", Err: os.NewSyscallError("shutdown", syscall.ENOTCONN)}, nil},
		{"closed by this side", net.ErrClosed, nil},
		{"anything else", other, other},
		{"a reset is not a peer that is gone", syscall.ECONNRESET, syscall.ECONNRESET},
	}
	for i := 0; i < len(tests); i++ {
		if got := endWrite(halfEnd{tests[i].err}); !errors.Is(got, tests[i].want) || (tests[i].want == nil && got != nil) {
			t.Errorf("%s: endWrite() = %v, want %v", tests[i].name, got, tests[i].want)
		}
	}
}

// wholeConn is a connection that cannot end its writing side alone.
type wholeConn struct{ net.Conn }

// loopbackPair is a TCP connection on loopback, both ends.
func loopbackPair(t *testing.T) (*net.TCPConn, *net.TCPConn) {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() = %v, want nil", err)
	}
	defer func() {
		if closeErr := listener.Close(); closeErr != nil {
			t.Errorf("close the listener = %v, want nil", closeErr)
		}
	}()
	near, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("Dial() = %v, want nil", err)
	}
	far, err := listener.Accept()
	if err != nil {
		t.Fatalf("Accept() = %v, want nil", err)
	}
	nearTCP, nearOK := near.(*net.TCPConn)
	farTCP, farOK := far.(*net.TCPConn)
	if !nearOK || !farOK {
		t.Fatalf("connections are %T and %T, want TCP", near, far)
	}
	deadline := time.Now().Add(testWait)
	if err = errors.Join(nearTCP.SetDeadline(deadline), farTCP.SetDeadline(deadline)); err != nil {
		t.Fatalf("SetDeadline() = %v, want nil", err)
	}
	t.Cleanup(func() {
		if closeErr := errors.Join(closed(nearTCP), closed(farTCP)); closeErr != nil {
			t.Errorf("close the pair = %v, want nil", closeErr)
		}
	})
	return nearTCP, farTCP
}

// TestPipeEndsBothDirectionsWhenAnEndCannotHalfClose: the end of one direction cannot be
// passed on alone, so the whole tunnel ends and neither copy waits for its deadline.
func TestPipeEndsBothDirectionsWhenAnEndCannotHalfClose(t *testing.T) {
	clientNear, clientFar := loopbackPair(t)
	upstreamNear, upstreamFar := loopbackPair(t)
	done := make(chan error, 1)
	go func() {
		done <- pipe(context.Background(), wholeConn{clientNear}, clientNear, wholeConn{upstreamNear})
	}()
	if _, err := io.WriteString(clientFar, "ping"); err != nil {
		t.Fatalf("write = %v, want nil", err)
	}
	if err := clientFar.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite() = %v, want nil", err)
	}
	got, err := io.ReadAll(upstreamFar)
	if err != nil || string(got) != "ping" {
		t.Fatalf("the far end read %q, %v, want ping and the end", got, err)
	}
	select {
	case err = <-done:
	case <-time.After(testWait):
		t.Fatal("pipe() still runs, want it to end with the direction that ended")
	}
	if err != nil {
		t.Errorf("pipe() = %v, want nil: an end this side closed is a clean end", err)
	}
	if _, err = upstreamNear.Write([]byte("x")); err == nil || !strings.Contains(err.Error(), "use of closed network connection") {
		t.Errorf("write on the tunnel's end = %v, want it closed", err)
	}
}
