package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

const insecureTCPListenEnv = "NEXUS_ALLOW_INSECURE_TCP_LISTEN"
const insecureTCPDialEnv = "NEXUS_ALLOW_INSECURE_TCP_DIAL"

// TCPTransport implements TCP transport.
type TCPTransport struct {
	allowInsecureRemoteDial bool
}

// NewTCPTransport creates a TCP transport.
func NewTCPTransport() *TCPTransport {
	return &TCPTransport{allowInsecureRemoteDial: allowsInsecureTCPDial()}
}

// NewTCPTransportWithInsecureRemoteDial creates a TCP transport with explicit
// opt-in policy for non-loopback plaintext remote dials.
func NewTCPTransportWithInsecureRemoteDial(allow bool) *TCPTransport {
	return &TCPTransport{allowInsecureRemoteDial: allow}
}

func (t *TCPTransport) Dial(ctx context.Context, target ServiceEndpoint) (Conn, error) {
	if target.TCPAddr == "" {
		return nil, errors.New("missing tcp address")
	}
	if len(target.PublicKey) == 0 && !t.allowInsecureRemoteDial && !target.Local && !IsLoopbackTCPAddr(target.TCPAddr) {
		return nil, fmt.Errorf(
			"refusing non-loopback plaintext tcp dial %s without explicit opt-in; set %s=1",
			target.TCPAddr,
			insecureTCPDialEnv,
		)
	}
	d := net.Dialer{}
	c, err := d.DialContext(ctx, "tcp", target.TCPAddr)
	if err != nil {
		return nil, fmt.Errorf("tcp dial %s: %w", target.TCPAddr, err)
	}
	return &tcpConn{msgpackConn: newMsgpackConn(c)}, nil
}

func (t *TCPTransport) Listen(_ context.Context, addr string) (Listener, error) {
	tcpAddr, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("resolve tcp address %s: %w", addr, err)
	}
	if !allowsInsecureTCPListen() && !isLoopbackListenAddr(tcpAddr) {
		return nil, fmt.Errorf("refusing non-loopback tcp listen address %s without %s=1", addr, insecureTCPListenEnv)
	}
	ln, err := net.ListenTCP("tcp", tcpAddr)
	if err != nil {
		return nil, fmt.Errorf("tcp listen %s: %w", addr, err)
	}
	return &tcpListener{ln: ln}, nil
}

func allowsInsecureTCPListen() bool {
	return os.Getenv(insecureTCPListenEnv) == "1"
}

func allowsInsecureTCPDial() bool {
	return os.Getenv(insecureTCPDialEnv) == "1"
}

// IsLoopbackTCPAddr reports whether addr has a loopback host component.
func IsLoopbackTCPAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	host = strings.TrimSpace(host)
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if i := strings.LastIndex(host, "%"); i >= 0 {
		host = host[:i]
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isLoopbackListenAddr(addr *net.TCPAddr) bool {
	return addr != nil && addr.IP != nil && addr.IP.IsLoopback()
}

type tcpListener struct {
	ln *net.TCPListener
}

func (l *tcpListener) Accept(ctx context.Context) (Conn, error) {
	for {
		if err := l.ln.SetDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
			return nil, err
		}
		c, err := l.ln.AcceptTCP()
		if err == nil {
			return &tcpConn{msgpackConn: newMsgpackConn(c)}, nil
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
