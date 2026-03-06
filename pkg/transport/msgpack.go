package transport

import (
	"net"
	"sync"
	"time"

	"github.com/vmihailenco/msgpack/v5"
)

type msgpackConn struct {
	raw    net.Conn
	enc    *msgpack.Encoder
	dec    *msgpack.Decoder
	sendMu sync.Mutex
	recvMu sync.Mutex
}

func newMsgpackConn(c net.Conn) *msgpackConn {
	return &msgpackConn{
		raw: c,
		enc: msgpack.NewEncoder(c),
		dec: msgpack.NewDecoder(c),
	}
}

func (c *msgpackConn) Send(msg *Message) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	return c.enc.Encode(msg)
}

func (c *msgpackConn) Recv() (*Message, error) {
	c.recvMu.Lock()
	defer c.recvMu.Unlock()

	var m Message
	if err := c.dec.Decode(&m); err != nil {
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
