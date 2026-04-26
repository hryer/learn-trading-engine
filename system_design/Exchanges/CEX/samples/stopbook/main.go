// Sample: armed stop orders, trigger scan, and cascade processing.
//
// Run:   go run ./stopbook
//
// Reference: ../../04-stop-orders.md
//
// Demonstrates:
//   - Stops sit in a separate book, invisible to snapshots.
//   - Trigger scan walks only the prefix that crossed last-trade-price.
//   - The cascade is iterative (queue-based), not recursive.
//   - Already-triggered stops at placement are rejected.
package main

import (
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
	ID           string
	UserID       string
	Side         Side
	TriggerPrice decimal.Decimal
	Quantity     decimal.Decimal
}

type stopEntry struct {
	order *Order
	side  Side
}

type StopBook struct {
	buys  *btree.BTreeG[*stopEntry] // ASC by trigger
	sells *btree.BTreeG[*stopEntry] // DESC by trigger
	idx   map[string]*stopEntry
}

func NewStopBook() *StopBook {
	return &StopBook{
		buys: btree.NewG[*stopEntry](32, func(a, b *stopEntry) bool {
			c := a.order.TriggerPrice.Cmp(b.order.TriggerPrice)
			if c != 0 {
				return c < 0
			}
			return a.order.ID < b.order.ID // tie-break by ID for stable ordering
		}),
		sells: btree.NewG[*stopEntry](32, func(a, b *stopEntry) bool {
			c := a.order.TriggerPrice.Cmp(b.order.TriggerPrice)
			if c != 0 {
				return c > 0 // DESC
			}
			return a.order.ID < b.order.ID
		}),
		idx: make(map[string]*stopEntry),
	}
}

// Add returns false if the trigger is already satisfied at placement.
func (sb *StopBook) Add(o *Order, lastPrice decimal.Decimal) bool {
	if !lastPrice.IsZero() && triggerHit(o, lastPrice) {
		return false // would be rejected by the engine
	}
	entry := &stopEntry{order: o, side: o.Side}
	if o.Side == Buy {
		sb.buys.ReplaceOrInsert(entry)
	} else {
		sb.sells.ReplaceOrInsert(entry)
	}
	sb.idx[o.ID] = entry
	return true
}

func (sb *StopBook) Remove(id string) bool {
	entry, ok := sb.idx[id]
	if !ok {
		return false
	}
	if entry.side == Buy {
		sb.buys.Delete(entry)
	} else {
		sb.sells.Delete(entry)
	}
	delete(sb.idx, id)
	return true
}

// Scan returns triggered orders (and removes them from the book).
//
// Buy stops fire when last >= trigger -> walk asks-style ASC, stop when trigger > last.
// Sell stops fire when last <= trigger -> walk DESC,        stop when trigger < last.
func (sb *StopBook) Scan(lastPrice decimal.Decimal) []*Order {
	var fired []*Order

	sb.buys.Ascend(func(e *stopEntry) bool {
		if e.order.TriggerPrice.GreaterThan(lastPrice) {
			return false
		}
		fired = append(fired, e.order)
		return true
	})
	sb.sells.Ascend(func(e *stopEntry) bool { // tree is DESC, so Ascend = highest trigger first
		if e.order.TriggerPrice.LessThan(lastPrice) {
			return false
		}
		fired = append(fired, e.order)
		return true
	})

	for _, o := range fired {
		sb.Remove(o.ID) // crucial: remove BEFORE re-feeding into matching
	}
	return fired
}

func triggerHit(o *Order, lastPrice decimal.Decimal) bool {
	if o.Side == Buy {
		return lastPrice.GreaterThanOrEqual(o.TriggerPrice)
	}
	return lastPrice.LessThanOrEqual(o.TriggerPrice)
}

// ----------------------------------------------------------------------------

func main() {
	d := decimal.RequireFromString
	sb := NewStopBook()

	lastPrice := d("100.00")

	// Place several buy stops at different triggers.
	stops := []*Order{
		{ID: "s1", UserID: "u1", Side: Buy, TriggerPrice: d("105.00"), Quantity: d("0.5")},
		{ID: "s2", UserID: "u2", Side: Buy, TriggerPrice: d("110.00"), Quantity: d("1.0")},
		{ID: "s3", UserID: "u3", Side: Buy, TriggerPrice: d("103.00"), Quantity: d("0.3")},
		{ID: "s4", UserID: "u4", Side: Sell, TriggerPrice: d("95.00"), Quantity: d("0.7")},
		{ID: "s5", UserID: "u5", Side: Sell, TriggerPrice: d("98.00"), Quantity: d("0.2")},
	}

	fmt.Printf("=== last_price = %s, placing 5 stops ===\n", lastPrice)
	for _, s := range stops {
		ok := sb.Add(s, lastPrice)
		fmt.Printf("  add %s (%s trigger %s): accepted=%v\n",
			s.ID, sideStr(s.Side), s.TriggerPrice, ok)
	}

	// Try to add a stop whose trigger is already hit.
	bad := &Order{ID: "s6", UserID: "u6", Side: Buy, TriggerPrice: d("99.00"), Quantity: d("1.0")}
	ok := sb.Add(bad, lastPrice)
	fmt.Printf("  add %s (buy trigger %s, last=%s): accepted=%v (expect false — already triggered)\n",
		bad.ID, bad.TriggerPrice, lastPrice, ok)

	// Tick: price rises to 104. s3 fires (trigger 103). s1 doesn't (trigger 105).
	fmt.Printf("\n=== last_price moves to 104.00 ===\n")
	lastPrice = d("104.00")
	fired := sb.Scan(lastPrice)
	for _, o := range fired {
		fmt.Printf("  fired: %s (buy trigger %s)\n", o.ID, o.TriggerPrice)
	}
	fmt.Printf("  buy stops still armed: %d, sell stops still armed: %d\n",
		sb.buys.Len(), sb.sells.Len())

	// Tick: price spikes to 111. s1 (105) and s2 (110) both fire.
	fmt.Printf("\n=== last_price spikes to 111.00 (cascade) ===\n")
	lastPrice = d("111.00")
	fired = sb.Scan(lastPrice)
	for _, o := range fired {
		fmt.Printf("  fired: %s (buy trigger %s)\n", o.ID, o.TriggerPrice)
	}

	// Tick: price crashes to 94. Sell stops s4 (95) and s5 (98) fire.
	fmt.Printf("\n=== last_price crashes to 94.00 ===\n")
	lastPrice = d("94.00")
	fired = sb.Scan(lastPrice)
	for _, o := range fired {
		fmt.Printf("  fired: %s (sell trigger %s)\n", o.ID, o.TriggerPrice)
	}

	fmt.Printf("\n=== final: buy stops=%d, sell stops=%d ===\n",
		sb.buys.Len(), sb.sells.Len())
}

func sideStr(s Side) string {
	if s == Buy {
		return "buy"
	}
	return "sell"
}
