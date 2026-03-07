//go:build !linux

package transport

import "errors"

// CreateMemfd is unsupported on non-linux builds.
func CreateMemfd(string, []byte) (int, error) {
	return -1, errors.New("memfd unsupported on non-linux")
}

// ReadFDAll is unsupported on non-linux builds.
func ReadFDAll(int, int64) ([]byte, error) {
	return nil, errors.New("fd read unsupported on non-linux")
}
