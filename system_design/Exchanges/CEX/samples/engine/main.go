// Sample: minimal end-to-end engine — book + matching + stops + SMP + concurrency.
//
// Run:   go run ./engine
//
// References:
//   - matching loop:        ../../03-matching-engine.md
//   - stop cascades:        ../../04-stop-orders.md
//   - concurrency model:    ../../06-concurrency-determinism.md
//   - self-match policy:    ../../07-self-match-prevention.md
//
// What this shows (in one file, ~350 lines):
//   - sync.Mutex single-writer model
//   - Limit + market + stop + stop_limit order types
//   - Cancel by id (resting OR armed)
//   - Stop cascade: triggered stop produces a trade that triggers another stop
//   - Self-match prevention: cancel-newest
//   - A determinism check at the bottom (replays the same sequence twice and compares trades)
package main

import (
	"container/list"
	"fmt"
	"sync"
	"time"

	"github.com/google/btree"
	"github.com/shopspring/decimal"
)

// ----- domain types ---------------------------------------------------------

type Side int

const (
	Buy Side = iota
	Sell
)

type OrderType int

const (
	Limit OrderType = iota
	Market
	Stop
	StopLimit
)

type Status int

const (
	Resting Status = iota
	PartiallyFilled
	Filled
	Rejected
	Cancelled
	Armed
)

func (s Status) String() string {
	return []string{"resting", "partially_filled", "filled", "rejected", "cancelled", "armed"}[s]
}

type Order struct {
	ID           string
	UserID       string
	Side         Side
	Type         OrderType
	Price        decimal.Decimal
	TriggerPrice decimal.Decimal
	Quantity     decimal.Decimal
	Remaining    decimal.Decimal
	Status       Status
	CreatedAt    time.Time
}

type Trade struct {
	ID        string
	TakerID   string
	MakerID   string
	Price     decimal.Decimal
	Quantity  decimal.Decimal
	TakerSide Side
	CreatedAt time.Time
}

type PlaceCmd struct {
	UserID       string
	Side         Side
	Type         OrderType
	Price        decimal.Decimal
	TriggerPrice decimal.Decimal
	Quantity     decimal.Decimal
}

// ----- internal data structures --------------------------------------------

type priceLevel struct {
	price decimal.Decimal
	queue *list.List
}

type orderHandle struct {
	order *Order
	node  *list.Element
	level *priceLevel
	side  Side
}

type stopEntry struct {
	order *Order
	side  Side
}

// ----- engine ---------------------------------------------------------------

type Engine struct {
	mu sync.Mutex

	bids      *btree.BTreeG[*priceLevel]
	asks      *btree.BTreeG[*priceLevel]
	buyStops  *btree.BTreeG[*stopEntry]
	sellStops *btree.BTreeG[*stopEntry]
	idx       map[string]*orderHandle
	stopIdx   map[string]*stopEntry

	lastPrice decimal.Decimal
	now       func() time.Time
	nextO     uint64
	nextT     uint64
}

func NewEngine(clock func() time.Time) *Engine {
	return &Engine{
		bids: btree.NewG[*priceLevel](32, func(a, b *priceLevel) bool { return a.price.GreaterThan(b.price) }),
		asks: btree.NewG[*priceLevel](32, func(a, b *priceLevel) bool { return a.price.LessThan(b.price) }),
		buyStops: btree.NewG[*stopEntry](32, func(a, b *stopEntry) bool {
			c := a.order.TriggerPrice.Cmp(b.order.TriggerPrice)
			if c != 0 {
				return c < 0
			}
			return a.order.ID < b.order.ID
		}),
		sellStops: btree.NewG[*stopEntry](32, func(a, b *stopEntry) bool {
			c := a.order.TriggerPrice.Cmp(b.order.TriggerPrice)
			if c != 0 {
				return c > 0
			}
			return a.order.ID < b.order.ID
		}),
		idx:     make(map[string]*orderHandle),
		stopIdx: make(map[string]*stopEntry),
		now:     clock,
	}
}

func (e *Engine) Place(cmd PlaceCmd) (*Order, []Trade, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.placeLocked(cmd)
}

func (e *Engine) Cancel(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if h, ok := e.idx[id]; ok {
		h.level.queue.Remove(h.node)
		h.order.Status = Cancelled
		delete(e.idx, id)
		if h.level.queue.Len() == 0 {
			e.tree(h.side).Delete(h.level)
		}
		return nil
	}
	if entry, ok := e.stopIdx[id]; ok {
		if entry.side == Buy {
			e.buyStops.Delete(entry)
		} else {
			e.sellStops.Delete(entry)
		}
		entry.order.Status = Cancelled
		delete(e.stopIdx, id)
		return nil
	}
	return fmt.Errorf("order %s not found", id)
}

