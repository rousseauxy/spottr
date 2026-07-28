package nntp

import (
	"context"
	"sync"
	"time"
)

// idleTimeout is how long a pooled connection may sit unused before we assume
// the provider has dropped it. Most Usenet servers close idle sessions within a
// few minutes.
const idleTimeout = 2 * time.Minute

// Pool is a bounded pool of authenticated NNTP connections.
//
// Without it, every spot view, NZB fetch and image fetch dialled a fresh
// TCP+TLS connection and re-authenticated. Providers cap concurrent
// connections per account, so an unauthenticated request flood translated
// directly into exhausting the operator's Usenet account — starving the sync
// engine and risking provider-side rate limiting. The semaphore makes
// NNTP_MAX_CONNS an actual ceiling rather than a config field nothing read.
type Pool struct {
	host string
	port int
	tls  bool
	user string
	pass string

	sem chan struct{} // capacity == max concurrent connections

	mu   sync.Mutex
	idle []*idleConn
}

type idleConn struct {
	client *Client
	putAt  time.Time
}

// NewPool creates a pool allowing at most maxConns concurrent connections.
func NewPool(host string, port int, useTLS bool, user, pass string, maxConns int) *Pool {
	if maxConns < 1 {
		maxConns = 1
	}
	return &Pool{
		host: host,
		port: port,
		tls:  useTLS,
		user: user,
		pass: pass,
		sem:  make(chan struct{}, maxConns),
	}
}

// Conn is a leased connection. Release must be called exactly once.
type Conn struct {
	*Client
	pool   *Pool
	broken bool
}

// Release returns the connection to the pool, or closes it if it was marked
// broken by an I/O error.
func (c *Conn) Release() {
	if c == nil || c.pool == nil {
		return
	}
	p := c.pool
	c.pool = nil

	if c.broken || c.Client == nil {
		if c.Client != nil {
			_ = c.Client.Close()
		}
		<-p.sem
		return
	}

	p.mu.Lock()
	p.idle = append(p.idle, &idleConn{client: c.Client, putAt: time.Now()})
	p.mu.Unlock()
	<-p.sem
}

// MarkBroken flags the connection as unusable so Release discards it. Call this
// whenever a command returns a transport error — reusing a connection whose
// protocol state is unknown corrupts the next caller's response.
func (c *Conn) MarkBroken() {
	if c != nil {
		c.broken = true
	}
}

// Get leases a connection, blocking until one is free or ctx is done.
func (p *Pool) Get(ctx context.Context) (*Conn, error) {
	select {
	case p.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	// Reuse a warm connection when we have one.
	for {
		p.mu.Lock()
		if len(p.idle) == 0 {
			p.mu.Unlock()
			break
		}
		ic := p.idle[len(p.idle)-1]
		p.idle = p.idle[:len(p.idle)-1]
		p.mu.Unlock()

		if time.Since(ic.putAt) > idleTimeout {
			_ = ic.client.Close()
			continue
		}
		return &Conn{Client: ic.client, pool: p}, nil
	}

	client, err := Dial(p.host, p.port, p.tls, p.user, p.pass)
	if err != nil {
		<-p.sem
		return nil, err
	}
	return &Conn{Client: client, pool: p}, nil
}

// Close tears down all idle connections. In-flight leases are unaffected.
func (p *Pool) Close() {
	p.mu.Lock()
	idle := p.idle
	p.idle = nil
	p.mu.Unlock()

	for _, ic := range idle {
		_ = ic.client.Close()
	}
}

// Reap closes connections that have been idle beyond the timeout. Run
// periodically so a quiet instance does not hold sockets open against the
// provider's connection allowance.
func (p *Pool) Reap() {
	p.mu.Lock()
	keep := p.idle[:0]
	var stale []*idleConn
	for _, ic := range p.idle {
		if time.Since(ic.putAt) > idleTimeout {
			stale = append(stale, ic)
		} else {
			keep = append(keep, ic)
		}
	}
	p.idle = keep
	p.mu.Unlock()

	for _, ic := range stale {
		_ = ic.client.Close()
	}
}

// RunReaper reaps idle connections until ctx is cancelled.
func (p *Pool) RunReaper(ctx context.Context) {
	t := time.NewTicker(idleTimeout / 2)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			p.Close()
			return
		case <-t.C:
			p.Reap()
		}
	}
}
