package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
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
	if err := os.MkdirAll(filepath.Dir(addr), 0o700); err != nil {
		return nil, fmt.Errorf("create uds dir: %w", err)
	}
	if err := cleanupSocketPath(addr); err != nil {
		return nil, err
	}
	unixAddr, err := net.ResolveUnixAddr("unix", addr)
	if err != nil {
		return nil, fmt.Errorf("resolve uds address %s: %w", addr, err)
	}
	ln, err := net.ListenUnix("unix", unixAddr)
	if err != nil {
		return nil, fmt.Errorf("uds listen %s: %w", addr, err)
	}
	if err := os.Chmod(addr, 0o600); err != nil {
		ln.Close()
		return nil, fmt.Errorf("chmod uds socket: %w", err)
	}
	return &udsListener{ln: ln, path: addr}, nil
}

type udsListener struct {
	ln   *net.UnixListener
	path string
}

func (l *udsListener) Accept(ctx context.Context) (Conn, error) {
	for {
		if err := l.ln.SetDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
			return nil, err
		}
		uc, err := l.ln.AcceptUnix()
		if err == nil {
			return &udsConn{msgpackConn: newMsgpackConn(uc), unixConn: uc}, nil
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
				continue
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			return nil, err
		}
	}
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

func cleanupSocketPath(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat existing socket path: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("uds path exists and is not a socket: %s", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("cleanup stale socket: %w", err)
	}
	return nil
}
