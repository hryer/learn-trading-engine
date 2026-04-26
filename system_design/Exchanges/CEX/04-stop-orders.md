# 04 — Stop Orders

**Status: [CHALLENGE] — required**

Stops are the trickiest part of the spec because they touch state (`last_trade_price`) that mutates during matching, and they can cascade.

## Mental model

A stop order is a **deferred order**. It's parked until the market reaches its trigger, then it transforms into something else (market or limit) and is processed normally.

```
Trader: "Buy 0.5 BTC at market — but only IF the price hits 510_000_000."

State 1: armed (in stop book, invisible)
State 2: triggered (last_trade_price reached 510_000_000)
State 3: now a market buy order — feeds into the matching engine
```

## Trigger semantics

| Order side | Fires when |
|---|---|
| Buy stop | `last_trade_price ≥ trigger_price` |
| Sell stop | `last_trade_price ≤ trigger_price` |

Why these directions:
- **Buy stop = "buy if it rallies past X"** — typical breakout entry, or short-cover stop-loss.
- **Sell stop = "sell if it drops below X"** — typical stop-loss for a long position.

Both fire when the price moves *against* the trader's current position.

## The "already triggered at placement" rule

The PDF: *"If the trigger is already satisfied at placement time (e.g., buy stop at 100 submitted while last price is 105), reject the order."*

Why: a stop whose trigger is already hit isn't really a "stop" — it's a market order in disguise. Accepting it would be confusing (the trader thought they were placing a deferred order; instead it executes immediately). Rejecting forces the trader to use a market order explicitly.

```go
func triggerAlreadyHit(o *Order, lastPrice decimal.Decimal) bool {
    if lastPrice.IsZero() {
        return false  // no trades yet — nothing to trigger against
    }
    if o.side == buy {
        return lastPrice.GreaterThanOrEqual(o.triggerPrice)
    }
    return lastPrice.LessThanOrEqual(o.triggerPrice)
}
```

### What if `last_trade_price` is zero (no trades yet)?

The PDF doesn't say. Two reasonable choices:
- **Accept** the stop (no last price → can't say it's already hit). Pick this. It's the only way to bootstrap the book.
- **Reject** all stops until a first trade exists. Pedantic, breaks the common case.

Document your choice. I recommend accept-when-zero.

## The stop book

Two sorted structures (see [02](02-data-structures.md)):

```go
type StopBook struct {
    buyStops  *btree.BTree            // sorted ASC by trigger
    sellStops *btree.BTree            // sorted DESC by trigger
    index     map[OrderID]*stopEntry  // O(1) cancel
}

type stopEntry struct {
    order *Order
    side  Side
}
```

### Why ASC for buy and DESC for sell?

Because you want to scan in the order in which orders trigger as the price moves.

If `last_trade_price` rises to 510, *all* buy stops with `trigger ≤ 510` should fire. Walking the buy tree from min upward, you hit them in trigger order and can stop the moment you see `trigger > 510`.

Symmetrically for sells.

## Trigger scan

After every trade-producing match, call:

```go
func (e *Engine) scanStops() []*Order {
    triggered := []*Order{}

    // Buy stops: fire when trigger ≤ lastPrice. Walk ascending.
    e.stopBook.buyStops.Ascend(func(item btree.Item) bool {
        entry := item.(*stopEntry)
        if entry.order.triggerPrice.GreaterThan(e.lastPrice) {
            return false   // stop iterating
        }
        triggered = append(triggered, entry.order)
        return true
    })

    // Sell stops: fire when trigger ≥ lastPrice. Walk descending.
    e.stopBook.sellStops.Descend(func(item btree.Item) bool {
        entry := item.(*stopEntry)
        if entry.order.triggerPrice.LessThan(e.lastPrice) {
            return false
        }
        triggered = append(triggered, entry.order)
        return true
    })

    // Remove triggered from the stop book *before* re-feeding them
    for _, o := range triggered {
        e.stopBook.remove(o.id)
    }

    return triggered
}
```

Critical: **remove from the stop book before re-processing.** Otherwise a triggered stop's market order could re-trigger itself in a tight loop.

## The cascade

Triggered stops become market or limit orders. Those orders execute. They produce trades. Trades update `last_trade_price`. The new `last_trade_price` may trigger *more* stops.

This is the **stop cascade**. In real markets it causes flash crashes (e.g. May 6, 2010). In the engine, you must handle it correctly without infinite-looping.

### Naive (wrong) approach

```go
// DON'T DO THIS
for _, t := range e.scanStops() {
    e.Place(commandFrom(t))   // recursive: re-enters the public API, holds the lock again? hangs
}
```

