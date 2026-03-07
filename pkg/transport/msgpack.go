package transport

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/vmihailenco/msgpack/v5"
)

const (
	// DefaultMaxMessageSize limits one decoded msgpack message to 64 MiB.
	DefaultMaxMessageSize = 64 * 1024 * 1024
)

var errMessageTooLarge = errors.New("msgpack message too large")

// MsgpackConnOption configures msgpack connection behavior.
type MsgpackConnOption func(*msgpackConn)

// MsgpackEnvelopeConn sends and receives msgpack transport messages.
type MsgpackEnvelopeConn interface {
	Send(msg *Message) error
	Recv() (*Message, error)
	SetReadDeadline(t time.Time) error
	Close() error
}

type msgpackConn struct {
	raw           net.Conn
	enc           *msgpack.Encoder
	dec           *msgpack.Decoder
	maxMsgSize    int64
	limitedReader *messageLimitedReader
	sendMu        sync.Mutex
	recvMu        sync.Mutex
}

type messageLimitedReader struct {
	reader    io.Reader
	max       int64
	remaining int64
	exceeded  bool
}

func (r *messageLimitedReader) reset() {
	r.remaining = r.max
	r.exceeded = false
}

func (r *messageLimitedReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		r.exceeded = true
		return 0, errMessageTooLarge
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.reader.Read(p)
	r.remaining -= int64(n)
	return n, err
}

// WithMaxMessageSize sets the maximum bytes allowed per decoded message.
func WithMaxMessageSize(maxMsgSize int64) MsgpackConnOption {
	return func(c *msgpackConn) {
		if maxMsgSize > 0 {
			c.maxMsgSize = maxMsgSize
		}
	}
}

// NewMsgpackConn creates a msgpack message connection with optional settings.
func NewMsgpackConn(c net.Conn, opts ...MsgpackConnOption) MsgpackEnvelopeConn {
	return newMsgpackConn(c, opts...)
}

func newMsgpackConn(c net.Conn, opts ...MsgpackConnOption) *msgpackConn {
	conn := &msgpackConn{
		raw:        c,
		enc:        msgpack.NewEncoder(c),
		maxMsgSize: DefaultMaxMessageSize,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(conn)
		}
	}
	if conn.maxMsgSize <= 0 {
		conn.maxMsgSize = DefaultMaxMessageSize
	}
	conn.limitedReader = &messageLimitedReader{
		reader: c,
		max:    conn.maxMsgSize,
	}
	conn.limitedReader.reset()
	conn.dec = msgpack.NewDecoder(conn.limitedReader)
	return conn
}

func (c *msgpackConn) Send(msg *Message) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	return c.enc.Encode(msg)
}

func (c *msgpackConn) Recv() (*Message, error) {
	c.recvMu.Lock()
	defer c.recvMu.Unlock()

	c.limitedReader.reset()
	var m Message
	if err := c.dec.Decode(&m); err != nil {
		if errors.Is(err, errMessageTooLarge) || c.limitedReader.exceeded {
			return nil, fmt.Errorf("message exceeds maximum size of %d bytes", c.maxMsgSize)
		}
		return nil, err
	}
	return &m, nil
}

func (c *msgpackConn) SetReadDeadline(t time.Time) error {
	return c.raw.SetReadDeadline(t)
}

func (c *msgpackConn) Close() error {
	return c.raw.Close()
}