func (e *Engine) placeLocked(cmd PlaceCmd) (*Order, []Trade, error) {
	o := e.newOrder(cmd)

	// Stop / stop-limit: arm or reject if already triggered.
	if cmd.Type == Stop || cmd.Type == StopLimit {
		if !e.lastPrice.IsZero() && triggerHit(o, e.lastPrice) {
			o.Status = Rejected
			return o, nil, nil
		}
		entry := &stopEntry{order: o, side: o.Side}
		if o.Side == Buy {
			e.buyStops.ReplaceOrInsert(entry)
		} else {
			e.sellStops.ReplaceOrInsert(entry)
		}
		e.stopIdx[o.ID] = entry
		o.Status = Armed
		return o, nil, nil
	}

	// Limit / market: match, then either rest (limit) or reject (market).
	trades := e.match(o)
	e.applyResting(o, trades)

	// Cascade: drain triggered stops iteratively (no recursion).
	allTrades := append([]Trade{}, trades...)
	queue := []*Order{}
	if len(trades) > 0 {
		queue = e.scanStops()
	}
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		converted := convertTriggered(next)
		more := e.match(converted)
		e.applyResting(converted, more)
		allTrades = append(allTrades, more...)
		if len(more) > 0 {
			queue = append(queue, e.scanStops()...)
		}
	}
	return o, allTrades, nil
}

func (e *Engine) match(taker *Order) []Trade {
	var trades []Trade
	tree := e.opposite(taker.Side)

	for taker.Remaining.IsPositive() {
		level, ok := tree.Min()
		if !ok || !crosses(taker, level.price) {
			break
		}
		for taker.Remaining.IsPositive() && level.queue.Len() > 0 {
			front := level.queue.Front()
			maker := front.Value.(*Order)

			// Self-match prevention: cancel-newest.
			if maker.UserID == taker.UserID {
				taker.Status = Cancelled
				return trades
			}

			fill := decimal.Min(taker.Remaining, maker.Remaining)
			price := maker.Price

			e.nextT++
			trades = append(trades, Trade{
				ID: fmt.Sprintf("t-%d", e.nextT), TakerID: taker.ID, MakerID: maker.ID,
				Price: price, Quantity: fill, TakerSide: taker.Side, CreatedAt: e.now(),
			})

			taker.Remaining = taker.Remaining.Sub(fill)
			maker.Remaining = maker.Remaining.Sub(fill)

			if maker.Remaining.IsZero() {
				level.queue.Remove(front)
				maker.Status = Filled
				delete(e.idx, maker.ID)
			} else {
				maker.Status = PartiallyFilled
			}

			e.lastPrice = price
		}
		if level.queue.Len() == 0 {
			tree.Delete(level)
		}
	}
	return trades
}

func (e *Engine) applyResting(o *Order, trades []Trade) {
	if o.Status == Cancelled {
		return // SMP already set this
	}
	if o.Remaining.IsPositive() {
		switch o.Type {
		case Limit, StopLimit:
			e.rest(o)
			if len(trades) == 0 {
				o.Status = Resting
			} else {
				o.Status = PartiallyFilled
			}
		case Market, Stop:
			o.Status = Rejected
		}
	} else {
		o.Status = Filled
	}
}

func (e *Engine) rest(o *Order) {
	tree := e.tree(o.Side)
	probe := &priceLevel{price: o.Price}
	level, ok := tree.Get(probe)
	if !ok {
		level = &priceLevel{price: o.Price, queue: list.New()}
		tree.ReplaceOrInsert(level)
	}
	node := level.queue.PushBack(o)
	e.idx[o.ID] = &orderHandle{order: o, node: node, level: level, side: o.Side}
}

func (e *Engine) scanStops() []*Order {
	var fired []*Order
	e.buyStops.Ascend(func(s *stopEntry) bool {
		if s.order.TriggerPrice.GreaterThan(e.lastPrice) {
			return false
		}
		fired = append(fired, s.order)
		return true
	})
	e.sellStops.Ascend(func(s *stopEntry) bool {
		if s.order.TriggerPrice.LessThan(e.lastPrice) {
			return false
		}
		fired = append(fired, s.order)
		return true
	})
	for _, o := range fired {
		entry := e.stopIdx[o.ID]
		if o.Side == Buy {
			e.buyStops.Delete(entry)
		} else {
			e.sellStops.Delete(entry)
		}
		delete(e.stopIdx, o.ID)
	}
	return fired
}

func (e *Engine) newOrder(cmd PlaceCmd) *Order {
	e.nextO++
	return &Order{
		ID: fmt.Sprintf("o-%d", e.nextO), UserID: cmd.UserID,
		Side: cmd.Side, Type: cmd.Type,
		Price: cmd.Price, TriggerPrice: cmd.TriggerPrice,
		Quantity: cmd.Quantity, Remaining: cmd.Quantity,
		CreatedAt: e.now(),
	}
}

