package egressproxy

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
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
		name        string
		acceptFirst bool
	}{
		{"half-close after the accept", true},
		{"half-close before the accept", false},
	}
	for i := 0; i < len(variants); i++ {
		eof, lost, other := probeVariant(t, variants[i].acceptFirst)
		t.Logf("probe: %s: %d EOF, %d lost (read timed out after %s), %d other", variants[i].name, eof, lost, probeWait, other)
		if lost > 0 || other > 0 {
			t.Errorf("probe: %s: %d lost, %d other, want 0", variants[i].name, lost, other)
		}
	}
}

func probeVariant(t *testing.T, acceptFirst bool) (eof, lost, other int) {
	t.Helper()
	for r := 0; r < probeRounds && lost < probeMaxLosses; r++ {
		switch err := probeOnce(t, acceptFirst); {
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

// probeOnce returns what the accepting side's first read returns.
func probeOnce(t *testing.T, acceptFirst bool) error {
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
	if acceptFirst {
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
	if err = unix.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite() = %v, want nil", err)
	}
	if !acceptFirst {
		time.Sleep(20 * time.Millisecond)
		go accept()
	}
	conn := <-accepted
	if conn == nil {
		return errors.New("no connection accepted")
	}
	defer probeClose(t, conn)
	if err = conn.SetReadDeadline(time.Now().Add(probeWait)); err != nil {
		t.Fatalf("SetReadDeadline() = %v, want nil", err)
	}
	_, err = conn.Read(make([]byte, 1))
	return err
}

func probeClose(t *testing.T, c io.Closer) {
	t.Helper()
	if err := closed(c); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}
}
