//go:build linux

package transport

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// CreateMemfd allocates an anonymous in-memory file and writes payload into it.
func CreateMemfd(name string, payload []byte) (int, error) {
	if name == "" {
		name = "nexus"
	}
	fd, err := unix.MemfdCreate(name, unix.MFD_CLOEXEC)
	if err != nil {
		return -1, fmt.Errorf("memfd create: %w", err)
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	if _, err := f.Write(payload); err != nil {
		return -1, fmt.Errorf("memfd write: %w", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		return -1, fmt.Errorf("memfd rewind: %w", err)
	}
	dupFD, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("memfd dup: %w", err)
	}
	return dupFD, nil
}

// ReadFDAll reads all bytes from an fd while preserving current offset behavior.
func ReadFDAll(fd int, maxBytes int64) ([]byte, error) {
	dupFD, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("dup fd: %w", err)
	}
	if _, err := unix.Seek(dupFD, 0, 0); err != nil {
		_ = unix.Close(dupFD)
		return nil, fmt.Errorf("seek fd: %w", err)
	}
	f := os.NewFile(uintptr(dupFD), "nexus-fd")
	if f == nil {
		_ = unix.Close(dupFD)
		return nil, fmt.Errorf("invalid fd %d", fd)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read fd: %w", err)
	}
	if int64(len(b)) > maxBytes {
		return nil, fmt.Errorf("fd payload exceeds limit of %d bytes", maxBytes)
	}
	return b, nil
}
