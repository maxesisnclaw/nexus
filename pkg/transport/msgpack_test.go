package transport

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/vmihailenco/msgpack/v5"
)

func TestMsgpackRecvDecodeError(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	conn := newMsgpackConn(client)
	errCh := make(chan error, 1)
	go func() {
		_, err := server.Write([]byte{0xff, 0x01, 0x02})
		errCh <- err
		_ = server.Close()
	}()

	if _, err := conn.Recv(); err == nil {
		t.Fatal("expected decode error from invalid msgpack bytes")
	}
	if err := <-errCh; err != nil {
		t.Fatalf("pipe write error: %v", err)
	}
}

func TestMsgpackRecvRejectsMessageOverLimit(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	const limit = int64(128)
	conn := newMsgpackConn(client, WithMaxMessageSize(limit))
	sendDone := make(chan error, 1)
	go func() {
		enc := msgpack.NewEncoder(server)
		sendDone <- enc.Encode(&Message{
			Method:  "big",
			Payload: bytes.Repeat([]byte("x"), 4096),
		})
	}()

	_, err := conn.Recv()
	if err == nil {
		t.Fatal("expected oversized message error")
	}
	want := fmt.Sprintf("message exceeds maximum size of %d bytes", limit)
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("expected error containing %q, got %v", want, err)
	}

	_ = client.Close()
	select {
	case <-sendDone:
	case <-time.After(2 * time.Second):
		t.Fatal("sender goroutine did not exit")
	}
}

func TestMsgpackRecvAcceptsMessageWithinLimit(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	const limit = int64(2048)
	conn := newMsgpackConn(client, WithMaxMessageSize(limit))
	msg := &Message{
		Method:  "ok",
		Payload: bytes.Repeat([]byte("y"), 512),
		Headers: map[string]string{"trace": "t-1"},
	}
	sendDone := make(chan error, 1)
	go func() {
		enc := msgpack.NewEncoder(server)
		sendDone <- enc.Encode(msg)
	}()

	got, err := conn.Recv()
	if err != nil {
		t.Fatalf("Recv() error = %v", err)
	}
	if got.Method != msg.Method {
		t.Fatalf("unexpected method: got=%s want=%s", got.Method, msg.Method)
	}
	if !bytes.Equal(got.Payload, msg.Payload) {
		t.Fatalf("unexpected payload length: got=%d want=%d", len(got.Payload), len(msg.Payload))
	}
	if got.Headers["trace"] != "t-1" {
		t.Fatalf("unexpected headers: %+v", got.Headers)
	}
	if err := <-sendDone; err != nil && err != io.ErrClosedPipe {
		t.Fatalf("send error: %v", err)
	}
}
