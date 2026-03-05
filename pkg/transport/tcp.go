package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
)

// TCPTransport implements TCP transport.
type TCPTransport struct{}

// NewTCPTransport creates a TCP transport.
func NewTCPTransport() *TCPTransport {
	return &TCPTransport{}
}

func (t *TCPTransport) Dial(ctx context.Context, target ServiceEndpoint) (Conn, error) {
	if target.TCPAddr == "" {
		return nil, errors.New("missing tcp address")
	}
	d := net.Dialer{}
	c, err := d.DialContext(ctx, "tcp", target.TCPAddr)
	if err != nil {
		return nil, fmt.Errorf("tcp dial %s: %w", target.TCPAddr, err)
	}
	return &tcpConn{msgpackConn: newMsgpackConn(c)}, nil
}

func (t *TCPTransport) Listen(_ context.Context, addr string) (Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("tcp listen %s: %w", addr, err)
	}
	return &tcpListener{ln: ln}, nil
}

type tcpListener struct {
	ln net.Listener
}

func (l *tcpListener) Accept(_ context.Context) (Conn, error) {
	c, err := l.ln.Accept()
	if err != nil {
		return nil, err
	}
	return &tcpConn{msgpackConn: newMsgpackConn(c)}, nil
}

func (l *tcpListener) Close() error {
	return l.ln.Close()
}

func (l *tcpListener) Addr() string {
	return l.ln.Addr().String()
}

type tcpConn struct {
	*msgpackConn
}

func (c *tcpConn) SendFd(int, []byte) error {
	return ErrFDUnsupported
}

func (c *tcpConn) RecvFd() (int, []byte, error) {
	return -1, nil, ErrFDUnsupported
}
