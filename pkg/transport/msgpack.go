package transport

import (
	"net"
	"sync"

	"github.com/vmihailenco/msgpack/v5"
)

type msgpackConn struct {
	raw net.Conn
	enc *msgpack.Encoder
	dec *msgpack.Decoder
	mu  sync.Mutex
}

func newMsgpackConn(c net.Conn) *msgpackConn {
	return &msgpackConn{
		raw: c,
		enc: msgpack.NewEncoder(c),
		dec: msgpack.NewDecoder(c),
	}
}

func (c *msgpackConn) Send(msg *Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.enc.Encode(msg)
}

func (c *msgpackConn) Recv() (*Message, error) {
	var m Message
	if err := c.dec.Decode(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (c *msgpackConn) Close() error {
	return c.raw.Close()
}
