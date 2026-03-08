package transport

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/flynn/noise"
)

const noiseHandshakeTimeout = 5 * time.Second

// NoiseTCPTransport wraps TCP with Noise Protocol encryption (NK pattern).
type NoiseTCPTransport struct {
	privateKey  []byte
	publicKey   []byte
	trustedKeys map[string]bool // hex-encoded server public keys allowed for dial.
}

// NewNoiseTCPTransport creates a Noise-encrypted TCP transport.
// Note: NK authenticates the responder (server) only.
func NewNoiseTCPTransport(privKey, pubKey []byte, trustedKeys []string) *NoiseTCPTransport {
	t := &NoiseTCPTransport{
		privateKey: append([]byte(nil), privKey...),
		publicKey:  append([]byte(nil), pubKey...),
	}
	if len(trustedKeys) > 0 {
		t.trustedKeys = make(map[string]bool, len(trustedKeys))
		for _, key := range trustedKeys {
			normalized := strings.ToLower(strings.TrimSpace(key))
			if normalized == "" {
				continue
			}
			t.trustedKeys[normalized] = true
		}
	}
	return t
}

func (t *NoiseTCPTransport) Dial(ctx context.Context, target ServiceEndpoint) (Conn, error) {
	if target.TCPAddr == "" {
		return nil, errors.New("missing tcp address")
	}
	if len(target.PublicKey) != noisePublicKeySize {
		return nil, errors.New("missing or invalid target noise public key")
	}
	targetKeyHex := strings.ToLower(hex.EncodeToString(target.PublicKey))
	if len(t.trustedKeys) > 0 && !t.trustedKeys[targetKeyHex] {
		return nil, fmt.Errorf("target noise key %s is not trusted", targetKeyHex)
	}

	d := net.Dialer{}
	raw, err := d.DialContext(ctx, "tcp", target.TCPAddr)
	if err != nil {
		return nil, fmt.Errorf("tcp dial %s: %w", target.TCPAddr, err)
	}

	conn, err := t.handshakeInitiator(raw, target.PublicKey)
	if err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("noise handshake initiator %s: %w", target.TCPAddr, err)
	}
	return conn, nil
}

func (t *NoiseTCPTransport) Listen(_ context.Context, addr string) (Listener, error) {
	if len(t.privateKey) != noisePrivateKeySize || len(t.publicKey) != noisePublicKeySize {
		return nil, errors.New("noise tcp listener requires a 32-byte private key and 32-byte public key")
	}
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
	return &noiseTCPListener{ln: ln, transport: t}, nil
}

func (t *NoiseTCPTransport) handshakeInitiator(raw net.Conn, targetPublicKey []byte) (Conn, error) {
	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite: noiseSuite,
		Pattern:     noise.HandshakeNK,
		Initiator:   true,
		PeerStatic:  append([]byte(nil), targetPublicKey...),
	})
	if err != nil {
		return nil, err
	}

	_ = raw.SetDeadline(time.Now().Add(noiseHandshakeTimeout))
	defer func() { _ = raw.SetDeadline(time.Time{}) }()

	initMsg, _, _, err := hs.WriteMessage(nil, nil)
	if err != nil {
		return nil, err
	}
	if err := writeLengthPrefixed(raw, initMsg); err != nil {
		return nil, err
	}

	resp, err := readLengthPrefixed(raw, noise.MaxMsgLen)
	if err != nil {
		return nil, err
	}
	_, csSend, csRecv, err := hs.ReadMessage(nil, resp)
	if err != nil {
		return nil, err
	}
	if csSend == nil || csRecv == nil {
		return nil, errors.New("noise handshake did not produce cipher states")
	}
	return &noiseConn{raw: raw, cs: csSend, csRecv: csRecv}, nil
}

func (t *NoiseTCPTransport) handshakeResponder(raw net.Conn) (Conn, error) {
	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite: noiseSuite,
		Pattern:     noise.HandshakeNK,
		Initiator:   false,
		StaticKeypair: noise.DHKey{
			Private: append([]byte(nil), t.privateKey...),
			Public:  append([]byte(nil), t.publicKey...),
		},
	})
	if err != nil {
		return nil, err
	}

	_ = raw.SetDeadline(time.Now().Add(noiseHandshakeTimeout))
	defer func() { _ = raw.SetDeadline(time.Time{}) }()

	msg1, err := readLengthPrefixed(raw, noise.MaxMsgLen)
	if err != nil {
		return nil, err
	}
	if _, _, _, err := hs.ReadMessage(nil, msg1); err != nil {
		return nil, err
	}
	msg2, csIToR, csRToI, err := hs.WriteMessage(nil, nil)
	if err != nil {
		return nil, err
	}
	if csIToR == nil || csRToI == nil {
		return nil, errors.New("noise handshake did not produce cipher states")
	}
	if err := writeLengthPrefixed(raw, msg2); err != nil {
		return nil, err
	}
	// Split() outputs initiator->responder first, then responder->initiator.
	return &noiseConn{raw: raw, cs: csRToI, csRecv: csIToR}, nil
}

type noiseTCPListener struct {
	ln        *net.TCPListener
	transport *NoiseTCPTransport
}

func (l *noiseTCPListener) Accept(ctx context.Context) (Conn, error) {
	for {
		if err := l.ln.SetDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
			return nil, err
		}
		raw, err := l.ln.AcceptTCP()
		if err == nil {
			conn, handshakeErr := l.transport.handshakeResponder(raw)
			if handshakeErr != nil {
				_ = raw.Close()
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				default:
					continue
				}
			}
			return conn, nil
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

func (l *noiseTCPListener) Close() error {
	return l.ln.Close()
}

func (l *noiseTCPListener) Addr() string {
	return l.ln.Addr().String()
}
