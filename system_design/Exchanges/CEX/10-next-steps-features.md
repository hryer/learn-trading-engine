# 10 — Next Steps: Features

**Status: [NEXT STEP] — DO NOT IMPLEMENT for the challenge.**

The PDF excludes all of these. Read for vocabulary so you can speak about them in the design walkthrough. **Do not build them.**

Topics:
1. Advanced order types (IOC, FOK, post-only, iceberg, trailing-stop)
2. Market data feeds (WebSocket, L2 deltas, snapshots)
3. Pre-trade risk (balance, position, margin)
4. Fees and rebates (maker-taker model)
5. Operational: rate limiting, auth, observability

## 1. Advanced order types

### IOC — Immediate-or-Cancel

"Fill what you can right now, cancel any remainder. Don't rest."

```
Behavior = limit order, but with "remainder rejected" instead of "remainder rests".
```

Implementation: identical match loop, then after the loop:

```go
case limit_ioc:
    trades = e.match(order)
    if order.remaining.IsPositive():
        order.status = cancelled  // not "rejected" — IOC is intentional partial
    else:
        order.status = filled
```

A market order is essentially an IOC at "any price." Some engines collapse market into IOC at price=0 (buy) or price=∞ (sell).

### FOK — Fill-or-Kill

"Fill the entire quantity right now, or reject the entire order."

Cannot partial-fill. Either 100% fills, or 0% fills.

Implementation requires a two-pass approach: first scan the book to verify enough liquidity exists *at acceptable prices*, then execute. Otherwise you get partial fills you can't undo.

```go
case limit_fok:
    if !canFullyFill(order, e.book):
        order.status = rejected
        return order, nil
    trades = e.match(order)
    // by precondition, taker.remaining is now 0
    order.status = filled
```

### Post-only

"Place this limit order, but only if it would NOT match anything (i.e., only if it can rest as a maker). If it would cross, cancel it."

Used by market makers who want to guarantee the maker rebate.

```go
case limit_post_only:
    if wouldCross(order, e.book):
        order.status = cancelled
        return order, nil
    e.book.rest(order)
    order.status = resting
```

`wouldCross` checks whether the limit price crosses the opposite side's best price. No matching, no trades.

### Iceberg

"Show only X of the total quantity on the book. As the visible portion fills, refill from the hidden portion."

```go
type IcebergOrder struct {
    *Order
    Visible  decimal.Decimal  // shown qty per slice
    Hidden   decimal.Decimal  // remaining hidden
}
```

When the visible portion fills:
1. Mark visible portion as filled.
2. If hidden > 0: create a new visible portion = `min(visible_size, hidden)`, deduct from hidden, append to the level's FIFO with a fresh time priority.

The "fresh time priority" is the key economic feature — iceberg orders sacrifice queue position to avoid showing size.

Trade-off: cheaper than walking with a market order (you stay maker), but slower fills (each slice rejoins the back of the FIFO).

### Trailing stop

A stop with a `trigger_price` that *moves* with the market.

```
Buy trailing stop, offset = 100:
  As price drops, trigger_price drops with it (= last_low + 100).
  As price rises, trigger_price stays put.
  When market reaches the trigger, fire as market.
```

Sell trailing stop is symmetric: trigger trails up as price rises, stays put on dips.

Storage: same stop book, but trigger_price is recomputed on every `last_trade_price` update for trailing orders.

```go
func (e *Engine) updateTrailingStops(lastPrice decimal.Decimal) {
    for _, ts := range e.trailingStops {
        if ts.side == sell && lastPrice.Sub(ts.offset).GreaterThan(ts.triggerPrice) {
            ts.triggerPrice = lastPrice.Sub(ts.offset)
        }
        if ts.side == buy && lastPrice.Add(ts.offset).LessThan(ts.triggerPrice) {
            ts.triggerPrice = lastPrice.Add(ts.offset)
        }
    }
}
```

### Order types you don't normally need to know

- **Hidden / dark** — fully invisible; matches only on fills, not in snapshots.
- **MIT (Market-If-Touched)** — like a stop but in the *favorable* direction (buy when price drops to X).
- **Discretionary** — has a "real" limit and a "show" limit; matches inside a band.

## 2. Market data feeds

REST snapshot is fine for small clients. Real exchanges push updates via WebSocket.

### Two channels per symbol

**A. Trade feed.** Every executed trade, in order.

```json
{"channel":"trades.BTCIDR","trade":{"id":"t-1","price":"500050000","qty":"0.3","side":"buy","ts":"..."}}
```

**B. Book diff feed.** Every level change.

```json
{"channel":"book.BTCIDR","update":{"side":"bid","price":"499950000","qty":"0.5"}}
```

`qty: "0"` means the level was deleted (last order at that level cancelled or filled).

Clients maintain a local copy of the book by:
1. Calling `GET /orderbook` for an initial snapshot, with a sequence number.
2. Subscribing to the diff feed.
3. Applying diffs whose sequence > snapshot's sequence.

### Sequence numbers

Each book diff carries a sequence number. Clients verify continuity (`prev_seq + 1 == this_seq`); on a gap, they re-snapshot.

Producing the sequence: the engine emits a `BookDelta` event after every state change, with the engine's monotonic write counter. This is the Disruptor pattern from [09](09-next-steps-architecture.md).

### Throttling

Some exchanges publish diffs at a fixed rate (e.g. 100ms snapshots) instead of every change. This batches deltas into a smaller stream — easier on retail clients with slow connections.

L2 = top N price levels aggregated. L3 = full order book including individual order IDs. L3 is rarely exposed to public clients (privacy).

