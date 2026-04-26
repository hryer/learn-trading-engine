# 07 — Self-Match Prevention (SMP)

**Status: [CHALLENGE] — required**

The PDF: *"Self-match prevention: an order whose owner would match their own resting order — pick a policy (cancel newest, cancel oldest, reject). Document your choice."*

Short topic. Pick a policy, implement it consistently, document it.

## Why SMP exists

Real markets disallow wash trades — a trader matching against their own order, creating fake volume without changing position. Reasons:

1. **Regulatory.** Exchanges must demonstrate they detect and prevent wash trading (US: CFTC Rule 1.38).
2. **Tax.** Some jurisdictions disallow wash sales for loss harvesting.
3. **Hygiene.** Even when not malicious, an algo's buy and sell legs can accidentally cross — exchanges prevent this so the trader doesn't pay both sides of the spread + maker/taker fees needlessly.

For this challenge there are no fees and no regulator. SMP is asked because it forces you to think about *policy* — a thing the engine must encode, not just data structures.

## The three classic policies

### Policy 1 — Cancel newest (CN)

The incoming taker order is cancelled. The maker stays.

**Pros:**
- Simplest to implement (taker is in your hand; just stop matching and mark cancelled).
- Preserves resting liquidity, which is the social good of an exchange.
- CME default for many products.

**Cons:**
- Surprising for the trader who just submitted — their order vanishes.
- If multiple of trader's own resting orders are at the top of the book, the taker is cancelled even if it could have matched against other users below them.

### Policy 2 — Cancel oldest (CO)

The resting maker order is cancelled. The taker continues, possibly matching against the next maker (if any).

**Pros:**
- Lets the new order proceed — better UX for the active trader.
- The maker was "stale" anyway; the trader has up-to-date intent in the new order.

**Cons:**
- Removes liquidity from the book, which other traders were relying on.
- If the trader has a stack of resting orders, the engine may cancel several of them as the taker walks down.

### Policy 3 — Reject (RJ)

The incoming order is rejected outright (no match, no rest, error to caller).

**Pros:**
- Most explicit: tells the trader their algo just collided with itself.
- No implicit cancellations of either side.

**Cons:**
- Worst UX during volatility. Traders prefer something to happen.
- Different return semantics from a normal match — adds an error path.

### Policy 4 — Decrement-and-cancel (DC)

CME extension. Reduce both sides by `min(taker_qty, maker_qty)`, cancel the smaller, keep the larger with reduced quantity.

Out of scope for this challenge. Mention in the next-steps section if asked.

## What to pick

**Cancel newest.** It's the boring CME-default choice and the simplest to implement.

In the README:

> *Self-match policy: cancel-newest. When an incoming taker would match against a resting order owned by the same `user_id`, the taker order is cancelled (status `cancelled`) and any trades already produced against other makers in the same call are kept. The resting maker is unaffected. Reasons: simplest to implement; CME default; preserves the book's liquidity (the social good of an exchange).*

## Implementation

In the inner match loop (file 03):

```go
for taker.remaining.IsPositive() && bestLevel.queue.Len() > 0 {
    front := bestLevel.queue.Front()
    maker := front.Value.(*Order)

    if maker.userID == taker.userID {
        // Cancel-newest policy
        taker.status = cancelled
        return trades   // any trades already produced are still valid
    }

    // ... normal fill logic
}
```

That's it. Three lines.

### Subtle case: multiple makers from same user, mixed with other users

Book at one price level: `[u1-order, u2-order, u1-order, u3-order]`. Taker is `u1`.

Cancel-newest: taker cancels on the first encounter with `u1-order`. The taker never reaches `u2-order` or `u3-order` even though it could have matched them legitimately.

Defensible because:
- The matching loop is FIFO by time. Skipping a maker would violate price-time priority for other users.
- The trader can resubmit a smaller order or use cancel-then-place.

Cancel-oldest would skip past `u1-order` and match `u2-order`, then encounter the second `u1-order` and skip again. Defensible too, but more behavior to specify.

Document whichever you pick. Consistency > cleverness.

## Edge cases worth a test

1. **Taker buy, sole resting maker is same user → taker cancelled, no trades.**
2. **Taker buy, first maker is different user (fills), second maker is same user → 1 trade emitted, taker then cancelled.**
3. **Taker buy with no SMP collision → normal flow, no test needed for SMP per se.**
4. **Taker is a market order, all liquidity is from same user → taker cancelled (not rejected — the cancel happens before the "no liquidity → reject" check).**

## What about stop orders?

A stop becomes a market or limit order on trigger. The triggered order is still owned by the original user. SMP applies normally when it enters the matching loop.

Edge case: a user places a buy stop and a sell limit on the same book. If the buy stop triggers and walks into the user's own sell limit, SMP cancels the (triggered) buy. Document this; it's an easy follow-up question.

## Defending in the interview

> "Cancel-newest. The incoming taker is cancelled the moment it would match its own resting order. Any trades against other makers earlier in the same call are kept. I picked cancel-newest because it's the CME default, it preserves resting liquidity which is the exchange's value to other users, and it's the simplest to implement — three lines in the match loop. Cancel-oldest is also defensible but cascades more weirdly when the user has multiple orders on the book."

Length: 30 seconds. That's the whole answer.
