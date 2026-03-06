package transport

import (
	"net"
	"testing"
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
