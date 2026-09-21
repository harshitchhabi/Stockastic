package universe_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"stockastic/api/internal/universe"
)

func write(t *testing.T, body string) string {
	p := filepath.Join(t.TempDir(), "u.json")
	_ = os.WriteFile(p, []byte(body), 0o600)
	return p
}

func TestLoadGoodFile(t *testing.T) {
	cs, err := universe.Load(write(t, `[{"symbol":"ABC","name":"Abc Ltd","sector":"IT","openPrice":12.34}]`))
	if err != nil || len(cs) != 1 || cs[0].Open != 1234 || cs[0].Sector != "IT" {
		t.Fatalf("%+v %v", cs, err)
	}
}

func TestBadFilesAreRefusedWithAReason(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"empty list":       {`[]`, "empty"},
		"bad json":         {`{`, "universe"},
		"lowercase symbol": {`[{"symbol":"abc","name":"x","openPrice":1}]`, "bad symbol"},
		"duplicate":        {`[{"symbol":"A","name":"x","openPrice":1},{"symbol":"A","name":"y","openPrice":2}]`, "duplicate"},
		"no name":          {`[{"symbol":"A","name":"","openPrice":1}]`, "no name"},
		"no price":         {`[{"symbol":"A","name":"x","openPrice":0}]`, "openPrice"},
	} {
		_, err := universe.Load(write(t, tc.body))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to mention %q", name, err, tc.want)
		}
	}
	if _, err := universe.Load(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("a missing file was accepted")
	}
}

func TestDefaultIsUsable(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range universe.Default() {
		if seen[c.Symbol] || c.Open <= 0 || c.Name == "" {
			t.Fatalf("bad default company %+v", c)
		}
		seen[c.Symbol] = true
	}
}
