package app

import (
	"sort"

	"stockastic/api/internal/dto"
)

// The organisers' standings: every team together, the investors on their own, and the funds on their own. Prizes
// are decided by the organisers from these; the platform does not work them out. They are live (not the players'
// five-minute snapshot).

type TeamStanding struct {
	Rank      int     `json:"rank"`
	AccountID string  `json:"accountId"`
	Team      string  `json:"team"`
	Role      string  `json:"role"`
	Status    string  `json:"status"`
	Value     float64 `json:"value"`
	ReturnPct float64 `json:"returnPct"`
	Trades    int     `json:"trades"`
}

type FundStanding struct {
	Rank       int      `json:"rank"`
	FundID     string   `json:"fundId"`
	Name       string   `json:"name"`
	Managers   []string `json:"managers"`
	NAV        float64  `json:"nav"`
	ReturnPct  float64  `json:"returnPct"`
	AUM        float64  `json:"aum"`
	Investors  int      `json:"investors"`
	FeePercent float64  `json:"feePercent"`
	Status     string   `json:"status"`
}

type Standings struct {
	Teams     []TeamStanding `json:"teams"`
	Investors []TeamStanding `json:"investors"`
	Funds     []FundStanding `json:"funds"`
}

// AdminStandings ranks every team by total value (cash, shares and fund units), the investors alone the same way,
// and the funds by their return since launch.
func (a *App) AdminStandings() Standings {
	navs := a.navs()
	start := float64(a.RB.StartingCapital())
	var all []TeamStanding
	for _, u := range a.users.all() {
		if u.IsAdmin {
			continue
		}
		v := a.totalValue(u.ID, navs)
		ret := 0.0
		if start > 0 {
			ret = (float64(v) - start) / start * 100
		}
		all = append(all, TeamStanding{AccountID: u.ID, Team: u.DisplayName, Role: u.Role, Status: u.Status, Value: dto.Rupees(v), ReturnPct: ret, Trades: len(a.MyTrades(u.ID))})
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Value != all[j].Value {
			return all[i].Value > all[j].Value
		}
		return all[i].Team < all[j].Team
	})
	out := Standings{Teams: []TeamStanding{}, Investors: []TeamStanding{}, Funds: []FundStanding{}}
	for i := range all {
		r := all[i]
		r.Rank = i + 1
		out.Teams = append(out.Teams, r)
		if r.Role == RoleInvestor {
			r.Rank = len(out.Investors) + 1
			out.Investors = append(out.Investors, r)
		}
	}
	for _, f := range a.Funds.Funds() {
		info := a.fundInfo(f, navs, "")
		st := "active"
		if f.Disqualified {
			st = "disqualified"
		}
		out.Funds = append(out.Funds, FundStanding{FundID: f.ID, Name: info.Name, Managers: info.Managers, NAV: info.NAV, ReturnPct: info.ReturnPct,
			AUM: info.AUM, Investors: info.Investors, FeePercent: info.FeePercent, Status: st})
	}
	sort.SliceStable(out.Funds, func(i, j int) bool { return out.Funds[i].ReturnPct > out.Funds[j].ReturnPct })
	for i := range out.Funds {
		out.Funds[i].Rank = i + 1
	}
	return out
}
