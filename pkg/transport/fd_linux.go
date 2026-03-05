//go:build linux

package transport

import "net"

func sendFD(_ *net.UnixConn, _ int, _ []byte) error {
	return ErrFDUnsupported
}

func recvFD(_ *net.UnixConn) (int, []byte, error) {
	return -1, nil, ErrFDUnsupported
}
