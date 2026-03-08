package transport

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestWriteAllReturnsErrNoProgressOnZeroByteWrite(t *testing.T) {
	conn := &zeroWriteConn{}
	err := writeAll(conn, []byte("hello"))
	if !errors.Is(err, io.ErrNoProgress) {
		t.Fatalf("expected io.ErrNoProgress, got %v", err)
	}
}

type zeroWriteConn struct{}

func (c *zeroWriteConn) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (c *zeroWriteConn) Write([]byte) (int, error) {
	return 0, nil
}

func (c *zeroWriteConn) Close() error {
	return nil
}

func (c *zeroWriteConn) LocalAddr() net.Addr {
	return dummyAddr("local")
}

func (c *zeroWriteConn) RemoteAddr() net.Addr {
	return dummyAddr("remote")
}

func (c *zeroWriteConn) SetDeadline(time.Time) error {
	return nil
}

func (c *zeroWriteConn) SetReadDeadline(time.Time) error {
	return nil
}

func (c *zeroWriteConn) SetWriteDeadline(time.Time) error {
	return nil
}

type dummyAddr string

func (a dummyAddr) Network() string { return "test" }
func (a dummyAddr) String() string  { return string(a) }
