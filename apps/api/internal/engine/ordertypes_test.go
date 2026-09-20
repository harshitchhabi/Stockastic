package engine_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"stockastic/api/internal/engine"
	"stockastic/api/internal/money"
)

func newTypesEngine(t *testing.T) *engine.Engine {
	t.Helper()
	e, err := engine.New(engine.Config{Symbols: []string{"ACME"}, Journal: nopJournal{}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	e.Start()
	t.Cleanup(func() { _ = e.Stop(context.Background()) })
	return e
}

func sub(t *testing.T, e *engine.Engine, id, who string, typ engine.OrderType, tif engine.TimeInForce, side engine.Side, price money.Paise, qty int64) (engine.Result, error) {
	t.Helper()
	return e.Submit(context.Background(), engine.NewOrder{Type: typ, TIF: tif, ClientOrderID: id, AccountID: who, Symbol: "ACME", Side: side, Price: price, Qty: qty})
}

func TestMarketOrderSweepsAndNeverRests(t *testing.T) {
	e := newTypesEngine(t)
	sub(t, e, "1", "a", 0, 0, engine.Sell, 10000, 5)
	sub(t, e, "2", "b", 0, 0, engine.Sell, 10100, 5)
	res, err := sub(t, e, "3", "c", engine.TypeMarket, 0, engine.Buy, 20000, 12)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Fills) != 2 || res.Order.Remaining != 2 || res.Order.Status != engine.StatusCancelled {
		t.Fatalf("got %d fills, remaining %d, status %s", len(res.Fills), res.Order.Remaining, res.Order.Status)
	}
	if d, _ := e.Depth("ACME"); len(d.Bids) != 0 {
		t.Fatalf("market order rested: %+v", d.Bids)
	}
}

func TestMarketOrderProtectionPriceCapsTheSweep(t *testing.T) {
	e := newTypesEngine(t)
	sub(t, e, "1", "a", 0, 0, engine.Sell, 10000, 5)
	sub(t, e, "2", "b", 0, 0, engine.Sell, 10100, 5)
	res, _ := sub(t, e, "3", "c", engine.TypeMarket, 0, engine.Buy, 10050, 10)
	if len(res.Fills) != 1 || res.Fills[0].Price != 10000 {
		t.Fatalf("protection price breached: %+v", res.Fills)
	}
}

func TestMarketOrderOnEmptyBookIsCancelledNotRested(t *testing.T) {
	e := newTypesEngine(t)
	res, err := sub(t, e, "1", "a", engine.TypeMarket, 0, engine.Buy, 20000, 5)
	if err != nil || len(res.Fills) != 0 || res.Order.Status != engine.StatusCancelled {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestFillOrKillIsAllOrNothing(t *testing.T) {
	e := newTypesEngine(t)
	sub(t, e, "1", "a", 0, 0, engine.Sell, 10000, 5)
	res, _ := sub(t, e, "2", "b", engine.TypeLimit, engine.FOK, engine.Buy, 10000, 8)
	if len(res.Fills) != 0 || res.Order.Status != engine.StatusCancelled {
		t.Fatalf("FOK partially filled: %+v", res)
	}
	if d, _ := e.Depth("ACME"); d.Asks[0].Qty != 5 {
		t.Fatalf("book was touched by a killed FOK: %+v", d.Asks)
	}
	res, _ = sub(t, e, "3", "b", engine.TypeLimit, engine.FOK, engine.Buy, 10000, 5)
	if len(res.Fills) != 1 || res.Order.Status != engine.StatusFilled {
		t.Fatalf("FOK should fill: %+v", res)
	}
}

func TestIOCLimitCancelsRemainder(t *testing.T) {
	e := newTypesEngine(t)
	sub(t, e, "1", "a", 0, 0, engine.Sell, 10000, 3)
	res, _ := sub(t, e, "2", "b", engine.TypeLimit, engine.IOC, engine.Buy, 10000, 5)
	if len(res.Fills) != 1 || res.Order.Remaining != 2 || res.Order.Status != engine.StatusCancelled {
		t.Fatalf("%+v", res)
	}
	if d, _ := e.Depth("ACME"); len(d.Bids) != 0 {
		t.Fatal("IOC remainder rested")
	}
}

func TestOrderTypeValidationAndFingerprint(t *testing.T) {
	e := newTypesEngine(t)
	if _, err := sub(t, e, "1", "a", engine.TypeMarket, engine.GTC, engine.Buy, 100, 1); !errors.Is(err, engine.ErrInvalidOrder) {
		t.Fatalf("resting market order accepted: %v", err)
	}
	sub(t, e, "2", "a", engine.TypeLimit, engine.GTC, engine.Buy, 100, 1)
	if _, err := sub(t, e, "2", "a", engine.TypeLimit, engine.IOC, engine.Buy, 100, 1); !errors.Is(err, engine.ErrIdempotencyMismatch) {
		t.Fatalf("same key with different TIF must be a mismatch: %v", err)
	}
	if r, err := sub(t, e, "2", "a", 0, 0, engine.Buy, 100, 1); err != nil || !r.Deduped {
		t.Fatalf("defaults must equal explicit limit/gtc: %+v %v", r, err)
	}
}
