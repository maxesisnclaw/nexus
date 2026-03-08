package transport

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGenerateAndLoadNoiseKey(t *testing.T) {
	priv, pub := GenerateKeypair()
	if len(priv) != noisePrivateKeySize || len(pub) != noisePublicKeySize {
		t.Fatalf("unexpected generated key lengths: priv=%d pub=%d", len(priv), len(pub))
	}

	path := filepath.Join(t.TempDir(), "noise.key")
	loadedPriv, loadedPub, err := LoadOrGenerateKey(path)
	if err != nil {
		t.Fatalf("LoadOrGenerateKey() error = %v", err)
	}
	if len(loadedPriv) != noisePrivateKeySize || len(loadedPub) != noisePublicKeySize {
		t.Fatalf("unexpected loaded key lengths: priv=%d pub=%d", len(loadedPriv), len(loadedPub))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected key file mode 0600, got %o", info.Mode().Perm())
	}

	loadedPrivAgain, loadedPubAgain, err := LoadOrGenerateKey(path)
	if err != nil {
		t.Fatalf("LoadOrGenerateKey() second error = %v", err)
	}
	if !bytes.Equal(loadedPrivAgain, loadedPriv) || !bytes.Equal(loadedPubAgain, loadedPub) {
		t.Fatal("expected same keypair on repeated load")
	}
}

func TestLoadOrGenerateKeyRejectsInsecurePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "noise.key")
	priv, _ := GenerateKeypair()
	if len(priv) != noisePrivateKeySize {
		t.Fatalf("unexpected private key length: %d", len(priv))
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(priv)), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}

	_, _, err := LoadOrGenerateKey(path)
	if err == nil {
		t.Fatal("expected insecure key file permissions to be rejected")
	}
	if !strings.Contains(err.Error(), "insecure permissions") {
		t.Fatalf("expected insecure permission error, got %v", err)
	}
}

