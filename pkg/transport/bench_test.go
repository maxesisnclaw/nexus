package transport

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/vmihailenco/msgpack/v5"
)

func BenchmarkMsgpackEncodeDecode(b *testing.B) {
	msg := &Message{
		Method:  "benchmark.echo",
		Payload: bytes.Repeat([]byte("x"), 1024),
		Headers: map[string]string{"trace": "abc123"},
	}
	buf := bytes.NewBuffer(nil)
	enc := msgpack.NewEncoder(buf)
	dec := msgpack.NewDecoder(buf)

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		buf.Reset()
		if err := enc.Encode(msg); err != nil {
			b.Fatalf("Encode() error = %v", err)
		}
		var out Message
		if err := dec.Decode(&out); err != nil {
			b.Fatalf("Decode() error = %v", err)
		}
	}
}

func BenchmarkUDSSendRecv(b *testing.B) {
	ctx := context.Background()
	uds := NewUDSTransport()
	sock := filepath.Join(b.TempDir(), "bench.sock")
	ln, err := uds.Listen(ctx, sock)
	if err != nil {
		b.Fatalf("Listen() error = %v", err)
	}
	defer ln.Close()

	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		conn, err := ln.Accept(ctx)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			msg, err := conn.Recv()
			if err != nil {
				return
			}
			if err := conn.Send(msg); err != nil {
				return
			}
		}
	}()

	client, err := uds.Dial(ctx, ServiceEndpoint{Local: true, UDSAddr: sock})
	if err != nil {
		b.Fatalf("Dial() error = %v", err)
	}
	defer client.Close()

	msg := &Message{Method: "ping", Payload: bytes.Repeat([]byte("z"), 256)}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := client.Send(msg); err != nil {
			b.Fatalf("Send() error = %v", err)
		}
		if _, err := client.Recv(); err != nil {
			b.Fatalf("Recv() error = %v", err)
		}
	}
	b.StopTimer()
	_ = client.Close()
	<-serverDone
}
