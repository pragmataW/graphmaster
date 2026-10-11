package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Default batch thresholds (PLAN: ~500 files / ~200ms per commit).
const (
	DefaultBatchFiles = 500
	DefaultBatchDelay = 200 * time.Millisecond
)

// WriterOptions configure a Writer.
type WriterOptions struct {
	// MaxFiles commits once this many files are finished (default 500).
	MaxFiles int
	// MaxDelay commits at the next file boundary once the open
	// transaction is older than this (default 200ms).
	MaxDelay time.Duration
	// Bulk defers secondary indexes and FTS population for a fresh full
	// build. Call Store.FinishBulk after the writer is closed.
	Bulk bool
	// OnCommit, if set, is called after every commit that included at
	// least one finished file, with the total files committed so far.
	OnCommit func(filesCommitted int)
}

// Writer is the single batched writer for graph.db. It owns one connection
// and keeps an IMMEDIATE transaction open across several files, committing
// only at file boundaries (FileDone), so every file's changes are atomic.
//
// A Writer is not safe for concurrent use; drive it from one goroutine. The
// caller must hold the cross-process write Lock for the Writer's lifetime.
// The context passed to NewWriter is used for every operation.
type Writer struct {
	s    *Store
	ctx  context.Context
	conn *sql.Conn
	opt  WriterOptions

	stmts     map[string]*sql.Stmt
	inTx      bool
	txStart   time.Time
	filesInTx int
	committed int
	closed    bool

	newNames map[string]int64 // interned in the open transaction
	paths    map[int64]string // file id -> path, for FTS rows
}

// NewWriter starts a writer on a dedicated connection.
func (s *Store) NewWriter(ctx context.Context, opt WriterOptions) (*Writer, error) {
	if opt.MaxFiles <= 0 {
		opt.MaxFiles = DefaultBatchFiles
	}
	if opt.MaxDelay <= 0 {
		opt.MaxDelay = DefaultBatchDelay
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	w := &Writer{
		s: s, ctx: ctx, conn: conn, opt: opt,
		stmts:    make(map[string]*sql.Stmt),
		newNames: make(map[string]int64),
		paths:    make(map[int64]string),
	}
	if opt.Bulk {
		if err := w.enterBulk(); err != nil {
			_ = conn.Close()
			return nil, err
		}
	}
	return w, nil
}

// stmt returns a prepared statement for q, preparing it on first use.
func (w *Writer) stmt(q string) (*sql.Stmt, error) {
	if st, ok := w.stmts[q]; ok {
		return st, nil
	}
	st, err := w.conn.PrepareContext(w.ctx, q)
	if err != nil {
		return nil, fmt.Errorf("store: prepare: %w", err)
	}
	w.stmts[q] = st
	return st, nil
}

func (w *Writer) exec(q string, args ...any) (sql.Result, error) {
	if err := w.begin(); err != nil {
		return nil, err
	}
	st, err := w.stmt(q)
	if err != nil {
		return nil, err
	}
	return st.ExecContext(w.ctx, args...)
}

// queryID runs a statement returning a single integer (e.g. RETURNING id).
// It returns sql.ErrNoRows when the statement produced no row.
func (w *Writer) queryID(q string, args ...any) (int64, error) {
	if err := w.begin(); err != nil {
		return 0, err
	}
	st, err := w.stmt(q)
	if err != nil {
		return 0, err
	}
	var id int64
	err = st.QueryRowContext(w.ctx, args...).Scan(&id)
	return id, err
}

func (w *Writer) begin() error {
	if w.closed {
		return errors.New("store: writer closed")
	}
	if w.inTx {
		return nil
	}
	if _, err := w.conn.ExecContext(w.ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	w.inTx = true
	w.txStart = time.Now()
	return nil
}

// FileDone marks the end of one file's changes and commits when the batch
// thresholds are reached.
func (w *Writer) FileDone() error {
	w.filesInTx++
	if w.filesInTx >= w.opt.MaxFiles || (w.inTx && time.Since(w.txStart) >= w.opt.MaxDelay) {
		return w.commit()
	}
	return nil
}

// Flush commits the open transaction, if any. Call it at file boundaries
// only (e.g. when the producer is idle).
func (w *Writer) Flush() error { return w.commit() }

func (w *Writer) commit() error {
	if !w.inTx {
		w.filesInTx = 0
		return nil
	}
	if _, err := w.conn.ExecContext(w.ctx, "COMMIT"); err != nil {
		_ = w.rollback()
		return fmt.Errorf("store: commit: %w", err)
	}
	w.inTx = false
	w.s.names.putAll(w.newNames)
	clear(w.newNames)
	files := w.filesInTx
	w.committed += files
	w.filesInTx = 0
	if files > 0 && w.opt.OnCommit != nil {
		w.opt.OnCommit(w.committed)
	}
	return nil
}

// Abort rolls back everything since the last commit. The writer stays
// usable.
func (w *Writer) Abort() error { return w.rollback() }

func (w *Writer) rollback() error {
	w.filesInTx = 0
	clear(w.newNames)
	clear(w.paths) // ids of rolled-back files may be reused
	if !w.inTx {
		return nil
	}
	w.inTx = false
	_, err := w.conn.ExecContext(context.Background(), "ROLLBACK")
	return err
}

// Close commits pending work and releases the connection.
func (w *Writer) Close() error {
	if w.closed {
		return nil
	}
	err := w.commit()
	if err != nil {
		_ = w.rollback()
	}
	w.closed = true
	for _, st := range w.stmts {
		_ = st.Close()
	}
	if cerr := w.conn.Close(); err == nil {
		err = cerr
	}
	return err
}

// Exec runs an arbitrary write inside the current batch, for operations not
// covered by typed helpers (e.g. resolver edge updates).
func (w *Writer) Exec(query string, args ...any) (sql.Result, error) {
	return w.exec(query, args...)
}

// Query reads through the writer's connection, observing uncommitted
// batch state. It does not start a transaction.
func (w *Writer) Query(query string, args ...any) (*sql.Rows, error) {
	return w.conn.QueryContext(w.ctx, query, args...)
}

// QueryRow is the single-row form of Query.
func (w *Writer) QueryRow(query string, args ...any) *sql.Row {
	return w.conn.QueryRowContext(w.ctx, query, args...)
}

// SetMeta upserts a meta value inside the current batch.
func (w *Writer) SetMeta(key, value string) error {
	_, err := w.exec(`INSERT OR REPLACE INTO meta(key, value) VALUES(?, ?)`, key, value)
	return err
}

// Intern returns the id of name, inserting it if needed.
func (w *Writer) Intern(name string) (int64, error) {
	if id, ok := w.s.names.get(name); ok {
		return id, nil
	}
	if id, ok := w.newNames[name]; ok {
		return id, nil
	}
	id, err := w.queryID(`INSERT INTO names(name) VALUES(?) ON CONFLICT(name) DO NOTHING RETURNING id`, name)
	if err == nil {
		w.newNames[name] = id
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	// Already present and committed (by us earlier or another process).
	id, err = w.queryID(`SELECT id FROM names WHERE name = ?`, name)
	if err != nil {
		return 0, err
	}
	w.s.names.put(name, id)
	return id, nil
}
