package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
)

// Rule says how much of one kind of record is worth keeping when the log is compacted.
type Rule struct {
	// Latest is how many of the newest records of this kind to keep per Key. Records of a kind with no
	// rule are always kept in full.
	//
	// With Latest 1, the newest content is written where the FIRST record of that key was, so anything that
	// must exist before later records (an account before its trades) still does.
	Latest int
	// Key groups records: records with the same key supersede each other. nil puts every record in one group.
	Key func(raw json.RawMessage) string
}

// Compaction reports what compacting did.
type Compaction struct {
	BeforeBytes, AfterBytes int64
	Kept, Dropped           int
}

type recPos struct {
	idx    int
	off    int64
	length int
}

// Compact rewrites the log without records that a newer record has replaced, so the file stays bounded
// however many times the server is restarted. It never drops anything a rule does not name, and it swaps
// the new file in with one atomic rename, so a crash at any point leaves either the old log or the new
// one, never a mixture. It must run while no server has the log open (it takes the same lock).
func Compact(path string, rules map[string]Rule) (Compaction, error) {
	var res Compaction
	_ = os.Remove(path + ".compact.tmp") // a leftover from a crash during an earlier compaction
	src, err := os.OpenFile(path, os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return res, nil
	}
	if err != nil {
		return res, err
	}
	closed := false
	closeSrc := func() {
		if !closed {
			src.Close()
			closed = true
		}
	}
	defer closeSrc()
	if err := lockFile(src); err != nil {
		return res, err
	}
	st, err := src.Stat()
	if err != nil {
		return res, err
	}
	res.BeforeBytes = st.Size()

	// Pass 1: find every record that a rule covers, and where the valid part of the file ends.
	type group struct{ recs []recPos }
	groups := map[string]map[string]*group{}
	r := bufio.NewReaderSize(src, 1<<20)
	var off int64
	n := 0
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			var e envelope
			if json.Unmarshal(bytes.TrimSpace(line), &e) != nil || e.K == "" {
				break // a torn tail: never acknowledged, and dropped by the rewrite
			}
			if rule, ok := rules[e.K]; ok && rule.Latest > 0 {
				key := ""
				if rule.Key != nil {
					key = rule.Key(e.V)
				}
				if groups[e.K] == nil {
					groups[e.K] = map[string]*group{}
				}
				g := groups[e.K][key]
				if g == nil {
					g = &group{}
					groups[e.K][key] = g
				}
				g.recs = append(g.recs, recPos{idx: n, off: off, length: len(line)})
			}
			off += int64(len(line))
			n++
		}
		if err != nil {
			if err != io.EOF {
				return res, err
			}
			break
		}
	}
	validBytes := off

	// Decide what to drop, and which record's content to write at which position.
	drop := make([]bool, n)
	replaceWith := map[int]recPos{}
	for kind, byKey := range groups {
		keep := rules[kind].Latest
		for _, g := range byKey {
			if len(g.recs) <= keep {
				continue
			}
			if keep == 1 {
				first, last := g.recs[0], g.recs[len(g.recs)-1]
				replaceWith[first.idx] = last
				for _, p := range g.recs[1:] {
					drop[p.idx] = true
				}
				continue
			}
			for _, p := range g.recs[:len(g.recs)-keep] {
				drop[p.idx] = true
			}
		}
	}
	dropped := 0
	for _, d := range drop {
		if d {
			dropped++
		}
	}
	if dropped == 0 && validBytes == res.BeforeBytes {
		res.Kept, res.AfterBytes = n, res.BeforeBytes
		return res, nil // nothing to do: leave the file alone
	}

	// Pass 2: write the survivors to a new file.
	tmpPath := path + ".compact.tmp"
	tmp, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return res, err
	}
	fail := func(err error) (Compaction, error) {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return res, err
	}
	w := bufio.NewWriterSize(tmp, 1<<20)
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	r = bufio.NewReaderSize(src, 1<<20)
	written := int64(0)
	for i := 0; i < n; i++ {
		line, err := r.ReadBytes('\n')
		if err != nil && err != io.EOF {
			return fail(err)
		}
		if drop[i] {
			continue
		}
		if p, ok := replaceWith[i]; ok {
			buf := make([]byte, p.length)
			if _, err := src.ReadAt(buf, p.off); err != nil {
				return fail(err)
			}
			line = buf
		}
		if _, err := w.Write(line); err != nil {
			return fail(err)
		}
		written += int64(len(line))
	}
	if err := w.Flush(); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return res, err
	}
	closeSrc() // Windows cannot replace a file that is still open
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return res, fmt.Errorf("store: replacing the log with its compacted copy: %w", err)
	}
	res.Kept, res.Dropped, res.AfterBytes = n-dropped, dropped, written
	return res, nil
}

// SortedKinds lists the kinds a rule set covers (for logging).
func SortedKinds(rules map[string]Rule) []string {
	out := make([]string, 0, len(rules))
	for k := range rules {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
