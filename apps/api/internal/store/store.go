// Package store is durable storage. Everything the platform must not lose (accounts, every matched
// order and fill, audit entries, the event clock, news releases, disputes) goes through one interface,
// an append-only Log, and is rebuilt from it on start.
//
// FileLog is a fsync'd write-ahead file: an append returns only after the record is on disk, which is
// what lets the engine acknowledge an order only once it is durable. A crash can leave one torn line at
// the end of the file; that record was never acknowledged, so it is ignored on replay. A Postgres Log
// can replace FileLog behind the same interface without touching the callers.
package store

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"stockastic/api/internal/engine"
)

// ErrLocked means another process already has this log open. Two servers writing one log would corrupt
// it, so the second refuses to start.
var ErrLocked = errors.New("store: the data log is already in use by another running server")

// Record kinds.
const (
	KindUser     = "user"
	KindBatch    = "batch"
	KindAudit    = "audit"
	KindClock    = "clock"
	KindNews     = "news"
	KindTicket   = "ticket"
	KindGrant    = "grant" // shares given (Qty > 0) or taken back (Qty < 0)
	KindCash     = "cash"
	KindAnnounce = "announce"
	KindPause    = "pause"
)

// Log is an append-only, replayable record of everything durable.
type Log interface {
	// Append durably stores one record. It returns only once the record survives a crash.
	Append(kind string, v any) error
	// Replay calls fn for every stored record, oldest first.
	Replay(fn func(kind string, raw json.RawMessage) error) error
	Close() error
}

type envelope struct {
	K  string          `json:"k"`
	At time.Time       `json:"at"`
	V  json.RawMessage `json:"v"`
}

// FileLog is a Log backed by one fsync'd file.
type FileLog struct {
	mu   sync.Mutex
	cond *sync.Cond
	f    *os.File
	w    *bufio.Writer
	path string

	// Group commit: appends made while a disk sync is running share the next one, so many orders in
	// different companies cost one fsync between them instead of one each.
	pending int64 // records written into the buffer so far
	synced  int64 // records known to be on disk
	syncing bool
	// broken is set if a sync ever fails. After that nothing is acknowledged: the file's state is
	// unknown, and pretending otherwise could lose data that was reported as safe.
	broken error
	// Torn is how many trailing bytes of a torn record were found (and truncated) when the file was opened.
	Torn int
}

// OpenFile opens (creating if needed) the log at path. A torn final record from a crash is truncated.
func OpenFile(path string) (*FileLog, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, err
	}
	l := &FileLog{f: f, path: path}
	l.cond = sync.NewCond(&l.mu)
	good, err := l.validLength()
	if err != nil {
		f.Close()
		return nil, err
	}
	st, _ := f.Stat()
	if st != nil && st.Size() > good {
		l.Torn = int(st.Size() - good)
		if err := f.Truncate(good); err != nil {
			f.Close()
			return nil, err
		}
	}
	if _, err := f.Seek(good, 0); err != nil {
		f.Close()
		return nil, err
	}
	l.w = bufio.NewWriterSize(f, 64<<10)
	return l, nil
}

// validLength is the byte length of the file up to and including the last complete, parseable line.
func (l *FileLog) validLength() (int64, error) {
	if _, err := l.f.Seek(0, 0); err != nil {
		return 0, err
	}
	r := bufio.NewReaderSize(l.f, 1<<20)
	var good int64
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			var e envelope
			if json.Unmarshal(bytes.TrimSpace(line), &e) != nil || e.K == "" {
				return good, nil
			}
			good += int64(len(line))
		}
		if err != nil {
			return good, nil
		}
	}
}

func (l *FileLog) Append(kind string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("store: encode %s: %w", kind, err)
	}
	line, err := json.Marshal(envelope{K: kind, At: time.Now().UTC(), V: raw})
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return errors.New("store: log closed")
	}
	if l.broken != nil {
		return l.broken
	}
	if _, err := l.w.Write(append(line, '\n')); err != nil {
		l.broken = err
		l.cond.Broadcast()
		return err
	}
	l.pending++
	mine := l.pending

	// Wait until a sync that covers this record has finished; if none is running, run it.
	for l.synced < mine {
		if l.broken != nil {
			return l.broken
		}
		if l.syncing {
			l.cond.Wait()
			continue
		}
		l.syncing = true
		upTo := l.pending
		err := l.w.Flush()
		f := l.f
		l.mu.Unlock()
		if err == nil {
			err = f.Sync() // the slow part; other appenders keep filling the buffer meanwhile
		}
		l.mu.Lock()
		l.syncing = false
		if err != nil {
			l.broken = err
		} else if upTo > l.synced {
			l.synced = upTo
		}
		l.cond.Broadcast()
	}
	return nil
}

func (l *FileLog) Replay(fn func(string, json.RawMessage) error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	rf, err := os.Open(l.path)
	if err != nil {
		return err
	}
	defer rf.Close()
	r := bufio.NewReaderSize(rf, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			var e envelope
			if json.Unmarshal(bytes.TrimSpace(line), &e) != nil {
				return nil // torn tail: never acknowledged
			}
			if err := fn(e.K, e.V); err != nil {
				return err
			}
		}
		if err != nil {
			return nil
		}
	}
}

func (l *FileLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for l.syncing {
		l.cond.Wait()
	}
	if l.f == nil {
		return nil
	}
	_ = l.w.Flush()
	_ = l.f.Sync()
	err := l.f.Close()
	l.f = nil
	l.cond.Broadcast()
	return err
}

// MemLog is a Log that keeps records in memory, for tests. Fail makes Append return an error.
type MemLog struct {
	mu   sync.Mutex
	recs []envelope
	Fail error
}

func NewMem() *MemLog { return &MemLog{} }

func (m *MemLog) Append(kind string, v any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Fail != nil {
		return m.Fail
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	m.recs = append(m.recs, envelope{K: kind, At: time.Now(), V: raw})
	return nil
}

func (m *MemLog) Replay(fn func(string, json.RawMessage) error) error {
	m.mu.Lock()
	recs := append([]envelope(nil), m.recs...)
	m.mu.Unlock()
	for _, e := range recs {
		if err := fn(e.K, e.V); err != nil {
			return err
		}
	}
	return nil
}

func (m *MemLog) Close() error { return nil }

// Journal adapts a Log to engine.Journal: a matching step is acknowledged only once it is durable.
type Journal struct {
	Log Log
	// Observe, if set, is told how long each commit took and whether it failed (for the health page).
	Observe func(d time.Duration, err error)
}

func (j Journal) Commit(_ context.Context, b engine.Batch) error {
	start := time.Now()
	err := j.Log.Append(KindBatch, b)
	if j.Observe != nil {
		j.Observe(time.Since(start), err)
	}
	return err
}