## 3. Pre-trade risk

Before an order enters the engine, check:

- **Balance.** Does the user have enough cash (for buys) or asset (for sells)?
- **Position limit.** Some users have caps on max position size (e.g. for derivatives).
- **Margin.** For perpetuals/futures, does the new position fit within the user's collateral?
- **Self-trade across orders.** A more aggressive form of SMP that pre-checks all of a user's resting orders before placing a new one.
- **Price band.** Reject orders priced too far from last trade (e.g. ±10%) to catch fat-finger errors.
- **Rate limits.** Per-user max orders per second.

These run in a **risk gateway** in front of the engine. Cleanly separated: risk says yes/no, engine assumes inputs are valid.

```
┌────────┐   ┌──────┐   ┌────────┐
│Gateway │ → │ Risk │ → │ Engine │
└────────┘   └──────┘   └────────┘
```

### Why the engine doesn't do risk

- Risk needs to read user balances/positions, which live in a wallet service.
- The engine is meant to be deterministic and pure. Reaching out to a wallet service breaks that.
- Risk is per-user; matching is per-symbol. Different shard keys.

In a monolith POC, risk + engine can be one service. In production they're separate.

## 4. Fees and rebates

### The maker-taker model

| Side | Fee |
|---|---|
| Maker | Lower fee, sometimes negative (rebate) |
| Taker | Higher fee |

Why: incentivizes makers to provide liquidity. Tighter spreads, deeper books.

Typical schedule (illustrative):
- Maker: 0.02% (or +0.01% rebate for top tiers)
- Taker: 0.05%

Tiered: high-volume users (or high-VIP-balance users) get lower fees.

### Where fees are computed

Not in the matching engine. The engine emits `Trade` events. A fee service consumes those, looks up the user's tier, computes maker/taker fees, debits the user's wallet.

```go
type Trade struct {
    // ... matching fields ...
    TakerSide   Side
    // No fee field. Fees computed downstream.
}
```

### Rounding

Banker's rounding (round-half-to-even) for fee computation. Per-trade rounding may diverge from per-fill rounding by 1 satoshi; pick one and document.

## 5. Operational concerns

### Authentication

The PDF: *no auth*. Production: HMAC-signed requests (Binance API key model) or JWT. The auth layer sits in front of the gateway.

### Rate limiting

Token bucket per user, per IP. Different limits for placing vs. canceling vs. reading.

### Observability

- **Metrics**: orders placed/sec, trades/sec, p50/p99 match latency, book depth, queue length per level.
- **Tracing**: per-request OpenTelemetry spans.
- **Structured logs**: every order with userID, type, status, latency. Aggregated to a log store.
- **Audit trail**: every order ever placed, immutable, kept forever. WAL is your friend.

### Admin / kill-switch

Real exchanges have a "halt trading" admin endpoint. When triggered:
- Engine stops accepting new orders.
- All resting orders remain.
- Snapshot/cancel work normally.

Used during incidents (matching bug suspected, market making algo gone rogue, regulatory pause). Out of scope here — but mention you'd add it.

### Circuit breakers

Auto-halt if last-trade-price moves > X% in Y seconds. Stops cascade flash-crash scenarios. Requires the engine to be aware of "halted" state.

## 6. The "what's next" list for the README

If you list these in priority order, pick:

1. **Persistence (WAL + snapshot).** Highest impact: survives restart.
2. **Market data WebSocket feed.** Second-highest: enables real clients.
3. **Per-symbol sharding.** Once you have >1 pair to support.
4. **Pre-trade risk gateway.** Required before real money.
5. **Fee schedule + maker-taker accounting.** Required before charging.
6. **Advanced order types (IOC, FOK, post-only).** Cheap to add given the existing engine.
7. **Iceberg, trailing stop.** Higher value, more code.

Stop at 5–7 entries in the README. Anything more is fluff.

## Defending in the interview Part 3 (Extension exercise)

The interviewer will pick one and ask you to design it on the spot. Have a 2-minute sketch ready for each:

**WAL persistence:** "Append-only log on disk; one write per command before applying. Group commit every K commands or X ms for throughput. On startup, replay log; engine determinism guarantees reconstruction. Add periodic snapshots + log truncation for bounded recovery."

**Multi-symbol sharding:** "One engine per pair, each in its own goroutine. Gateway routes by symbol. No cross-pair coordination at matching layer. Per-pair WAL, per-pair sequence. Risk and wallet sit above, sharded per user."

**WebSocket feed:** "Engine emits events to a Disruptor ring (file 09). One consumer is the WS publisher: per subscribed client, push relevant events. Clients open a TCP/WS connection, subscribe by channel name, receive diffs with sequence numbers. Snapshots delivered via REST."

**Iceberg:** "New order type with `visible` and `total` quantity. Internally we slice it: only `visible` qty enters the FIFO with normal time priority. As that slice fills, allocate the next slice from the hidden remainder, append to FIFO with fresh time priority. Trade-off documented: maker rebate preserved, queue position reset on each refill."

**Pre-trade risk:** "Separate service, sits in front of engine. Reads user balance/position from wallet service (cached). On place, calculates worst-case impact (margin for derivatives, available cash for spot), accepts or rejects. Engine sees only validated commands. Wallet service is the source of truth for balances and is the system of record for fees collected."

Two minutes each. Don't go deeper unless asked.

## Final note

You don't need to *master* any of this. You need to **mention it credibly** so the interviewer knows you've thought about the road from "case study" to "production." The PDF rubric is explicit: *correctness, data modeling, concurrency, code quality, communication.* Communication includes "I know what I didn't build, and I know how I'd build it."
