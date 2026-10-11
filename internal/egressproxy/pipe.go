package egressproxy

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"
)

// halfCloser is a connection that can end its writing side and keep reading, as TCP and
// Unix stream connections can.
type halfCloser interface {
	CloseWrite() error
}

// pipe copies both ways between a and b and returns when both directions have ended. It
// closes both connections when ctx ends or when one direction fails, so neither copy
// outlives the other for longer than its deadline. fromA is what is read for a: the
// connection itself, or a reader that still holds bytes read ahead from it.
//
// The error is what ended the tunnel when that was no clean end. A connection closed by
// this side, on ctx or after the other direction failed, is a clean end.
func pipe(ctx context.Context, a net.Conn, fromA io.Reader, b net.Conn) error {
	results := make(chan error, 3)
	stop := context.AfterFunc(ctx, func() { results <- errors.Join(closed(a), closed(b)) })
	go func() { results <- copyHalf(b, fromA, a) }()
	go func() { results <- copyHalf(a, b, b) }()
	err := errors.Join(<-results, <-results)
	if !stop() {
		// The closing function ran or runs: its result is the third one.
		err = errors.Join(err, <-results)
	}
	return err
}

// copyHalf copies src to dst until src ends. After a clean end it ends the writing side of
// dst, so the peer sees the end and the other direction can finish. After an error, or when
// dst cannot end one side alone, it closes both connections, which ends the other
// direction too. srcConn is the connection src reads from.
func copyHalf(dst net.Conn, src io.Reader, srcConn net.Conn) error {
	_, err := io.Copy(dst, src)
	if half, ok := dst.(halfCloser); ok && err == nil {
		return endWrite(half)
	}
	return errors.Join(ignoreClosed(err), closed(dst), closed(srcConn))
}

// endWrite ends the writing side. A peer that is gone already has nothing left to be told.
func endWrite(conn halfCloser) error {
	err := conn.CloseWrite()
	if errors.Is(err, syscall.ENOTCONN) {
		return nil
	}
	return ignoreClosed(err)
}

// closed closes c. Closing what is closed already is no error: a tunnel has several ends
// that may close it, and the first one wins.
func closed(c io.Closer) error {
	return ignoreClosed(c.Close())
}

// peerEnded reports an error that says only how the peer ended the connection: with a
// reset, which a program sends when it closes a connection with bytes unread, or by being
// closed already when the next write came.
func peerEnded(err error) bool {
	return errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE)
}

func ignoreClosed(err error) error {
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}
