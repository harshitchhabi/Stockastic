package engine

import (
	"sort"
	"time"

	"stockastic/api/internal/ids"
	"stockastic/api/internal/money"
)

// level is one price's FIFO queue of resting orders.
type level struct {
	price  money.Paise
	orders []*Order
}

func (l *level) qty() (q int64) {
	for _, o := range l.orders {
		q += o.Remaining
	}
	return q
}

// book is a single-symbol limit order book. It is owned by exactly one goroutine and has no locking.
type book struct {
	symbol string
	bids   []*level // sorted best-first: price descending
	asks   []*level // sorted best-first: price ascending
	byID   map[string]*Order
	seq    uint64
}

func newBook(symbol string) *book {
	return &book{symbol: symbol, byID: make(map[string]*Order)}
}

func (b *book) side(s Side) *[]*level {
	if s == Buy {
		return &b.bids
	}
	return &b.asks
}

// better reports whether price a has priority over price b on the given side.
func better(s Side, a, b money.Paise) bool {
	if s == Buy {
		return a > b
	}
	return a < b
}

func crosses(taker Side, takerPrice, makerPrice money.Paise) bool {
	if taker == Buy {
		return takerPrice >= makerPrice
	}
	return takerPrice <= makerPrice
}

// planned is one fill the plan intends to make against a resting order.
type planned struct {
	maker *Order
	qty   int64
}

// plan is the read-only result of matching an order: nothing in the book has been touched.
type plan struct {
	taker   Order // post-match state (Remaining, Status)
	fills   []planned
	makers  []Order // resting orders in their post-fill state, one per fill
	nextSeq uint64
}

// planSubmit walks the opposite side in price-time priority WITHOUT mutating anything, so the
// caller can commit it durably first and apply it only on success.
func (b *book) planSubmit(n NewOrder, now time.Time) plan {
	taker := Order{
		ID: ids.New(), ClientOrderID: n.ClientOrderID, AccountID: n.AccountID, Symbol: b.symbol,
		Side: n.Side, Price: n.Price, Qty: n.Qty, Remaining: n.Qty, Status: StatusOpen,
		Seq: b.seq + 1, CreatedAt: now,
	}
	p := plan{nextSeq: taker.Seq}
	opposite := b.side(opposite(n.Side))
	left := taker.Remaining
scan:
	for _, lv := range *opposite {
		if !crosses(n.Side, n.Price, lv.price) {
			break
		}
		for _, m := range lv.orders {
			if left == 0 {
				break scan
			}
			q := min(left, m.Remaining)
			post := *m
			post.Remaining -= q
			if post.Remaining == 0 {
				post.Status = StatusFilled
			} else {
				post.Status = StatusPartiallyFilled
			}
			p.fills = append(p.fills, planned{maker: m, qty: q})
			p.makers = append(p.makers, post)
			left -= q
		}
	}
	taker.Remaining = left
	switch {
	case left == 0:
		taker.Status = StatusFilled
	case left < taker.Qty:
		taker.Status = StatusPartiallyFilled
	}
	p.taker = taker
	return p
}

func opposite(s Side) Side {
	if s == Buy {
		return Sell
	}
	return Buy
}

// fills materialises the planned fills as Fill records at the maker's price.
func (p plan) fillRecords(symbol string, now time.Time) []Fill {
	out := make([]Fill, len(p.fills))
	for i, f := range p.fills {
		out[i] = Fill{
			ID: ids.New(), Symbol: symbol, Price: f.maker.Price, Qty: f.qty,
			TakerOrderID: p.taker.ID, MakerOrderID: f.maker.ID,
			TakerAccountID: p.taker.AccountID, MakerAccountID: f.maker.AccountID,
			TakerSide: p.taker.Side, At: now,
		}
	}
	return out
}

// apply makes a committed plan real: fills the makers, removes exhausted ones, rests the remainder.
func (b *book) apply(p plan) {
	for i, f := range p.fills {
		*f.maker = p.makers[i]
		b.byID[f.maker.ID] = f.maker
	}
	b.pruneFilled(opposite(p.taker.Side))
	t := p.taker
	b.byID[t.ID] = &t
	b.seq = p.nextSeq
	if t.Status.Live() {
		b.rest(b.byID[t.ID])
	}
}

func (b *book) pruneFilled(s Side) {
	sl := b.side(s)
	out := (*sl)[:0]
	for _, lv := range *sl {
		keep := lv.orders[:0]
		for _, o := range lv.orders {
			if o.Status.Live() {
				keep = append(keep, o)
			}
		}
		for i := len(keep); i < len(lv.orders); i++ {
			lv.orders[i] = nil
		}
		lv.orders = keep
		if len(lv.orders) > 0 {
			out = append(out, lv)
		}
	}
	for i := len(out); i < len(*sl); i++ {
		(*sl)[i] = nil
	}
	*sl = out
}

// rest appends o to the back of its price level, creating the level in sorted position if needed.
func (b *book) rest(o *Order) {
	sl := b.side(o.Side)
	i := sort.Search(len(*sl), func(i int) bool { return !better(o.Side, (*sl)[i].price, o.Price) })
	if i < len(*sl) && (*sl)[i].price == o.Price {
		(*sl)[i].orders = append((*sl)[i].orders, o)
		return
	}
	*sl = append(*sl, nil)
	copy((*sl)[i+1:], (*sl)[i:])
	(*sl)[i] = &level{price: o.Price, orders: []*Order{o}}
}

// applyCancel removes a live order from its level and marks it cancelled.
func (b *book) applyCancel(id string) {
	o := b.byID[id]
	if o == nil {
		return
	}
	sl := b.side(o.Side)
	for li, lv := range *sl {
		if lv.price != o.Price {
			continue
		}
		for oi, x := range lv.orders {
			if x.ID == id {
				lv.orders = append(lv.orders[:oi], lv.orders[oi+1:]...)
				break
			}
		}
		if len(lv.orders) == 0 {
			*sl = append((*sl)[:li], (*sl)[li+1:]...)
		}
		break
	}
	o.Status = StatusCancelled
}

// restore re-inserts a persisted order without matching it (startup / Resume only). Live orders
// must be restored in ascending Seq so time priority within a level is preserved.
func (b *book) restore(o Order) {
	c := o
	b.byID[c.ID] = &c
	if c.Seq > b.seq {
		b.seq = c.Seq
	}
	if c.Status.Live() {
		b.rest(b.byID[c.ID])
	}
}

func (b *book) snapshot(n int) Depth {
	d := Depth{Symbol: b.symbol}
	side := func(sl []*level) []Level {
		out := make([]Level, 0, min(n, len(sl)))
		for i := 0; i < len(sl) && i < n; i++ {
			out = append(out, Level{Price: sl[i].price, Qty: sl[i].qty(), Orders: len(sl[i].orders)})
		}
		return out
	}
	d.Bids, d.Asks = side(b.bids), side(b.asks)
	return d
}