func TestNoiseHandshakeBetweenGoroutines(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	serverPriv, serverPub := GenerateKeypair()
	server := NewNoiseTCPTransport(serverPriv, serverPub, nil)
	ln, err := server.Listen(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer ln.Close()

	accepted := make(chan error, 1)
	go func() {
		conn, err := ln.Accept(ctx)
		if err != nil {
			accepted <- err
			return
		}
		accepted <- conn.Close()
	}()

	client := NewNoiseTCPTransport(nil, nil, nil)
	conn, err := client.Dial(ctx, ServiceEndpoint{TCPAddr: ln.Addr(), PublicKey: serverPub})
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	_ = conn.Close()

	select {
	case err := <-accepted:
		if err != nil {
			t.Fatalf("Accept() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for accepted connection")
	}
}

func TestNoiseTCPTransportSendRecv(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	serverPriv, serverPub := GenerateKeypair()
	server := NewNoiseTCPTransport(serverPriv, serverPub, nil)
	ln, err := server.Listen(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer ln.Close()

	done := make(chan error, 1)
	go func() {
		serverConn, err := ln.Accept(ctx)
		if err != nil {
			done <- err
			return
		}
		defer serverConn.Close()
		msg, err := serverConn.Recv()
		if err != nil {
			done <- err
			return
		}
		if msg.Method != "ping" || !bytes.Equal(msg.Payload, []byte("hello")) {
			done <- fmt.Errorf("unexpected request: method=%s payload=%q", msg.Method, string(msg.Payload))
			return
		}
		done <- serverConn.Send(&Message{Method: "pong", Payload: []byte("ok")})
	}()

	client := NewNoiseTCPTransport(nil, nil, nil)
	clientConn, err := client.Dial(ctx, ServiceEndpoint{TCPAddr: ln.Addr(), PublicKey: serverPub})
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer clientConn.Close()

	if err := clientConn.Send(&Message{Method: "ping", Payload: []byte("hello")}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	resp, err := clientConn.Recv()
	if err != nil {
		t.Fatalf("Recv() error = %v", err)
	}
	if resp.Method != "pong" || !bytes.Equal(resp.Payload, []byte("ok")) {
		t.Fatalf("unexpected response: method=%s payload=%q", resp.Method, string(resp.Payload))
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("server loop error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server loop timeout")
	}
}

func TestNoiseDialWithWrongKeyRejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	serverPriv, _ := GenerateKeypair()
	// Ensure responder has a consistent keypair.
	serverPub, err := DerivePublicKey(serverPriv)
	if err != nil {
		t.Fatalf("DerivePublicKey() error = %v", err)
	}

	server := NewNoiseTCPTransport(serverPriv, serverPub, nil)
	ln, err := server.Listen(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer ln.Close()

	acceptDone := make(chan error, 1)
	go func() {
		_, err := ln.Accept(ctx)
		acceptDone <- err
	}()

	_, wrongPub := GenerateKeypair()
	client := NewNoiseTCPTransport(nil, nil, nil)
	if _, err := client.Dial(ctx, ServiceEndpoint{TCPAddr: ln.Addr(), PublicKey: wrongPub}); err == nil {
		t.Fatal("expected dial with wrong server key to fail")
	}

	cancel()
	select {
	case err := <-acceptDone:
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("unexpected accept error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for listener accept exit")
	}
}

func TestNoiseDialHandshakeHonorsContextCancellation(t *testing.T) {
	_, serverPub := GenerateKeypair()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}
		accepted <- conn
	}()

	ctx, cancel := context.WithCancel(context.Background())
	cancelAfter := 80 * time.Millisecond
	go func() {
		time.Sleep(cancelAfter)
		cancel()
	}()

	client := NewNoiseTCPTransport(nil, nil, nil)
	start := time.Now()
	_, err = client.Dial(ctx, ServiceEndpoint{TCPAddr: ln.Addr().String(), PublicKey: serverPub})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected dial to fail after context cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled error, got %v", err)
	}
	if elapsed > time.Second {
		t.Fatalf("expected canceled handshake to fail promptly, elapsed=%s", elapsed)
	}

	select {
	case conn := <-accepted:
		_ = conn.Close()
	default:
	}
}

func TestNoiseDialHandshakeHonorsContextDeadline(t *testing.T) {
	_, serverPub := GenerateKeypair()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}
		accepted <- conn
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	client := NewNoiseTCPTransport(nil, nil, nil)
	start := time.Now()
	_, err = client.Dial(ctx, ServiceEndpoint{TCPAddr: ln.Addr().String(), PublicKey: serverPub})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected dial to fail after context deadline")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline exceeded, got %v", err)
	}
	if elapsed > time.Second {
		t.Fatalf("expected deadline-bounded handshake to fail promptly, elapsed=%s", elapsed)
	}

	select {
	case conn := <-accepted:
		_ = conn.Close()
	default:
	}
}

func TestNoiseConnMultipleMessages(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	serverPriv, serverPub := GenerateKeypair()
	server := NewNoiseTCPTransport(serverPriv, serverPub, nil)
	ln, err := server.Listen(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer ln.Close()

	done := make(chan error, 1)
	go func() {
		conn, err := ln.Accept(ctx)
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		for i := 0; i < 5; i++ {
			msg, err := conn.Recv()
			if err != nil {
				done <- err
				return
			}
			want := fmt.Sprintf("m%d", i)
			if msg.Method != want {
				done <- fmt.Errorf("unexpected method: got=%s want=%s", msg.Method, want)
				return
			}
			if err := conn.Send(&Message{Method: "ack", Payload: []byte(want)}); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()

	client := NewNoiseTCPTransport(nil, nil, nil)
	conn, err := client.Dial(ctx, ServiceEndpoint{TCPAddr: ln.Addr(), PublicKey: serverPub})
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer conn.Close()

	for i := 0; i < 5; i++ {
		method := fmt.Sprintf("m%d", i)
		if err := conn.Send(&Message{Method: method}); err != nil {
			t.Fatalf("Send(%s) error = %v", method, err)
		}
		resp, err := conn.Recv()
		if err != nil {
			t.Fatalf("Recv(%s) error = %v", method, err)
		}
		if resp.Method != "ack" || string(resp.Payload) != method {
			t.Fatalf("unexpected response for %s: method=%s payload=%q", method, resp.Method, string(resp.Payload))
		}
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("server loop error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for server loop")
	}
}
