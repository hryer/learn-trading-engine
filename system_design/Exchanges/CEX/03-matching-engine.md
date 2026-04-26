# 03 — Matching Engine

**Status: [CHALLENGE] — required**

The core algorithm. Get this right and the rest is plumbing.

## The contract

A single function:

```go
func (e *Engine) Place(cmd PlaceOrderCommand) (Order, []Trade, error)
```

In: a command (validated, no defaults missing).
Out: the resulting order (with final status) + the trades produced.

The engine owns:
- Generating order ID + timestamp
- Generating trade IDs
- Updating the book
- Updating the stop book
- Returning a coherent result

The HTTP layer owns: parsing JSON, calling `Place`, serializing the result.

This separation is one of the scoring signals. **The engine package must not import `net/http`, `encoding/json`, or anything HTTP-shaped.**

## The decision tree

```
Place(cmd):
  validate(cmd)                      # decimals positive, type+side valid, etc.
  order = newOrder(cmd, e.nextID(), e.now())

  switch order.type:
    case stop, stop_limit:
      if triggerAlreadyHit(order, e.lastPrice):
        return order(rejected), nil
      e.stopBook.add(order)
      return order(armed), nil

    case market:
      trades = e.match(order)
      if order.remaining > 0:
        order.status = rejected   # market orders don't rest
      return order, trades

    case limit:
      trades = e.match(order)
      if order.remaining > 0:
        e.book.rest(order)
        order.status = resting if no trades else partially_filled
      else:
        order.status = filled
      return order, trades

  # after every successful match:
  if trades produced:
    e.lastPrice = trades[last].price
    triggered = e.stopBook.scan(e.lastPrice)
    for t in triggered:
      recursivelyPlace(t)        # see "stop cascade" below
```

## The match loop

The heart. ~30 lines. Let's build it carefully.

```go
func (e *Engine) match(taker *Order) []Trade {
    var trades []Trade
    book := e.oppositeBook(taker.side)  // taker buy → asks book

    for taker.remaining.IsPositive() && !book.empty() {
        bestLevel := book.bestLevel()

        if !crosses(taker, bestLevel.price) {
            break  // limit price too aggressive for the book
        }

        for taker.remaining.IsPositive() && bestLevel.queue.Len() > 0 {
            front := bestLevel.queue.Front()
            maker := front.Value.(*Order)

            // Self-match check (see file 07)
            if action := e.smp.check(taker, maker); action != smpProceed {
                if action == smpCancelMaker {
                    bestLevel.queue.Remove(front)
                    e.cancelMaker(maker)
                    continue
                }
                if action == smpCancelTaker {
                    taker.status = cancelled
                    return trades
                }
                // smpReject: bubble up
            }

            fillQty := decimal.Min(taker.remaining, maker.remaining)
            tradePrice := maker.price  // maker price wins

            trade := Trade{
                ID:           e.nextTradeID(),
                TakerOrderID: taker.id,
                MakerOrderID: maker.id,
                Price:        tradePrice,
                Quantity:     fillQty,
                TakerSide:    taker.side,
                CreatedAt:    e.now(),
            }
            trades = append(trades, trade)

            taker.remaining = taker.remaining.Sub(fillQty)
            maker.remaining = maker.remaining.Sub(fillQty)

            if maker.remaining.IsZero() {
                bestLevel.queue.Remove(front)
                maker.status = filled
                delete(e.orderIndex, maker.id)
            } else {
                maker.status = partiallyFilled
            }
        }

        if bestLevel.queue.Len() == 0 {
            book.removeLevel(bestLevel.price)
        }
    }

    return trades
}

func crosses(taker *Order, bookPrice decimal.Decimal) bool {
    if taker.typ == market {
        return true   // market crosses everything
    }
    if taker.side == buy {
        return taker.price.GreaterThanOrEqual(bookPrice)
    }
    return taker.price.LessThanOrEqual(bookPrice)
}
```

### Why this shape

- **Outer loop walks levels.** Best price first.
- **Inner loop walks the FIFO at one level.** Time priority within a level.
- **Break on no-cross.** Critical — without this, a limit order would walk the whole book.
- **Empty-level cleanup at the end of the inner loop.** Don't forget; empty levels accumulate otherwise.

## Worked example

Initial book:
```
ASKS:  500_050_000 → [o-A: 0.3, o-B: 0.5]    (o-A came first)
       500_100_000 → [o-C: 1.0]
BIDS:  499_950_000 → [o-D: 0.4]
```

Incoming: **buy limit 1.0 BTC @ 500_080_000** (taker = o-T)

Step-by-step:

1. Walk asks. Best = 500_050_000. Crosses? Yes (500_080_000 ≥ 500_050_000).
2. Inner loop at 500_050_000:
   - Match against o-A. fill = min(1.0, 0.3) = 0.3. Trade @ 500_050_000.
     - taker.remaining = 0.7, o-A.remaining = 0 → filled, removed.
   - Match against o-B. fill = min(0.7, 0.5) = 0.5. Trade @ 500_050_000.
     - taker.remaining = 0.2, o-B.remaining = 0 → filled, removed.
   - Level empty → remove from tree.
