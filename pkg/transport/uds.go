package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

// UDSTransport implements unix domain socket transport.
type UDSTransport struct{}

// NewUDSTransport creates a UDS transport.
func NewUDSTransport() *UDSTransport {
	return &UDSTransport{}
}

func (t *UDSTransport) Dial(ctx context.Context, target ServiceEndpoint) (Conn, error) {
	if target.UDSAddr == "" {
		return nil, errors.New("missing uds address")
	}
	d := net.Dialer{}
	c, err := d.DialContext(ctx, "unix", target.UDSAddr)
	if err != nil {
		return nil, fmt.Errorf("uds dial %s: %w", target.UDSAddr, err)
	}
	uc, ok := c.(*net.UnixConn)
	if !ok {
		_ = c.Close()
		return nil, errors.New("not a unix connection")
	}
	return &udsConn{msgpackConn: newMsgpackConn(uc), unixConn: uc}, nil
}

func (t *UDSTransport) Listen(_ context.Context, addr string) (Listener, error) {
	if addr == "" {
		return nil, errors.New("missing uds listen address")
	}
	if err := os.MkdirAll(filepath.Dir(addr), 0o755); err != nil {
		return nil, fmt.Errorf("create uds dir: %w", err)
	}
	if err := os.Remove(addr); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("cleanup stale socket: %w", err)
	}
	ln, err := net.Listen("unix", addr)
	if err != nil {
		return nil, fmt.Errorf("uds listen %s: %w", addr, err)
	}
	return &udsListener{ln: ln, path: addr}, nil
}

type udsListener struct {
	ln   net.Listener
	path string
}

func (l *udsListener) Accept(_ context.Context) (Conn, error) {
	c, err := l.ln.Accept()
	if err != nil {
		return nil, err
	}
	uc, ok := c.(*net.UnixConn)
	if !ok {
		_ = c.Close()
		return nil, errors.New("not a unix connection")
	}
	return &udsConn{msgpackConn: newMsgpackConn(uc), unixConn: uc}, nil
}

func (l *udsListener) Close() error {
	errListen := l.ln.Close()
	errSock := os.Remove(l.path)
	if errSock != nil && !errors.Is(errSock, os.ErrNotExist) {
		return errors.Join(errListen, errSock)
	}
	return errListen
}

func (l *udsListener) Addr() string {
	return l.ln.Addr().String()
}

type udsConn struct {
	*msgpackConn
	unixConn *net.UnixConn
}

func (c *udsConn) SendFd(fd int, metadata []byte) error {
	return sendFD(c.unixConn, fd, metadata)
}

func (c *udsConn) RecvFd() (int, []byte, error) {
	return recvFD(c.unixConn)
}
