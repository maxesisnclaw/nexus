package transport

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"sync"
	"time"

	"github.com/flynn/noise"
	"github.com/vmihailenco/msgpack/v5"
)

const noiseAEADTagSize = 16

type noiseConn struct {
	raw    net.Conn
	cs     *noise.CipherState
	csRecv *noise.CipherState
	mu     sync.Mutex
	recvMu sync.Mutex
}

func (c *noiseConn) Send(msg *Message) error {
	if msg == nil {
		return errors.New("message is nil")
	}
	plaintext, err := msgpack.Marshal(msg)
	if err != nil {
		return fmt.Errorf("msgpack encode: %w", err)
	}
	if len(plaintext) > DefaultMaxMessageSize {
		return fmt.Errorf("message exceeds maximum size of %d bytes", DefaultMaxMessageSize)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	ciphertext, err := c.cs.Encrypt(nil, nil, plaintext)
	if err != nil {
		return fmt.Errorf("noise encrypt: %w", err)
	}
	if err := writeLengthPrefixed(c.raw, ciphertext); err != nil {
		return err
	}
	return nil
}

func (c *noiseConn) Recv() (*Message, error) {
	c.recvMu.Lock()
	defer c.recvMu.Unlock()

	maxCipherSize := DefaultMaxMessageSize + noiseAEADTagSize
	ciphertext, err := readLengthPrefixed(c.raw, maxCipherSize)
	if err != nil {
		return nil, err
	}
	plaintext, err := c.csRecv.Decrypt(nil, nil, ciphertext)
	if err != nil {
		return nil, fmt.Errorf("noise decrypt: %w", err)
	}
	if len(plaintext) > DefaultMaxMessageSize {
		return nil, fmt.Errorf("message exceeds maximum size of %d bytes", DefaultMaxMessageSize)
	}

	var msg Message
	if err := msgpack.Unmarshal(plaintext, &msg); err != nil {
		return nil, fmt.Errorf("msgpack decode: %w", err)
	}
	return &msg, nil
}

func (c *noiseConn) SendFd(int, []byte) error {
	return ErrFDUnsupported
}

func (c *noiseConn) RecvFd() (int, []byte, error) {
	return -1, nil, ErrFDUnsupported
}

func (c *noiseConn) SetReadDeadline(t time.Time) error {
	return c.raw.SetReadDeadline(t)
}

func (c *noiseConn) SetWriteDeadline(t time.Time) error {
	return c.raw.SetWriteDeadline(t)
}

func (c *noiseConn) Close() error {
	return c.raw.Close()
}

func writeLengthPrefixed(conn net.Conn, payload []byte) error {
	if len(payload) > math.MaxUint32 {
		return fmt.Errorf("payload too large: %d", len(payload))
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if err := writeAll(conn, header[:]); err != nil {
		return fmt.Errorf("write frame length: %w", err)
	}
	if len(payload) == 0 {
		return nil
	}
	if err := writeAll(conn, payload); err != nil {
		return fmt.Errorf("write frame payload: %w", err)
	}
	return nil
}

func readLengthPrefixed(conn net.Conn, maxLen int) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint32(header[:]))
	if maxLen > 0 && n > maxLen {
		return nil, fmt.Errorf("message exceeds maximum size of %d bytes", maxLen)
	}
	if n == 0 {
		return nil, nil
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func writeAll(conn net.Conn, data []byte) error {
	for len(data) > 0 {
		n, err := conn.Write(data)
		if err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}
