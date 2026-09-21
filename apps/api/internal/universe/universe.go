// Package universe is the list of tradable companies. The real 250-company list comes from the
// organisers; until it does, Default is a small PLACEHOLDER set of fictional companies so the platform
// can run end to end. Point UNIVERSE_PATH at a JSON file to replace it without changing any code:
//
//	[{"symbol":"ACME","name":"Acme Corp","sector":"Banking","openPrice":101.5}, ...]
package universe

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"

	"stockastic/api/internal/money"
)

type Company struct {
	Symbol string
	Name   string
	Sector string
	Open   money.Paise
}

var symbolRE = regexp.MustCompile(`^[A-Z0-9][A-Z0-9._-]{0,15}$`)

type fileEntry struct {
	Symbol    string  `json:"symbol"`
	Name      string  `json:"name"`
	Sector    string  `json:"sector"`
	OpenPrice float64 `json:"openPrice"`
}

// Load reads and validates a universe file. A bad file is an error: the event must not start on a
// half-understood company list.
func Load(path string) ([]Company, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var in []fileEntry
	if err := json.Unmarshal(b, &in); err != nil {
		return nil, fmt.Errorf("universe: %s: %w", path, err)
	}
	out := make([]Company, 0, len(in))
	seen := map[string]bool{}
	for i, e := range in {
		switch {
		case !symbolRE.MatchString(e.Symbol):
			return nil, fmt.Errorf("universe: entry %d: bad symbol %q", i, e.Symbol)
		case seen[e.Symbol]:
			return nil, fmt.Errorf("universe: duplicate symbol %q", e.Symbol)
		case e.Name == "":
			return nil, fmt.Errorf("universe: %s has no name", e.Symbol)
		case e.OpenPrice <= 0:
			return nil, fmt.Errorf("universe: %s needs a positive openPrice", e.Symbol)
		}
		seen[e.Symbol] = true
		out = append(out, Company{Symbol: e.Symbol, Name: e.Name, Sector: e.Sector, Open: money.FromRupees(e.OpenPrice)})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("universe: %s is empty", path)
	}
	return out, nil
}

// Default is the PLACEHOLDER universe.
func Default() []Company {
	c := func(sym, name, sector string, price float64) Company {
		return Company{Symbol: sym, Name: name, Sector: sector, Open: money.FromRupees(price)}
	}
	return []Company{
		c("ACME", "Acme Corp", "Banking", 101.50),
		c("GLOBEX", "Globex Industries", "IT", 245.20),
		c("INITECH", "Initech", "Energy", 58.75),
		c("UMBRELLA", "Umbrella Group", "Pharma", 312.40),
		c("STARK", "Stark Industries", "Auto", 890.10),
		c("WAYNE", "Wayne Enterprises", "FMCG", 154.30),
		c("WONKA", "Wonka Foods", "Metals", 42.90),
		c("HOOLI", "Hooli Systems", "Telecom", 176.60),
		c("PIEDPIPER", "Pied Piper", "Banking", 23.45),
		c("CYBERDYNE", "Cyberdyne", "IT", 67.80),
		c("TYRELL", "Tyrell Corp", "Energy", 129.00),
		c("SOYLENT", "Soylent Co", "Pharma", 34.20),
	}
}
