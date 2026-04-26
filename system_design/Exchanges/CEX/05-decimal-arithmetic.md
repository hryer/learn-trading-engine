# 05 — Decimal Arithmetic

**Status: [CHALLENGE] — required**

The PDF: *"Decimal-correct. Prices and quantities must not use float64 arithmetic for matching or fill math."*

This is the easiest scoring point to hit and the most expensive to retrofit. Pick a representation **before you write any matching code**.

## Why floats are forbidden

```go
0.1 + 0.2 == 0.30000000000000004
```

float64 stores numbers as `mantissa × 2^exponent`. Decimals like 0.1 are repeating in binary — they can't be represented exactly. Every arithmetic op accumulates error.

For an order book this is catastrophic:
- A user places a sell limit at 0.5. You store 0.4999999999999.
- An incoming buy at 0.5 tries to cross — `taker.price >= maker.price` is `false`. Match doesn't happen.
- Or the inverse: imprecise comparisons cause a match that shouldn't have happened. Now the user is filled at a price they didn't authorize.

Real exchanges have lost money to this. Don't.

## Two acceptable representations

### Option A — `shopspring/decimal` (arbitrary-precision)

```go
import "github.com/shopspring/decimal"

p, _ := decimal.NewFromString("500000000")
q, _ := decimal.NewFromString("0.5")
notional := p.Mul(q)            // exact
isCross := taker.Price.GreaterThanOrEqual(maker.Price)
```

**Pros:**
- Reads naturally in code (`Add`, `Sub`, `Mul`, `Div`, `GreaterThan`, etc.).
- Handles arbitrary precision — no risk of overflow at large notionals.
- JSON serialization built in (`MarshalJSON` returns a string, no precision loss).

**Cons:**
- ~10× slower than int64 ops (each op allocates).
- ~2–4× more memory per value.
- For HFT (>1M ops/sec) this matters. For this challenge it does not.

### Option B — fixed-point int64 (HFT-style)

Pick a scale: prices in 1/100 IDR (so 500_000_000.00 IDR is `int64(50_000_000_000)`), quantities in satoshis (1 BTC = `int64(100_000_000)`).

```go
type Price int64    // scale: 100 = 1.00 IDR
type Quantity int64 // scale: 100_000_000 = 1 BTC

const (
    priceScale = 100
    qtyScale   = 100_000_000
)

// Notional needs care: price * qty would overflow int64 for large amounts.
// Multiply in big.Int then scale down, or use int128.
```

**Pros:**
- ~5–20 ns per op. Zero allocation.
- Cache-friendly. Hyperliquid uses this.

**Cons:**
- Overflow risk if you don't think through scales. Notional = price × qty can exceed int64 range.
- Fixed precision — must be set per pair. New pair with different tick size requires careful scaling.
- JSON serialization is custom (you must convert int64 → "0.50000000" strings).

### What to pick for the challenge

**`shopspring/decimal`.** Reasons:

1. The PDF says decimal-correct, not "fast." Performance is not the rubric.
2. Less code, fewer chances for off-by-one in scale conversions.
3. Reads naturally in tests and assertions.
4. JSON in/out is automatic.

If asked "why not fixed-point?":

> "For this scope, decimal precision wins. Fixed-point is faster but introduces scale conversions at every API boundary. Hyperliquid uses fixed-point because they need every nanosecond on the matching path. We don't. The matching path is dominated by JSON encoding and HTTP overhead — the arithmetic is rounding error in the budget."

## API boundaries: strings, not floats

JSON has no decimal type. Numbers are float64-shaped. So:

```json
// WRONG — JSON parser turns this into float64, precision destroyed
{"price": 500000000, "quantity": 0.5}

// RIGHT — strings preserved verbatim through encode/decode
{"price": "500000000", "quantity": "0.5"}
```

`shopspring/decimal` handles this for you:

```go
type PlaceOrderRequest struct {
    Price    decimal.Decimal `json:"price"`
    Quantity decimal.Decimal `json:"quantity"`
}
// Marshals/unmarshals as JSON strings by default. Safe.
```

For fixed-point, you write custom Marshal/Unmarshal that emits/parses strings.

The PDF examples confirm:
```json
"price": "500000000",
"quantity": "0.5"
```

Those are strings. Your handlers must accept and emit strings.

## What to do with rounding

In this challenge the math is simple — no division, so no rounding issues.

For real engines:
- **Notional** = price × qty: exact under decimal, may need bounded-precision rounding under fixed-point.
- **Fees** = trade_value × fee_rate: bank-style rounding (round-half-to-even) is the convention. Out of scope here but mention it if asked.
- **Pro-rata fills** (not in this scope): involves division, which definitely rounds. Pick rounding mode (down for the customer, up for the house, or unbiased — half-to-even).

## Validation at the boundary

Before any decimal hits the engine, validate:

```go
func validate(req PlaceOrderRequest) error {
    if !req.Quantity.IsPositive() {
        return errors.New("quantity must be > 0")
    }
    if req.Type == limit && !req.Price.IsPositive() {
        return errors.New("limit order requires positive price")
    }
    if req.Type == stopLimit && !req.Price.IsPositive() {
        return errors.New("stop_limit requires positive price")
    }
    if req.Type.NeedsTrigger() && !req.TriggerPrice.IsPositive() {
        return errors.New("stop orders require positive trigger_price")
    }
    // Reject NaN, Inf, negative — decimal lib makes most of these impossible.
    return nil
}
```

Don't propagate invalid decimals into the engine. The engine assumes its inputs are valid.

## Comparison correctness

```go
// CORRECT
if taker.Price.GreaterThanOrEqual(maker.Price) { ... }

// WRONG — shopspring/decimal doesn't overload operators
if taker.Price >= maker.Price { ... } // compile error, but tempting in pseudocode
```

Use the methods: `Cmp`, `Equal`, `GreaterThan`, `GreaterThanOrEqual`, `LessThan`, `LessThanOrEqual`, `IsZero`, `IsPositive`, `IsNegative`.

For fixed-point int64 you can use `==`, `<`, `>` directly. Test thoroughly that scales are consistent on both sides.

## Subtraction and zero

After every fill:
```go
taker.remaining = taker.remaining.Sub(fillQty)
if taker.remaining.IsZero() { ... }
```

`IsZero()` is *exact* under decimal. Not "epsilon close to zero" like with floats. This is the entire reason we chose decimals.

## Test it

```go
func TestDecimalIsExact(t *testing.T) {
    a := decimal.RequireFromString("0.1")
    b := decimal.RequireFromString("0.2")
    sum := a.Add(b)
    expected := decimal.RequireFromString("0.3")
    if !sum.Equal(expected) {
        t.Fatalf("want %s got %s", expected, sum)
    }
}
```

This passes with `shopspring/decimal`. With float64 it fails. That one test is your sanity check that the foundation is right.

## Summary

- **Use `shopspring/decimal`** for the challenge.
- **Strings in JSON, decimals internally.**
- **Validate at the boundary, trust internally.**
- **Use methods, not operators.**
- **No floats anywhere in the matching path.**

The interview probe is usually: "Why decimals?" followed by "Why not fixed-point?" Have both answers ready.