func (e *Engine) tree(s Side) *btree.BTreeG[*priceLevel] {
	if s == Buy {
		return e.bids
	}
	return e.asks
}
func (e *Engine) opposite(s Side) *btree.BTreeG[*priceLevel] {
	if s == Buy {
		return e.asks
	}
	return e.bids
}

func crosses(taker *Order, p decimal.Decimal) bool {
	if taker.Type == Market || taker.Type == Stop {
		return true
	}
	if taker.Side == Buy {
		return taker.Price.GreaterThanOrEqual(p)
	}
	return taker.Price.LessThanOrEqual(p)
}

func triggerHit(o *Order, last decimal.Decimal) bool {
	if o.Side == Buy {
		return last.GreaterThanOrEqual(o.TriggerPrice)
	}
	return last.LessThanOrEqual(o.TriggerPrice)
}

func convertTriggered(stop *Order) *Order {
	if stop.Type == Stop {
		stop.Type = Market
	} else {
		stop.Type = Limit
	}
	return stop
}

// ----- demo -----------------------------------------------------------------

func main() {
	demo("Run #1", buildScenario())

	// Determinism: same input -> same trades.
	t1 := tradesOnly(buildScenario())
	t2 := tradesOnly(buildScenario())
	fmt.Println("\n=== determinism check ===")
	fmt.Printf("Run #1 trade count: %d\n", len(t1))
	fmt.Printf("Run #2 trade count: %d\n", len(t2))
	identical := len(t1) == len(t2)
	for i := 0; i < len(t1) && identical; i++ {
		identical = t1[i].MakerID == t2[i].MakerID &&
			t1[i].Price.Equal(t2[i].Price) &&
			t1[i].Quantity.Equal(t2[i].Quantity)
	}
	fmt.Printf("identical? %v (expect true)\n", identical)
}

func demo(label string, cmds []PlaceCmd) {
	fmt.Printf("=== %s ===\n", label)
	e := newDeterministicEngine()
	for _, c := range cmds {
		o, ts, err := e.Place(c)
		if err != nil {
			fmt.Printf("  ERROR: %v\n", err)
			continue
		}
		fmt.Printf("  place %s (%s %s, type=%d) -> status=%s, trades=%d\n",
			o.ID, sideStr(o.Side), o.Quantity, o.Type, o.Status, len(ts))
		for _, t := range ts {
			fmt.Printf("    trade %s: maker=%s qty=%s price=%s\n",
				t.ID, t.MakerID, t.Quantity, t.Price)
		}
	}
}

func tradesOnly(cmds []PlaceCmd) []Trade {
	e := newDeterministicEngine()
	var all []Trade
	for _, c := range cmds {
		_, ts, _ := e.Place(c)
		all = append(all, ts...)
	}
	return all
}

func newDeterministicEngine() *Engine {
	t0 := time.Unix(0, 0).UTC()
	tick := int64(0)
	clock := func() time.Time {
		tick++
		return t0.Add(time.Duration(tick))
	}
	return NewEngine(clock)
}

func buildScenario() []PlaceCmd {
	d := decimal.RequireFromString
	return []PlaceCmd{
		// Seed asks
		{UserID: "u1", Side: Sell, Type: Limit, Price: d("100.50"), Quantity: d("0.3")},
		{UserID: "u2", Side: Sell, Type: Limit, Price: d("100.50"), Quantity: d("0.5")},
		{UserID: "u3", Side: Sell, Type: Limit, Price: d("100.80"), Quantity: d("1.0")},
		// Buy stop that arms (last price still 0 -> accepted; will fire on first trade above 100.70).
		{UserID: "u4", Side: Buy, Type: Stop, TriggerPrice: d("100.70"), Quantity: d("0.4")},
		// Limit buy 0.3 @ 100.50: matches o-1 fully. last_price = 100.50.
		{UserID: "u5", Side: Buy, Type: Limit, Price: d("100.50"), Quantity: d("0.3")},
		// Limit buy 0.6 @ 100.80: matches o-2 (0.5 @ 100.50), then 0.1 of o-3 @ 100.80.
		// last_price ticks to 100.80 -> u4's stop (trigger 100.70) fires as a market buy 0.4,
		// which then matches the remaining 0.4 of o-3 @ 100.80. CASCADE.
		{UserID: "u6", Side: Buy, Type: Limit, Price: d("100.80"), Quantity: d("0.6")},
		// Self-match attempt: u3 still has resting sell @ 100.80 (0.5 left).
		// u3 buying at 100.80 would match their own order -> SMP cancels the taker.
		{UserID: "u3", Side: Buy, Type: Limit, Price: d("100.80"), Quantity: d("0.1")},
	}
}

func sideStr(s Side) string {
	if s == Buy {
		return "buy"
	}
	return "sell"
}
