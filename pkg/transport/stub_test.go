//go:build !linux

package transport

import (
	"errors"
	"testing"
)

func TestFDStubsReturnUnsupported(t *testing.T) {
	if err := sendFD(nil, 1, []byte("x")); !errors.Is(err, ErrFDUnsupported) {
		t.Fatalf("expected ErrFDUnsupported from sendFD, got %v", err)
	}
	if _, _, err := recvFD(nil); !errors.Is(err, ErrFDUnsupported) {
		t.Fatalf("expected ErrFDUnsupported from recvFD, got %v", err)
	}
}

func TestMemfdStubsReturnUnsupported(t *testing.T) {
	if _, err := CreateMemfd("name", []byte("payload")); err == nil {
		t.Fatal("expected CreateMemfd unsupported error")
	}
	if _, err := ReadFDAll(1, 1); err == nil {
		t.Fatal("expected ReadFDAll unsupported error")
	}
}
