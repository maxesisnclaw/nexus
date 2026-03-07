//go:build linux

package transport

import (
	"bytes"
	"testing"

	"golang.org/x/sys/unix"
)

func BenchmarkCreateMemfdAndRead(b *testing.B) {
	payload := bytes.Repeat([]byte("a"), 1<<20)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		fd, err := CreateMemfd("bench", payload)
		if err != nil {
			b.Fatalf("CreateMemfd() error = %v", err)
		}
		data, err := ReadFDAll(fd, int64(len(payload)))
		if err != nil {
			_ = unix.Close(fd)
			b.Fatalf("ReadFDAll() error = %v", err)
		}
		if len(data) != len(payload) {
			_ = unix.Close(fd)
			b.Fatalf("unexpected payload size: %d", len(data))
		}
		if err := unix.Close(fd); err != nil {
			b.Fatalf("Close() error = %v", err)
		}
	}
}
