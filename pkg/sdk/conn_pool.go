package sdk

import (
	"context"
	"errors"
	"sync"

	"nexus/pkg/transport"
)

const defaultMaxIdlePerEndpoint = 4

type connectionPool interface {
	Acquire(ctx context.Context, endpoint transport.ServiceEndpoint) (transport.Conn, error)
	Release(endpoint transport.ServiceEndpoint, conn transport.Conn, reusable bool)
	Close() error
}

type dialConnectionPool struct {
	router *transport.Router

	mu                 sync.Mutex
	closed             bool
	maxIdlePerEndpoint int
	idle               map[string][]transport.Conn
}

func newConnectionPool(router *transport.Router) connectionPool {
	return &dialConnectionPool{
		router:             router,
		maxIdlePerEndpoint: defaultMaxIdlePerEndpoint,
		idle:               make(map[string][]transport.Conn),
	}
}

func (p *dialConnectionPool) Acquire(ctx context.Context, endpoint transport.ServiceEndpoint) (transport.Conn, error) {
	key := endpointPoolKey(endpoint)

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, errors.New("connection pool is closed")
	}
	if conns := p.idle[key]; len(conns) > 0 {
		conn := conns[len(conns)-1]
		p.idle[key] = conns[:len(conns)-1]
		if len(p.idle[key]) == 0 {
			delete(p.idle, key)
		}
		p.mu.Unlock()
		return conn, nil
	}
	p.mu.Unlock()

	return p.router.Dial(ctx, endpoint)
}

func (p *dialConnectionPool) Release(endpoint transport.ServiceEndpoint, conn transport.Conn, reusable bool) {
	if conn == nil {
		return
	}
	if !reusable {
		_ = conn.Close()
		return
	}

	key := endpointPoolKey(endpoint)
	p.mu.Lock()
	if p.closed || p.maxIdlePerEndpoint <= 0 {
		p.mu.Unlock()
		_ = conn.Close()
		return
	}
	idleConns := p.idle[key]
	if len(idleConns) >= p.maxIdlePerEndpoint {
		p.mu.Unlock()
		_ = conn.Close()
		return
	}
	p.idle[key] = append(idleConns, conn)
	p.mu.Unlock()
}

func (p *dialConnectionPool) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	var conns []transport.Conn
	for _, idleConns := range p.idle {
		conns = append(conns, idleConns...)
	}
	p.idle = nil
	p.mu.Unlock()

	var closeErr error
	for _, conn := range conns {
		closeErr = errors.Join(closeErr, conn.Close())
	}
	return closeErr
}

func endpointPoolKey(endpoint transport.ServiceEndpoint) string {
	if endpoint.Local && endpoint.UDSAddr != "" {
		return "uds:" + endpoint.UDSAddr
	}
	if endpoint.TCPAddr != "" {
		return "tcp:" + endpoint.TCPAddr
	}
	return "uds:" + endpoint.UDSAddr
}
