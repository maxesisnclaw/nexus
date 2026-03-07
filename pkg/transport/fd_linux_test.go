//go:build linux

package transport

import (
	"bytes"
	"context"
	"errors"
	"net"
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
		data, err := ReadFDAll(fd, 1<<20)
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

	got, err := ReadFDAll(fd, int64(len(payload)))
	if err != nil {
		t.Fatalf("ReadFDAll() error = %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload mismatch: got=%d want=%d", len(got), len(payload))
	}
}

func TestReadFDAllInvalidFD(t *testing.T) {
	if _, err := ReadFDAll(-1, 1024); err == nil {
		t.Fatal("expected invalid fd error")
	}
}

func TestReadFDAllKeepsCallerFDOpen(t *testing.T) {
	payload := []byte("still-open")
	fd, err := CreateMemfd("readfdall-open", payload)
	if err != nil {
		t.Fatalf("CreateMemfd() error = %v", err)
	}
	defer unix.Close(fd)

	if _, err := ReadFDAll(fd, int64(len(payload))); err != nil {
		t.Fatalf("ReadFDAll() error = %v", err)
	}
	if _, err := unix.Seek(fd, 0, 0); err != nil {
		t.Fatalf("fd should remain open after ReadFDAll(): %v", err)
	}

	buf := make([]byte, len(payload))
	n, err := unix.Read(fd, buf)
	if err != nil {
		t.Fatalf("read via caller fd failed: %v", err)
	}
	if !bytes.Equal(buf[:n], payload) {
		t.Fatalf("caller fd payload mismatch: got=%q want=%q", string(buf[:n]), string(payload))
	}
}

func TestRecvFDClosesExtraFDs(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "multi-fd.sock")
	addr, err := net.ResolveUnixAddr("unix", sock)
	if err != nil {
		t.Fatalf("ResolveUnixAddr() error = %v", err)
	}
	ln, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatalf("ListenUnix() error = %v", err)
	}
	defer ln.Close()

	acceptCh := make(chan *net.UnixConn, 1)
	errCh := make(chan error, 1)
	go func() {
		conn, acceptErr := ln.AcceptUnix()
		if acceptErr != nil {
			errCh <- acceptErr
			return
		}
		acceptCh <- conn
	}()

	sender, err := net.DialUnix("unix", nil, addr)
	if err != nil {
		t.Fatalf("DialUnix() error = %v", err)
	}
	defer sender.Close()

	var receiver *net.UnixConn
	select {
	case receiver = <-acceptCh:
	case acceptErr := <-errCh:
		t.Fatalf("AcceptUnix() error = %v", acceptErr)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for accept")
	}
	defer receiver.Close()

	pipeFDs := make([]int, 2)
	if err := unix.Pipe2(pipeFDs, unix.O_CLOEXEC); err != nil {
		t.Fatalf("pipe() error = %v", err)
	}
	readFD := pipeFDs[0]
	writeFD := pipeFDs[1]
	defer func() {
		if readFD >= 0 {
			_ = unix.Close(readFD)
		}
		if writeFD >= 0 {
			_ = unix.Close(writeFD)
		}
	}()

	oob := unix.UnixRights(readFD, readFD)
	n, oobn, sendErr := sender.WriteMsgUnix([]byte("m"), oob, nil)
	if sendErr != nil {
		t.Fatalf("WriteMsgUnix() error = %v", sendErr)
	}
	if n != 1 || oobn != len(oob) {
		t.Fatalf("WriteMsgUnix() short write: n=%d oobn=%d", n, oobn)
	}

	if err := unix.Close(readFD); err != nil {
		t.Fatalf("close original read fd error = %v", err)
	}
	readFD = -1

	receivedFD, md, err := recvFD(receiver)
	if err != nil {
		t.Fatalf("recvFD() error = %v", err)
	}
	if string(md) != "m" {
		t.Fatalf("unexpected metadata %q", string(md))
	}

	if err := unix.Close(receivedFD); err != nil {
		t.Fatalf("close primary received fd error = %v", err)
	}

	if _, err := unix.Write(writeFD, []byte{1}); !errors.Is(err, unix.EPIPE) {
		t.Fatalf("expected EPIPE with no leaked extra read fds, got %v", err)
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
