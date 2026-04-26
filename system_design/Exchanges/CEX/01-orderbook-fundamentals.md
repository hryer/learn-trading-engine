# 01 — Order Book Fundamentals

**Status: [CHALLENGE] — required**

Vocabulary and mental model. Every later file assumes you know these terms.

## The order book

An order book is a list of unfilled buy and sell intentions for one trading pair (e.g. `BTC/IDR`).

```
                    ASKS (sell side, sorted ascending)
  price        qty     orders
  500_100_000  0.30   [o-7]
  500_050_000  1.20   [o-3, o-9]   ← best ask (lowest sell)
  -----------------------------------  spread = 50_000
  499_950_000  0.50   [o-12]       ← best bid (highest buy)
  499_900_000  2.00   [o-1, o-4, o-5]
  499_800_000  0.10   [o-8]
                    BIDS (buy side, sorted descending)
```

Key vocabulary:

| Term | Meaning |
|---|---|
| **Bid** | A buy order resting on the book |
| **Ask / Offer** | A sell order resting on the book |
| **BBO** | Best Bid and Offer = the top of each side |
| **Spread** | best_ask − best_bid (always ≥ 0 in a healthy book) |
| **Level** | All orders at a single price |
| **Depth** | Total quantity available at or near the BBO |
| **Last trade price** | The price of the most recent executed trade — drives stop triggers |
| **Maker** | The order already resting on the book (provides liquidity) |
| **Taker** | The incoming order that crosses the spread (consumes liquidity) |
| **Cross** | When best_bid ≥ best_ask — a healthy book never crosses; if it does, the engine matches until it doesn't |

## Order types (the four in scope)

### Limit order
"Buy up to 0.5 BTC at 500_000_000 IDR or better."

- Has a price.
- If the book has matching orders at or better, fills immediately as a taker.
- Any unfilled remainder **rests** on the book at its limit price as a maker.

### Market order
"Buy 0.5 BTC at whatever price."

- No price. Walks the opposite side, consuming as much as it can.
- Any unfilled remainder is **rejected** (does NOT rest, does NOT convert to limit).
- If the book is empty on the opposite side, the entire order is rejected.

### Stop (stop-market)
"If price reaches 510_000_000, then buy 0.5 BTC at market."

- Has a `trigger_price`. While untriggered, sits in a separate **stop book** — invisible to the order book snapshot.
- When `last_trade_price ≥ trigger_price` (for buy) or `≤ trigger_price` (for sell), the order **arms → fires** and converts to a market order.
- Common use: stop-loss to exit a losing position.

### Stop-limit
"If price reaches 510_000_000, then place a limit buy at 510_500_000."

- Has both `trigger_price` and `price`.
- On trigger, becomes a regular limit order at `price`.
- Used to bound slippage that a stop-market would suffer.

### Order types you'll see mentioned but won't build
IOC, FOK, post-only, iceberg, trailing-stop — see [10-next-steps-features.md](10-next-steps-features.md).

## Price-time priority (FIFO)

The matching rule used by virtually every traditional exchange (Binance, CME, Nasdaq, Hyperliquid):

1. **Price priority** — better-priced orders match first.
   - For incoming buy: the *lowest* ask price matches first.
   - For incoming sell: the *highest* bid price matches first.
2. **Time priority** — at the same price level, the order that arrived **first** matches first.

So at price 500_050_000 with `[o-3 (1.0), o-9 (0.2)]`, an incoming taker takes from `o-3` until it's empty, then `o-9`.

This is why each price level needs an **ordered queue**, not a set. See [02-data-structures.md](02-data-structures.md).

### Alternatives (don't build, but know they exist)

- **Pro-rata** — at a level, fills split proportionally by size. Used in some CME products.
- **Size-time** — large orders before small at the same price (rare).

For this challenge: strict price-time FIFO. Don't deviate.

## Order lifecycle (status field)

```
                  ┌─────────┐
                  │ pending │  (in-flight, never visible)
                  └────┬────┘
                       │
        ┌──────────────┼──────────────┬──────────────┐
        ▼              ▼              ▼              ▼
   ┌─────────┐   ┌──────────┐   ┌──────────┐   ┌──────────┐
   │ armed   │   │ resting  │   │ filled   │   │ rejected │
   │(stops)  │   │(limits)  │   │          │   │          │
   └────┬────┘   └────┬─────┘   └──────────┘   └──────────┘
        │             │
        ▼             ▼
   ┌──────────┐  ┌────────────────┐
   │triggered │  │partially_filled│
   └──────────┘  └────────────────┘
                       │
                       ▼
                  ┌──────────┐    ┌───────────┐
                  │ filled   │ or │ cancelled │
                  └──────────┘    └───────────┘
```

The PDF spec collapses `triggered` into a transient state. In practice the order goes from `armed → (transient: behaves as market/limit) → filled / partially_filled / rejected / resting`. You don't need to expose `triggered` as a persistent status.

## Maker vs taker (and why trade price = maker price)

When a taker buy at 500_100_000 hits a resting ask at 500_050_000:
- The trade executes at **500_050_000** (the maker's price).
- The taker gets a 50_000 IDR/BTC price improvement.

This is universal exchange convention. The maker posted first, set the price, and is rewarded with priority. The taker pays the spread for immediacy.

In real exchanges, makers also pay a lower fee (or get a rebate) — the "maker-taker model." Out of scope for the challenge (no fees), but design your `Trade` struct with `taker_side` recorded so a fee module could be plugged in later.

## Snapshot semantics

`GET /orderbook?depth=10` returns the top 10 price levels per side, **aggregated**. Individual order IDs are not returned. Stop orders (untriggered) are excluded.

```json
{
  "bids": [
    {"price": "499950000", "quantity": "0.5"},
    {"price": "499900000", "quantity": "2.0"}
  ],
  "asks": [
    {"price": "500050000", "quantity": "1.2"},
    {"price": "500100000", "quantity": "0.3"}
  ]
}
```

Two implementation notes:
- **Aggregate at read time, not write time.** Storing `total_qty_at_level` as a running sum is a footgun — every cancel/fill/partial requires updating it atomically, and bugs there silently corrupt snapshots. Sum from the FIFO queue when the snapshot is requested. Cheap.
- **Copy out a snapshot under the lock.** Don't return live references to internal slices.

## What to internalize before moving on

- [ ] BBO, spread, level, maker, taker, last trade price.
- [ ] Why limit orders rest but market orders don't.
- [ ] The trigger semantics for buy-stop vs sell-stop.
- [ ] Trade price = maker price.
- [ ] Snapshot is aggregated and excludes armed stops.
