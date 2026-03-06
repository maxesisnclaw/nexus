//go:build linux

package transport

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestUDSSendRecvFD(t *testing.T) {
	ctx := context.Background()
	uds := NewUDSTransport()
	sock := filepath.Join(t.TempDir(), "fd.sock")
	ln, err := uds.Listen(ctx, sock)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer ln.Close()

	type result struct {
		metadata []byte
		payload  []byte
		err      error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := ln.Accept(ctx)
		if err != nil {
			ch <- result{err: err}
			return
		}
		defer c.Close()
		fd, md, err := c.RecvFd()
		if err != nil {
			ch <- result{err: err}
			return
		}
		defer unix.Close(fd)
		data, err := ReadFDAll(fd)
		ch <- result{metadata: md, payload: data, err: err}
	}()

	c, err := uds.Dial(ctx, ServiceEndpoint{Local: true, UDSAddr: sock})
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer c.Close()

	fd, err := CreateMemfd("test-payload", []byte("hello-zero-copy"))
	if err != nil {
		t.Fatalf("CreateMemfd() error = %v", err)
	}
	defer unix.Close(fd)

	if err := c.SendFd(fd, []byte("meta")); err != nil {
		t.Fatalf("SendFd() error = %v", err)
	}

	select {
	case got := <-ch:
		if got.err != nil {
			t.Fatalf("server result error = %v", got.err)
		}
		if string(got.metadata) != "meta" {
			t.Fatalf("unexpected metadata %q", string(got.metadata))
		}
		if string(got.payload) != "hello-zero-copy" {
			t.Fatalf("unexpected payload %q", string(got.payload))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for fd receive")
	}
}

func TestCreateMemfdAndReadFDAll(t *testing.T) {
	payload := bytes.Repeat([]byte("p"), 1024)
	fd, err := CreateMemfd("unit-test", payload)
	if err != nil {
		t.Fatalf("CreateMemfd() error = %v", err)
	}
	defer unix.Close(fd)

	got, err := ReadFDAll(fd)
	if err != nil {
		t.Fatalf("ReadFDAll() error = %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload mismatch: got=%d want=%d", len(got), len(payload))
	}
}

func TestReadFDAllInvalidFD(t *testing.T) {
	if _, err := ReadFDAll(-1); err == nil {
		t.Fatal("expected invalid fd error")
	}
}

func TestSendRecvFDNilConnErrors(t *testing.T) {
	if err := sendFD(nil, 1, []byte("x")); err == nil {
		t.Fatal("expected sendFD nil conn error")
	}
	if _, _, err := recvFD(nil); err == nil {
		t.Fatal("expected recvFD nil conn error")
	}
	if err := sendFD(nil, -1, nil); err == nil {
		t.Fatal("expected sendFD invalid fd error")
	}
}
