package egressproxy

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Temporary measurement for the Windows failure of TestRelayOneSetsTheTunnelDeadline on main
// d593720: does the peer see the end of a Unix connection's writing side when that end comes
// before or after the accept? Removed again before this branch lands.

const (
	probeRounds    = 100
	probeMaxLosses = 5
	probeWait      = 2 * time.Second
)

func TestProbeHalfCloseDelivery(t *testing.T) {
	variants := []struct {
		name string
		mode probeMode
	}{
		{"half-close after the accept", probeAcceptFirst},
		{"half-close before the accept", probeCloseFirst},
		{"half-close while a read waits", probeReadFirst},
	}
	for i := 0; i < len(variants); i++ {
		eof, lost, other := probeVariant(t, variants[i].mode)
		t.Logf("probe: %s: %d EOF, %d lost (read timed out after %s), %d other", variants[i].name, eof, lost, probeWait, other)
		if lost > 0 || other > 0 {
			t.Errorf("probe: %s: %d lost, %d other, want 0", variants[i].name, lost, other)
		}
	}
}

func probeVariant(t *testing.T, mode probeMode) (eof, lost, other int) {
	t.Helper()
	for r := 0; r < probeRounds && lost < probeMaxLosses; r++ {
		switch err := probeOnce(t, mode); {
		case errors.Is(err, io.EOF):
			eof++
		case errors.Is(err, os.ErrDeadlineExceeded):
			lost++
		default:
			other++
			t.Logf("probe: round %d: %v", r, err)
		}
	}
	return eof, lost, other
}

type probeMode int

const (
	probeAcceptFirst probeMode = iota
	probeCloseFirst
	probeReadFirst
)

// probeOnce returns what the accepting side's first read returns.
func probeOnce(t *testing.T, mode probeMode) error {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", filepath.Join(shortDir(t), "s"))
	if err != nil {
		t.Fatalf("Listen() = %v, want nil", err)
	}
	defer probeClose(t, listener)
	accepted := make(chan net.Conn, 1)
	accept := func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			t.Errorf("Accept() = %v, want nil", acceptErr)
		}
		accepted <- conn
	}
	if mode != probeCloseFirst {
		go accept()
		time.Sleep(20 * time.Millisecond)
	}
	client, err := (&net.Dialer{}).DialContext(t.Context(), "unix", listener.Addr().String())
	if err != nil {
		t.Fatalf("Dial() = %v, want nil", err)
	}
	defer probeClose(t, client)
	unix, ok := client.(*net.UnixConn)
	if !ok {
		t.Fatalf("Dial() = %T, want a *net.UnixConn", client)
	}
	if mode == probeReadFirst {
		return closeWhileReading(t, <-accepted, unix)
	}
	if err = unix.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite() = %v, want nil", err)
	}
	if mode == probeCloseFirst {
		time.Sleep(20 * time.Millisecond)
		go accept()
	}
	conn := <-accepted
	if conn == nil {
		return errors.New("no connection accepted")
	}
	defer probeClose(t, conn)
	return readOne(t, conn)
}

func readOne(t *testing.T, conn net.Conn) error {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(probeWait)); err != nil {
		t.Errorf("SetReadDeadline() = %v, want nil", err)
		return err
	}
	_, err := conn.Read(make([]byte, 1))
	return err
}

func probeClose(t *testing.T, c io.Closer) {
	t.Helper()
	if err := closed(c); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}
}

// closeWhileReading half-closes client while a read on conn already waits.
func closeWhileReading(t *testing.T, conn net.Conn, client *net.UnixConn) error {
	t.Helper()
	if conn == nil {
		return errors.New("no connection accepted")
	}
	defer probeClose(t, conn)
	read := make(chan error, 1)
	go func() { read <- readOne(t, conn) }()
	time.Sleep(20 * time.Millisecond)
	if err := client.CloseWrite(); err != nil {
		t.Errorf("CloseWrite() = %v, want nil", err)
	}
	return <-read
}

// TestProbeRelayOneEmptyClient repeats the failing case of TestRelayOneSetsTheTunnelDeadline:
// a relay whose client sends nothing, against a running proxy, with a 2 s deadline.
func TestProbeRelayOneEmptyClient(t *testing.T) {
	const rounds = 200
	server := startServer(t, Options{Destinations: []string{"gateway.example:443"}})
	timeouts, other := 0, 0
	for r := 0; r < rounds && timeouts < probeMaxLosses; r++ {
		conn := &scriptedConn{in: strings.NewReader("")}
		err := relayOne(t.Context(), conn, server.SocketPath(), time.Now().Add(probeWait))
		switch {
		case err == nil:
		case errors.Is(err, os.ErrDeadlineExceeded):
			timeouts++
			t.Logf("probe: relay round %d: %v, answer so far %q", r, err, conn.out.String())
		default:
			other++
			t.Logf("probe: relay round %d: %v", r, err)
		}
	}
	t.Logf("probe: relayOne with an empty client: %d timeouts, %d other errors", timeouts, other)
	if timeouts > 0 || other > 0 {
		t.Errorf("probe: relayOne: %d timeouts, %d other, want 0", timeouts, other)
	}
}
