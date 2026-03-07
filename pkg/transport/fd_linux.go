//go:build linux

package transport

import (
	"errors"
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

func sendFD(conn *net.UnixConn, fd int, metadata []byte) error {
	if conn == nil {
		return errors.New("nil unix connection")
	}
	if fd < 0 {
		return fmt.Errorf("invalid fd %d", fd)
	}
	if len(metadata) == 0 {
		metadata = []byte{0}
	}
	oob := unix.UnixRights(fd)
	n, oobn, err := conn.WriteMsgUnix(metadata, oob, nil)
	if err != nil {
		return fmt.Errorf("send fd: %w", err)
	}
	if n != len(metadata) || oobn != len(oob) {
		return errors.New("short send while passing fd")
	}
	return nil
}

func recvFD(conn *net.UnixConn) (int, []byte, error) {
	if conn == nil {
		return -1, nil, errors.New("nil unix connection")
	}
	payload := make([]byte, 64*1024)
	oob := make([]byte, unix.CmsgSpace(4*16))
	n, oobn, _, _, err := conn.ReadMsgUnix(payload, oob)
	if err != nil {
		return -1, nil, fmt.Errorf("recv fd: %w", err)
	}
	cmsgs, err := unix.ParseSocketControlMessage(oob[:oobn])
	if err != nil {
		return -1, nil, fmt.Errorf("parse control message: %w", err)
	}
	primaryFD := -1
	for _, cmsg := range cmsgs {
		fds, err := unix.ParseUnixRights(&cmsg)
		if err != nil {
			continue
		}
		for _, receivedFD := range fds {
			if primaryFD < 0 {
				primaryFD = receivedFD
				continue
			}
			_ = unix.Close(receivedFD)
		}
	}
	if primaryFD >= 0 {
		return primaryFD, payload[:n], nil
	}
	return -1, nil, errors.New("no fd found in message")
}