Two problems:
1. Re-entering `Place()` re-acquires the mutex (or at minimum re-runs validation that doesn't apply to internal calls).
2. The recursion depth is unbounded — a market crash could overflow the stack.

### Correct approach: trigger queue

```go
func (e *Engine) Place(cmd PlaceOrderCommand) (Order, []Trade, error) {
    e.mu.Lock()
    defer e.mu.Unlock()
    return e.placeLocked(cmd)
}

func (e *Engine) placeLocked(cmd PlaceOrderCommand) (Order, []Trade, error) {
    // ... usual logic, dispatch by type ...
    var allTrades []Trade
    
    order, trades := e.processOrder(cmd)   // may emit trades
    allTrades = append(allTrades, trades...)

    // Drain the trigger queue iteratively, not recursively.
    queue := []*Order{}
    if len(trades) > 0 {
        e.lastPrice = trades[len(trades)-1].Price
        queue = e.scanStops()
    }

    for len(queue) > 0 {
        next := queue[0]
        queue = queue[1:]

        // Convert the triggered stop into its market/limit form.
        converted := convertTriggered(next)
        _, moreTrades := e.processOrder(converted)
        allTrades = append(allTrades, moreTrades...)

        if len(moreTrades) > 0 {
            e.lastPrice = moreTrades[len(moreTrades)-1].Price
            // New triggers may exist. Append, don't recurse.
            queue = append(queue, e.scanStops()...)
        }
    }

    return order, allTrades, nil
}
```

This is **iterative, single-lock-acquire, bounded by the number of stops in the book**. No stack issues. Deterministic ordering.

### What about HTTP response shape?

The original `Place` call's caller asked for *one* order. Do you return all the cascade trades in that response?

Two valid answers:

1. **Return only the trades produced by the original taker.** Cleaner API, matches what the caller did. Cascade trades are observable separately via `/trades`.
2. **Return all trades emitted in the cascade.** Useful for clients to understand market impact.

Pick #1 for simplicity. Document in the README.

## Conversion: stop → market, stop_limit → limit

```go
func convertTriggered(stop *Order) PlaceOrderCommand {
    cmd := PlaceOrderCommand{
        UserID:   stop.userID,
        Side:     stop.side,
        Quantity: stop.remaining,   // in case of weird state
    }
    if stop.typ == stopOrder {
        cmd.Type = market
    } else {
        cmd.Type = limit
        cmd.Price = stop.price       // the stop_limit's limit price
    }
    return cmd
}
```

The triggered order keeps its identity (id, user, created_at) for audit. But internally it processes as a market/limit order. You can either:
- Reuse the original Order struct, mutating its `type` field.
- Create a new internal Order with the same id linked to the original.

Reusing is simpler. Mutating `type` is fine inside the engine since the order is no longer in the stop book.

## Cancel an armed stop

```go
// Cancel handles both cases (see file 03):
if entry, ok := e.stopBook.index[id]; ok {
    e.stopBook.remove(id)
    entry.order.status = cancelled
    return nil
}
```

The PDF requires this work. Make sure your cancel code path tries the stop book if the order isn't in the live book.

## Stop orders in the snapshot

**Excluded.** The PDF is explicit: *"Stop orders must NOT appear in the snapshot until triggered."*

This is automatic if your snapshot reads from the bid/ask trees only. The stop book is a separate structure.

Some real exchanges expose a "conditional orders" endpoint (`GET /conditional_orders`) for users to see their own stops. Out of scope.

## Edge cases worth a test each

1. **Place buy stop, never triggered, eventually cancelled.** Status: armed → cancelled.
2. **Place buy stop, triggered immediately by a subsequent trade.** One trade triggers the stop, which produces another trade.
3. **Place buy stop with trigger already hit at placement.** Rejected.
4. **Place stop_limit that triggers, but the limit price doesn't cross.** Becomes a resting limit (no fills).
5. **Cascade: stop A triggers a market order that triggers stop B.** Both produce trades; trigger order is deterministic.
6. **Cancel an armed stop.** Doesn't appear in subsequent snapshots; can't be re-triggered.
7. **Two buy stops with same trigger, different created_at.** Earlier one fires first when both triggered on the same tick.
8. **Buy stop and sell stop both triggered by the same trade.** Process in deterministic order (e.g. buys before sells, or by trigger distance — pick and document).

## Defending in the interview

> "Stops sit in a separate book — two sorted trees, one ascending for buys, one descending for sells, so a trigger scan walks only the prefix that crossed. After any trade I update last-trade-price and scan; triggered stops are removed from the stop book and queued, then drained iteratively in the same lock acquisition. That handles cascades without unbounded recursion. Already-triggered stops at placement are rejected per the spec; if there are no trades yet, I accept (the alternative blocks the bootstrap case)."

That's the whole pitch. The cascade story is the part interviewers probe — be ready to draw the iterative loop on a whiteboard.
