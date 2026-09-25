package store

import "context"

// Records that exist to be looked at later rather than to rebuild the platform's state. The server writes them to
// the durable log like everything else; replay ignores them.
const (
	// KindActivity is something a person did: signed up, signed in, connected, disconnected.
	KindActivity = "activity"
	// KindWallets is every team's cash and total value at one moment (written once a minute).
	KindWallets = "wallets"
)

// Activity is one thing a person did.
type Activity struct {
	Account   string `json:"account"`
	Type      string `json:"type"` // signup, login, login_failed, connect, disconnect
	IP        string `json:"ip,omitempty"`
	UserAgent string `json:"ua,omitempty"`
	Detail    string `json:"detail,omitempty"`
	At        int64  `json:"at"` // milliseconds
}

// Wallets is every team's [cash, total value] in paise at one moment.
type Wallets struct {
	At int64               `json:"at"` // milliseconds
	W  map[string][2]int64 `json:"w"`
}

// History answers questions about the past from the lookup tables. It is only there when PostgreSQL is in use.
type History interface {
	// WalletHistory is a team's cash and value over time, oldest first, thinned to at most max points.
	WalletHistory(ctx context.Context, account string, max int) ([]WalletPoint, error)
	// Activity is what a person did, newest first.
	Activity(ctx context.Context, account string, limit int) ([]ActivityRow, error)
	// Ledger is every change to a team's cash and shares, newest first.
	Ledger(ctx context.Context, account string, limit int) ([]LedgerRow, error)
	// SharedAddresses lists addresses that more than one account signed in from.
	SharedAddresses(ctx context.Context) ([]SharedAddress, error)
	// Behind is how many records the lookup tables have yet to take in.
	Behind(ctx context.Context) (int64, error)
}

type WalletPoint struct {
	At    int64 `json:"at"`
	Cash  int64 `json:"cash"`
	Value int64 `json:"value"`
}

type ActivityRow struct {
	At        int64  `json:"at"`
	Type      string `json:"type"`
	IP        string `json:"ip"`
	UserAgent string `json:"userAgent"`
	Detail    string `json:"detail"`
}

type LedgerRow struct {
	At          int64  `json:"at"`
	Type        string `json:"type"`
	Symbol      string `json:"symbol"`
	SharesDelta int64  `json:"sharesDelta"`
	CashDelta   int64  `json:"cashDelta"`
}

type SharedAddress struct {
	IP       string   `json:"ip"`
	Accounts []string `json:"accounts"`
}
