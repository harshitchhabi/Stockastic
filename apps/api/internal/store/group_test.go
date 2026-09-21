package store

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestEveryAcknowledgedAppendIsOnDiskAndEachWritersOrderIsKept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal")
	l, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const writers, each = 16, 100
	var acked atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				if err := l.Append(KindTrade, map[string]int{"w": w, "i": i}); err != nil {
					t.Error(err)
					return
				}
				acked.Add(1)
			}
		}(w)
	}
	wg.Wait()

	// Read the file independently of the log object, as a crashed-and-restarted process would.
	f, _ := os.Open(path)
	defer f.Close()
	last := map[int]int{}
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var e struct {
			V struct{ W, I int }
		}
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("a record on disk is not whole: %v", err)
		}
		if prev, ok := last[e.V.W]; ok && e.V.I != prev+1 {
			t.Fatalf("writer %d: record %d came after %d", e.V.W, e.V.I, prev)
		}
		last[e.V.W] = e.V.I
		n++
	}
	if int64(n) != acked.Load() {
		t.Fatalf("%d acknowledged appends but %d records on disk", acked.Load(), n)
	}
	_ = l.Close()
}

func TestAfterADiskFailureNothingIsAcknowledgedAgain(t *testing.T) {
	l, err := OpenFile(filepath.Join(t.TempDir(), "wal"))
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append(KindUser, 1); err != nil {
		t.Fatal(err)
	}
	// Pull the file out from under the log: the next write/sync must fail.
	l.mu.Lock()
	_ = l.f.Close()
	l.mu.Unlock()

	if err := l.Append(KindUser, 2); err == nil {
		t.Fatal("an append to a failed disk was acknowledged")
	}
	for i := 0; i < 3; i++ {
		if err := l.Append(KindUser, 3); err == nil {
			t.Fatal("the log kept accepting writes after a failure, so it could report data safe that is not")
		}
	}
}
