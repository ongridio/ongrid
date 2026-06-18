package dbcli

import (
	"database/sql"
	"fmt"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql" // register mysql driver for database/sql
)

// defaultPoolSize is the max open connections per pool entry.
const defaultPoolSize = 5

// defaultIdleTimeout is how long an idle connection stays in the pool.
const defaultIdleTimeout = 10 * time.Minute

// poolEntry holds one *sql.DB plus its last-used timestamp.
type poolEntry struct {
	db       *sql.DB
	lastUsed time.Time
	dsn      string
}

// Pool is a shared collection of *sql.DB instances keyed by DSN.
// Thread-safe. Idle connections are evicted after IdleTimeout.
type Pool struct {
	mu       sync.Mutex
	entries  map[string]*poolEntry
	maxOpen  int
	timeout  time.Duration
	stopCh   chan struct{}
	stopped  bool
}

// GlobalPool is the shared pool used by the db_exec_query skill.
var GlobalPool = NewPool(defaultPoolSize, defaultIdleTimeout)

// NewPool creates a pool. maxOpen is per-DSN; timeout is idle eviction.
func NewPool(maxOpen int, idleTimeout time.Duration) *Pool {
	p := &Pool{
		entries: make(map[string]*poolEntry),
		maxOpen: maxOpen,
		timeout: idleTimeout,
		stopCh:  make(chan struct{}),
	}
	// Start eviction goroutine.
	go p.evictLoop()
	return p
}

// Get returns a *sql.DB for the given DSN. If one exists in the pool
// and is still healthy, it's returned; otherwise a new one is opened.
func (p *Pool) Get(dsn, driverName string) (*sql.DB, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.stopped {
		return nil, fmt.Errorf("pool: already stopped")
	}

	if entry, ok := p.entries[dsn]; ok {
		// Check if the connection is still alive.
		if err := entry.db.Ping(); err == nil {
			entry.lastUsed = time.Now()
			return entry.db, nil
		}
		// Stale connection — close and reopen.
		entry.db.Close()
		delete(p.entries, dsn)
	}

	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("pool: open: %w", err)
	}
	db.SetMaxOpenConns(p.maxOpen)
	db.SetMaxIdleConns(p.maxOpen)
	db.SetConnMaxLifetime(30 * time.Minute)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("pool: ping: %w", err)
	}

	p.entries[dsn] = &poolEntry{
		db:       db,
		lastUsed: time.Now(),
		dsn:      dsn,
	}
	return db, nil
}

// CloseAll closes every pooled connection. Called on edge shutdown.
func (p *Pool) CloseAll() {
	p.mu.Lock()
	p.stopped = true
	entries := p.entries
	p.entries = make(map[string]*poolEntry)
	p.mu.Unlock()

	for _, entry := range entries {
		entry.db.Close()
	}
}

// evictLoop periodically closes idle connections.
func (p *Pool) evictLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		p.mu.Lock()
		now := time.Now()
		for dsn, entry := range p.entries {
			if now.After(entry.lastUsed.Add(p.timeout)) {
				entry.db.Close()
				delete(p.entries, dsn)
			}
		}
		p.mu.Unlock()

		// Check if stopped.
		p.mu.Lock()
		stopped := p.stopped
		p.mu.Unlock()
		if stopped {
			return
		}
	}
}
