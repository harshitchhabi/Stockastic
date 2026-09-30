package app

import (
	"fmt"
	"strings"

	"stockastic/api/internal/store"
)

// The rules players read on the Rules page. This is only text: changing it never changes how the platform behaves
// (that comes from the rulebook file). Until an organiser edits it, the text is written from the rulebook's own
// numbers, so what players read matches what the platform does.
//
// Format, kept simple on purpose: a line starting "# " is a heading, a line starting "- " is a bullet point, and
// anything else is a paragraph. Nothing in it is ever treated as HTML.

const (
	settingRules  = "rulesText"
	maxRulesChars = 30000
)

// RulesView is the text shown to players and whether an organiser has changed it from the default.
type RulesView struct {
	Text   string `json:"text"`
	Edited bool   `json:"edited"`
}

// Rules is what players see on the Rules page.
func (a *App) Rules() RulesView {
	a.rulesMu.Lock()
	t := a.rulesText
	a.rulesMu.Unlock()
	if strings.TrimSpace(t) == "" {
		return RulesView{Text: a.DefaultRules()}
	}
	return RulesView{Text: t, Edited: true}
}

// SetRules replaces the text players see. Empty text brings back the default written from the rulebook.
func (a *App) SetRules(actor User, reason, text string) error {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if len([]rune(text)) > maxRulesChars {
		return bad("too_long", fmt.Sprintf("The rules can be at most %d characters.", maxRulesChars))
	}
	what := "Changed the rules shown to players"
	if strings.TrimSpace(text) == "" {
		what = "Put back the default rules shown to players"
	}
	return a.Do(actor, what, "rules", reason, func() error {
		if err := a.wal.Append(store.KindSetting, Setting{Key: settingRules, Text: text}); err != nil {
			return err
		}
		a.rulesMu.Lock()
		a.rulesText = text
		a.rulesMu.Unlock()
		a.Hub.ToAll("rules", map[string]any{})
		return nil
	})
}

func rupees(v float64) string {
	s := fmt.Sprintf("%.0f", v)
	// Indian grouping: the last three digits, then pairs.
	if len(s) <= 3 {
		return "₹" + s
	}
	head, tail := s[:len(s)-3], s[len(s)-3:]
	var parts []string
	for len(head) > 2 {
		parts = append([]string{head[len(head)-2:]}, parts...)
		head = head[:len(head)-2]
	}
	if head != "" {
		parts = append([]string{head}, parts...)
	}
	return "₹" + strings.Join(parts, ",") + "," + tail
}

func pct(v float64) string { return strings.TrimSuffix(fmt.Sprintf("%.1f", v), ".0") + "%" }

