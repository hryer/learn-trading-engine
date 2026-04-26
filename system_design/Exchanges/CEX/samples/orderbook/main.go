// Sample: the two-layer order book — btree of price levels + FIFO list per level.
//
// Run:   go run ./orderbook
//
// Reference: ../../02-data-structures.md
//
// What this shows:
//   - btree gives ordered access (best bid/ask, in-order snapshot iteration)
//   - container/list gives O(1) head pop and O(1) cancel-by-node
//   - the order index gives O(log L) cancel-by-id
//   - empty levels are removed so snapshots stay clean
package main

import (
	"container/list"
	"fmt"

	"github.com/google/btree"
	"github.com/shopspring/decimal"
)

type Side int

const (
	Buy Side = iota
	Sell
)

type Order struct {
	ID       string
	UserID   string
	Side     Side
	Price    decimal.Decimal
	Quantity decimal.Decimal
}

// priceLevel: one price + a FIFO of orders.
type priceLevel struct {
	price decimal.Decimal
	queue *list.List // *Order
}

// btree.Item — strict less-than. We use this for the *asks* book (ascending).
// For the *bids* book we wrap it (see lessBids below) to invert the order.
func (l *priceLevel) Less(than btree.Item) bool {
	return l.price.LessThan(than.(*priceLevel).price)
}

// orderHandle: pointer-back into the level + FIFO node, for O(1) cancel.
type orderHandle struct {
	order *Order
	node  *list.Element
	level *priceLevel
	side  Side
}

type Book struct {
	bids       *btree.BTreeG[*priceLevel] // sorted DESC (we'll reverse iterate)
	asks       *btree.BTreeG[*priceLevel] // sorted ASC
	orderIndex map[string]*orderHandle
}

func NewBook() *Book {
	asks := btree.NewG[*priceLevel](32, func(a, b *priceLevel) bool {
		return a.price.LessThan(b.price)
	})
	bids := btree.NewG[*priceLevel](32, func(a, b *priceLevel) bool {
		// Sort DESC so Min() returns the BEST bid (highest price).
		return a.price.GreaterThan(b.price)
	})
	return &Book{
		bids:       bids,
		asks:       asks,
		orderIndex: make(map[string]*orderHandle),
	}
}

// Place adds the order at its price level (no matching here — just resting).
func (b *Book) Place(o *Order) {
	tree := b.tree(o.Side)
	probe := &priceLevel{price: o.Price}
	level, ok := tree.Get(probe)
	if !ok {
		level = &priceLevel{price: o.Price, queue: list.New()}
		tree.ReplaceOrInsert(level)
	}
	node := level.queue.PushBack(o)
	b.orderIndex[o.ID] = &orderHandle{
		order: o, node: node, level: level, side: o.Side,
	}
}

// Cancel removes the order in O(log L). Drops the level if it became empty.
func (b *Book) Cancel(id string) bool {
	h, ok := b.orderIndex[id]
	if !ok {
		return false
	}
	h.level.queue.Remove(h.node)
	delete(b.orderIndex, id)
	if h.level.queue.Len() == 0 {
		b.tree(h.side).Delete(h.level)
	}
	return true
}

// BestBid / BestAsk: O(log n) via tree min.
func (b *Book) BestBid() (decimal.Decimal, bool) {
	level, ok := b.bids.Min() // bids tree is DESC — Min is the highest price
	if !ok {
		return decimal.Decimal{}, false
	}
	return level.price, true
}

func (b *Book) BestAsk() (decimal.Decimal, bool) {
	level, ok := b.asks.Min()
	if !ok {
		return decimal.Decimal{}, false
	}
	return level.price, true
}

// Snapshot top N levels per side, aggregated by summing the FIFO at read time.
type Level struct {
	Price    decimal.Decimal
	Quantity decimal.Decimal
}

type Snapshot struct {
	Bids []Level
	Asks []Level
}

func (b *Book) Snapshot(depth int) Snapshot {
	snap := Snapshot{}
	collect := func(tree *btree.BTreeG[*priceLevel], dst *[]Level) {
		count := 0
		tree.Ascend(func(level *priceLevel) bool {
			if count >= depth {
				return false
			}
			total := decimal.Zero
			for e := level.queue.Front(); e != nil; e = e.Next() {
				total = total.Add(e.Value.(*Order).Quantity)
			}
			*dst = append(*dst, Level{Price: level.price, Quantity: total})
			count++
			return true
		})
	}
	collect(b.bids, &snap.Bids) // bids tree DESC -> Ascend = best first
	collect(b.asks, &snap.Asks)
	return snap
}

func (b *Book) tree(s Side) *btree.BTreeG[*priceLevel] {
	if s == Buy {
		return b.bids
	}
	return b.asks
}

// ----------------------------------------------------------------------------

func main() {
	book := NewBook()
	d := decimal.RequireFromString

	// Place a few orders.
	book.Place(&Order{ID: "o1", UserID: "u1", Side: Sell, Price: d("100.50"), Quantity: d("1.0")})
	book.Place(&Order{ID: "o2", UserID: "u2", Side: Sell, Price: d("100.50"), Quantity: d("0.5")}) // same level
	book.Place(&Order{ID: "o3", UserID: "u3", Side: Sell, Price: d("101.00"), Quantity: d("2.0")})
	book.Place(&Order{ID: "o4", UserID: "u4", Side: Buy, Price: d("100.00"), Quantity: d("1.5")})
	book.Place(&Order{ID: "o5", UserID: "u5", Side: Buy, Price: d("99.50"), Quantity: d("3.0")})

	bestBid, _ := book.BestBid()
	bestAsk, _ := book.BestAsk()
	fmt.Printf("Best bid: %s\nBest ask: %s\nSpread:   %s\n\n",
		bestBid, bestAsk, bestAsk.Sub(bestBid))

	printSnapshot("Initial book", book.Snapshot(10))

	// Cancel o1: o2 still rests at the same level (qty drops from 1.5 to 0.5).
	book.Cancel("o1")
	printSnapshot("After cancel o1", book.Snapshot(10))

	// Cancel o2: the 100.50 level should disappear.
	book.Cancel("o2")
	printSnapshot("After cancel o2 (level removed)", book.Snapshot(10))

	// Cancel non-existent.
	fmt.Printf("cancel unknown? %v (expect false)\n", book.Cancel("o-unknown"))
}

func printSnapshot(label string, s Snapshot) {
	fmt.Println(label + ":")
	fmt.Println("  ASKS (ascending price):")
	for _, l := range s.Asks {
		fmt.Printf("    %s × %s\n", l.Price, l.Quantity)
	}
	fmt.Println("  BIDS (descending price):")
	for _, l := range s.Bids {
		fmt.Printf("    %s × %s\n", l.Price, l.Quantity)
	}
	fmt.Println()
}
