// Sample: the matching loop with limit + market orders, partial fills,
// and trade-at-maker-price semantics.
//
// Run:   go run ./matching
//
// Reference: ../../03-matching-engine.md
package main

import (
	"container/list"
	"fmt"
	"time"

	"github.com/google/btree"
	"github.com/shopspring/decimal"
)

type Side int

const (
	Buy Side = iota
	Sell
)

type Type int

const (
	Limit Type = iota
	Market
)

type Status int

const (
	Resting Status = iota
	PartiallyFilled
	Filled
	Rejected
	Cancelled
)

func (s Status) String() string {
	return []string{"resting", "partially_filled", "filled", "rejected", "cancelled"}[s]
}

type Order struct {
	ID        string
	UserID    string
	Side      Side
	Type      Type
	Price     decimal.Decimal
	Quantity  decimal.Decimal
	Remaining decimal.Decimal
	Status    Status
	CreatedAt time.Time
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

type priceLevel struct {
	price decimal.Decimal
	queue *list.List // *Order
}

type Engine struct {
	bids  *btree.BTreeG[*priceLevel] // DESC
	asks  *btree.BTreeG[*priceLevel] // ASC
	idx   map[string]*orderHandle
	now   func() time.Time
	nextO uint64
	nextT uint64
}

type orderHandle struct {
	order *Order
	node  *list.Element
	level *priceLevel
	side  Side
}

func NewEngine() *Engine {
	asks := btree.NewG[*priceLevel](32, func(a, b *priceLevel) bool { return a.price.LessThan(b.price) })
	bids := btree.NewG[*priceLevel](32, func(a, b *priceLevel) bool { return a.price.GreaterThan(b.price) })
	t0 := time.Unix(0, 0).UTC()
	tick := int64(0)
	return &Engine{
		asks: asks, bids: bids,
		idx: make(map[string]*orderHandle),
		now: func() time.Time {
			tick++
			return t0.Add(time.Duration(tick))
		},
	}
}

func (e *Engine) Place(o *Order) (*Order, []Trade) {
	e.nextO++
	o.ID = fmt.Sprintf("o-%d", e.nextO)
	o.CreatedAt = e.now()
	o.Remaining = o.Quantity

	trades := e.match(o)

	if o.Remaining.IsPositive() {
		switch o.Type {
		case Limit:
			e.rest(o)
			if len(trades) == 0 {
				o.Status = Resting
			} else {
				o.Status = PartiallyFilled
			}
		case Market:
			o.Status = Rejected // remainder rejected; trades already produced are kept
		}
	} else {
		o.Status = Filled
	}
	return o, trades
}

func (e *Engine) match(taker *Order) []Trade {
	var trades []Trade
	tree := e.opposite(taker.Side)

	for taker.Remaining.IsPositive() {
		bestLevel, ok := tree.Min()
		if !ok || !crosses(taker, bestLevel.price) {
			break
		}

		for taker.Remaining.IsPositive() && bestLevel.queue.Len() > 0 {
			front := bestLevel.queue.Front()
			maker := front.Value.(*Order)

			fill := decimal.Min(taker.Remaining, maker.Remaining)
			price := maker.Price // maker price wins, always

			e.nextT++
			trades = append(trades, Trade{
				ID: fmt.Sprintf("t-%d", e.nextT), TakerID: taker.ID, MakerID: maker.ID,
				Price: price, Quantity: fill, TakerSide: taker.Side, CreatedAt: e.now(),
			})

			taker.Remaining = taker.Remaining.Sub(fill)
			maker.Remaining = maker.Remaining.Sub(fill)

			if maker.Remaining.IsZero() {
				bestLevel.queue.Remove(front)
				maker.Status = Filled
				delete(e.idx, maker.ID)
			} else {
				maker.Status = PartiallyFilled
			}
		}

		if bestLevel.queue.Len() == 0 {
			tree.Delete(bestLevel)
		}
	}
	return trades
}

func crosses(taker *Order, bookPrice decimal.Decimal) bool {
	if taker.Type == Market {
		return true
	}
	if taker.Side == Buy {
		return taker.Price.GreaterThanOrEqual(bookPrice)
	}
	return taker.Price.LessThanOrEqual(bookPrice)
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

// ----------------------------------------------------------------------------

func main() {
	d := decimal.RequireFromString
	e := NewEngine()

	// Build a thin asks book.
	fmt.Println("=== seed asks ===")
	seed(e, &Order{UserID: "u1", Side: Sell, Type: Limit, Price: d("100.50"), Quantity: d("0.3")})
	seed(e, &Order{UserID: "u2", Side: Sell, Type: Limit, Price: d("100.50"), Quantity: d("0.5")})
	seed(e, &Order{UserID: "u3", Side: Sell, Type: Limit, Price: d("100.80"), Quantity: d("1.0")})

	// Aggressive limit buy that walks two levels and rests the remainder.
	fmt.Println("\n=== buy limit 1.0 @ 100.80 ===")
	taker := &Order{UserID: "u9", Side: Buy, Type: Limit, Price: d("100.80"), Quantity: d("1.0")}
	o, trades := e.Place(taker)
	report(o, trades)
	// Expect:
	//   trade 1: 0.3 @ 100.50 (vs o-1)
	//   trade 2: 0.5 @ 100.50 (vs o-2)
	//   trade 3: 0.2 @ 100.80 (vs o-3)
	//   o-4 status: filled, remaining 0

	// Market buy on a thin book — should fill what it can and reject the rest.
	fmt.Println("\n=== market buy 5.0 (book has only 0.8 left) ===")
	taker = &Order{UserID: "u10", Side: Buy, Type: Market, Quantity: d("5.0")}
	o, trades = e.Place(taker)
	report(o, trades)
	// Expect: 1 trade for 0.8 @ 100.80; status = rejected, remaining = 4.2

	// Market on empty side.
	fmt.Println("\n=== market buy 1.0 on empty asks ===")
	taker = &Order{UserID: "u11", Side: Buy, Type: Market, Quantity: d("1.0")}
	o, trades = e.Place(taker)
	report(o, trades)
	// Expect: 0 trades; status = rejected, remaining = 1.0
}

func seed(e *Engine, o *Order) {
	o2, _ := e.Place(o)
	fmt.Printf("  placed %s: %s %s @ %s (status=%s)\n",
		o2.ID, sideStr(o2.Side), o2.Quantity, o2.Price, o2.Status)
}

func report(o *Order, trades []Trade) {
	fmt.Printf("  taker %s status=%s remaining=%s\n", o.ID, o.Status, o.Remaining)
	for _, t := range trades {
		fmt.Printf("  trade %s: maker=%s qty=%s price=%s\n", t.ID, t.MakerID, t.Quantity, t.Price)
	}
}

func sideStr(s Side) string {
	if s == Buy {
		return "buy"
	}
	return "sell"
}