3. Walk asks. Best = 500_100_000. Crosses? No (500_080_000 < 500_100_000).
4. Break.
5. Taker remaining = 0.2 > 0, type=limit → rest at 500_080_000.

Result:
- 2 trades, both at 500_050_000.
- o-T status = `partially_filled`, remaining = 0.2.
- New book:
  ```
  ASKS:  500_100_000 → [o-C: 1.0]
  BIDS:  500_080_000 → [o-T: 0.2]
         499_950_000 → [o-D: 0.4]
  ```

Notice: trade prices are 500_050_000 (maker price), not 500_080_000 (taker limit). The taker got a better deal than they asked for.

## Market order specifics

```go
case market:
    trades = e.match(order)        // crosses() returns true unconditionally
    if order.remaining.IsPositive():
        order.status = rejected    // <-- crucial: don't rest
        // The trades already produced are still valid!
    else:
        order.status = filled
    return order, trades
```

Edge case: market buy on an empty asks book → 0 trades, status = rejected.

The PDF is explicit: *"Market orders either fill immediately or fail (reject remainder; do not rest)."* Read that twice.

### "What does 'reject remainder' mean if some fills happened?"

Trades that already executed are real. Only the unfilled portion is rejected. The order's final status is `rejected` (some exchanges call this `partially_filled_then_cancelled`; we collapse to `rejected` for simplicity — document this in your README).

Some exchanges instead set status = `partially_filled` with an explicit `cancel_reason = "market_no_liquidity"`. Either is defensible. Pick one.

## Limit order specifics

```go
case limit:
    trades = e.match(order)
    if order.remaining.IsPositive():
        e.book.rest(order)
        if len(trades) == 0:
            order.status = resting
        else:
            order.status = partiallyFilled
    else:
        order.status = filled
    return order, trades
```

Three terminal states: `filled`, `partially_filled`, `resting`. (Plus `cancelled` later via the cancel API.)

### Edge case: limit at a price that doesn't cross

Buy limit @ 499_900_000 on a book with best ask 500_050_000:
- `match()` enters the loop, checks `crosses()`, returns false, breaks immediately.
- 0 trades.
- Order rests at 499_900_000.
- Status = `resting`.

This is the common case for makers (passive liquidity).

### Edge case: limit at a crossable price, fills exactly

Buy limit 0.3 BTC @ 500_050_000 on the book above:
- 1 trade for 0.3 @ 500_050_000.
- Remaining = 0 → status = `filled`. No resting.

## Partial fills produce multiple trade records

The PDF: *"Partial fills are allowed and must produce multiple trade records."*

This is enforced naturally by the inner-loop structure above — every maker that fills produces one trade record, even if the taker is the same. The example earlier produced 2 trade records for one taker order.

Don't try to "merge" trades across makers. Each trade has its own maker, its own price (could differ across levels), its own ID.

## Cancel

```go
func (e *Engine) Cancel(id OrderID) error {
    handle, ok := e.orderIndex[id]
    if !ok {
        // Could also be in the stop book
        if stopHandle, ok := e.stopBook.index[id]; ok {
            e.stopBook.remove(stopHandle)
            return nil
        }
        return ErrNotFound
    }

    handle.level.queue.Remove(handle.node)
    handle.order.status = cancelled
    delete(e.orderIndex, id)

    if handle.level.queue.Len() == 0 {
        e.book(handle.side).removeLevel(handle.level.price)
    }

    return nil
}
```

Two indexes to check: the resting book index, the stop book index. An order is in exactly one (or already filled/rejected/cancelled).

### What about cancelling a `filled` or `cancelled` order?

Return `ErrNotFound` (or 404 from the HTTP layer). The order is no longer mutable.

What about cancelling an in-flight order whose `Place()` hasn't returned yet? Can't happen — the engine is single-writer (file 06). Place and Cancel are serialized.

## Coding-style guidance

1. **Take the engine struct as a receiver, not method args.** State stays encapsulated.
2. **Return values, not error codes.** `(Order, []Trade, error)` is the canonical shape.
3. **No goroutines inside `match()`.** Determinism (file 06) requires sequential matching.
4. **Pre-allocate the `trades` slice with reasonable capacity** (e.g. `make([]Trade, 0, 4)`) — most matches produce 0–4 trades; saves growth allocs.
5. **Don't log inside the match loop.** Profile killer. Return what happened; log at the API edge if needed.

## Common bugs to avoid

| Bug | How it bites |
|---|---|
| Forgetting to remove empty levels | Snapshots show ghost levels with qty 0 |
| Using taker.price for trade price | Hides the maker rebate; spec violation |
| Resting a market order | Worst possible bug — book corruption |
| Updating lastPrice mid-loop instead of after | Can incorrectly trigger a stop in the middle of a fill |
| Iterating a Go map for the FIFO | Non-deterministic — different fill order on each run |
| Mutating an order while it's in the FIFO via stale pointer | Use the order index handle, not raw pointers |

## Defending the design in the interview

> "The match function is ~30 lines, no goroutines, sequential. The outer loop is over price levels by best-price; the inner is FIFO at the level. Trades are emitted with maker price. Limit remainder rests; market remainder is rejected. After every successful match I update last-trade-price and scan the stop book — that handles cascades. The engine has zero HTTP imports and is driven from tests."

That's the whole pitch. Practice it.