// DefaultRules is the players' rules written from the rulebook's numbers.
func (a *App) DefaultRules() string {
	r := a.RB
	perMin := "minute"
	if r.RateLimits.WindowSeconds != 60 {
		perMin = fmt.Sprintf("%d seconds", r.RateLimits.WindowSeconds)
	}
	teamLine := "- Each team is one account with one shared portfolio. All scoring is at team level."
	if a.TeamSize() > 1 {
		teamLine = fmt.Sprintf("- Teams have up to %d people and one shared portfolio. The person who registers the team is its leader; teammates join with the team code from the leader's Team page, each with their own login. Never share a password, even within your team.\n- Everyone in the team sees the same portfolio, prices and news. Only one person places the team's trades and moves its money: the leader, unless the leader or an organiser hands this to a teammate.", a.TeamSize())
	}
	lines := []string{
		"# The event",
		"The event has two phases. Phase 1 is a trading qualifier. In Phase 2 the best teams run investment funds and everyone else invests, directly or through the funds.",
		teamLine,
		fmt.Sprintf("- Every team starts with the same capital: %s.", rupees(r.Accounts.StartingCapital)),
		"",
		"# Trading",
		fmt.Sprintf("- You can trade only while the market is open. At most %d trades per %s.", r.RateLimits.TradesPerWindow, perMin),
		"- A trade happens at the price on screen when you press the button. If the price has moved, the platform refuses and shows you the new price. Trades are final.",
		"- Whole shares only. You cannot sell shares you do not own, and your cash can never go below zero.",
		fmt.Sprintf("- No buy may put more than %s of your total portfolio (cash, shares and fund units) in one company. This is checked only when you buy: if a holding later grows past it because its price rose, you do not have to sell.", pct(r.Market.MaxSingleStockPercent)),
		"- Prices are set by the market simulation; your trades do not move them.",
		"",
		"# News",
		"- News about companies, the economy and policy arrives during both phases. Some headlines are unconfirmed rumours that may later be denied: think about how reliable each one is.",
		"- A bull or bear run (the whole market turning up or down) is announced to everyone at the same moment.",
		fmt.Sprintf("- In Phase 2, fund managers see each news item %d seconds before everyone else. Nobody sees anything that others never see.", r.News.FundManagerLeadSeconds),
		"",
		"# Phase 1 and qualification",
		"- When Phase 1 ends, trading stops for everyone at the same moment. Your value is your cash plus your shares at that moment's prices.",
		fmt.Sprintf("- The top %d teams become fund managers. Ties are broken by the highest value reached during Phase 1, then by fewer trades, then by a coin toss supervised by the organisers.", r.Qualification.QualifyingTeams),
		fmt.Sprintf("- The qualifying teams are paired into %d funds: rank 1 with rank %d, rank 2 with rank %d, and so on, so every fund has a strong and a weaker team. Pairs are not chosen by the teams.", r.Qualification.FundCount, r.Qualification.QualifyingTeams, r.Qualification.QualifyingTeams-1),
		"",
		"# Phase 2: investing",
		"- Everyone else continues as investors with whatever they have at the end of Phase 1.",
		fmt.Sprintf("- Every investor team must keep at least %s of its portfolio in the funds.", pct(r.Fund.MandatoryAllocationPercent)),
		fmt.Sprintf("- Each fund starts at %s a unit. Putting money in buys units at the fund's current price, and the value of your units rises and falls with the fund.", rupees(r.Fund.LaunchNav)),
		fmt.Sprintf("- The smallest investment in a fund is %s or %s of your wallet, whichever is lower. At most %s of your wallet may be in any one fund.", rupees(r.Fund.MinInvestmentAbsolute), pct(r.Fund.MinInvestmentWalletPercent), pct(r.Fund.MaxSingleFundWalletPercent)),
		"- You can move money into or out of funds only while an allocation window is open. The last window is final: after it closes, fund positions are locked until the end.",
		"- To keep things balanced, each fund can take only its share of new money until every fund has had its share. If a fund is full for now, choose another.",
		"",
		"# Phase 2: fund managers",
		"- Each fund publishes a name (fictional, never a real company or bank), its investment philosophy, its risk profile and its strategy.",
		"- One member team places the fund's trades. Fund managers see news before everyone else (see News).",
		"",
		"# Prizes",
		"There are four prizes, decided by the organisers and announced at the end: the best fund, the best individual investor, the most creative investor and the best risk manager.",
		fmt.Sprintf("- To be considered for the most creative investor, write a short strategy log (2 to 3 sentences: what you did and why) at up to %d checkpoints in Phase 2, on the Funds page.", r.Prizes.Prize3.StrategyLogCheckpoints),
		"",
		"# Fair play",
		"- One account per team. No bots or scripts, no working with other teams, and no passing on news early.",
		"- Found a bug? Report it at the Help Desk. Using it instead means disqualification.",
		"- Serious breaches mean immediate disqualification. Minor first ones get a warning; a second means disqualification.",
		"",
		"# Problems and disputes",
		fmt.Sprintf("- Raise a problem at the Help Desk within %d minutes (or before the current trading block ends). A screenshot showing the time helps.", r.Disputes.RaiseWithinMinutes),
		"- The organisers decide, and their decision is final.",
	}
	return strings.Join(lines, "\n")
}
