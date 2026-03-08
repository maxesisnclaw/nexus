package sdk

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/maxesisn/nexus/pkg/transport"
)

func TestConnectionPoolAcquireCanceledContextKeepsIdleConnection(t *testing.T) {
	pool := &dialConnectionPool{
		maxIdlePerEndpoint: defaultMaxIdlePerEndpoint,
		idle:               make(map[string][]transport.Conn),
	}
	endpoint := transport.ServiceEndpoint{
		Local:   true,
		UDSAddr: "/tmp/nexus-conn-pool-test.sock",
	}
	conn := &connPoolTestConn{}
	pool.Release(endpoint, conn, true)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := pool.Acquire(ctx, endpoint)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire() error = %v, want %v", err, context.Canceled)
	}
	if got != nil {
		t.Fatalf("Acquire() conn = %v, want nil when context is canceled", got)
	}

	got, err = pool.Acquire(context.Background(), endpoint)
	if err != nil {
		t.Fatalf("Acquire() after canceled attempt error = %v", err)
	}
	if got != conn {
		t.Fatalf("Acquire() returned unexpected connection after canceled attempt")
	}
}

type connPoolTestConn struct{}

func (c *connPoolTestConn) Send(*transport.Message) error { return nil }

func (c *connPoolTestConn) Recv() (*transport.Message, error) { return nil, nil }

func (c *connPoolTestConn) SendFd(int, []byte) error { return nil }

func (c *connPoolTestConn) RecvFd() (int, []byte, error) { return 0, nil, nil }

func (c *connPoolTestConn) SetReadDeadline(time.Time) error { return nil }

func (c *connPoolTestConn) SetWriteDeadline(time.Time) error { return nil }

func (c *connPoolTestConn) Close() error { return nil }
