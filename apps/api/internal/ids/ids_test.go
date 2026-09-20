package ids

import (
	"regexp"
	"sync"
	"testing"
)

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewIsAWellFormedUUIDv4(t *testing.T) {
	for i := 0; i < 1000; i++ {
		if id := New(); !uuidV4.MatchString(id) {
			t.Fatalf("%q is not a UUIDv4", id)
		}
	}
}

func TestNewIsUniqueEvenUnderConcurrency(t *testing.T) {
	const workers, each = 16, 10_000
	out := make(chan string, workers*each)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				out <- New()
			}
		}()
	}
	wg.Wait()
	close(out)
	seen := make(map[string]struct{}, workers*each)
	for id := range out {
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id %s", id)
		}
		seen[id] = struct{}{}
	}
}
