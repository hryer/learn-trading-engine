# Reading the Graph — Before the Math

> **Read this first** if the symbols in [equation.md](./equation.md) (`A`, `K₀`, `D`, `x`, `y`) feel like random letters. This doc grounds every symbol so the math actually *means* something.

---

## 1. What each letter is

| Symbol | Kind | Plain meaning |
|---|---|---|
| `x`, `y` | **State** (graph axes) | How much of each token is in the pool *right now*. A trade changes these. |
| `D` | **Parameter** (slider) | The "size" of the pool. Roughly `x + y` when balanced. Constant across trades — only changes when someone adds or removes liquidity. |
| `A` | **Parameter** (slider) | Amplification. A plain number the pool designer picks. Higher A = flatter middle. |
| `K₀` | **Derived** (not a slider!) | Computed *from* `x`, `y`, `D`. Tells the equation how balanced the current pool is. You never set K₀ — it just falls out. |
| `N` | Constant | Number of tokens. `N = 2` for Desmos since Desmos only has `x` and `y`. |

**The key split:** `x, y` are the *axes* of the graph. `A, D` are *knobs* (sliders). `K₀` is a *readout* that the equation computes internally.

---

## 2. What a point on the curve actually means

> **Every point `(x, y)` on the curve = one legal state the pool can be in.**

Trading moves you *along* the curve: a user swaps token A for token B, which decreases `x` and increases `y` (or vice versa), sliding the pool's state to a new point on the same curve.

The **slope** at your current point = the current exchange rate between the two tokens.

- Slope ≈ −1 → rate is close to 1:1.
- Very steep slope → you'd give up a lot of one token for very little of the other (bad price).

---

## 3. Visual → trading reference

What to look for on the graph, and what it means in trading terms:

- **Balance point `(D/2, D/2)`** — the pool holds equal amounts of both tokens. Price is 1:1. `K₀ = 1`. This is where the curve is flattest.
- **Flat middle zone** — the region where the curve hugs the diagonal line. Low slippage here — trading is cheap, the price barely moves. This is the "stablecoin behavior" zone.
- **Bent edges near the axes** — the curve refuses to touch `x = 0` or `y = 0`. This is *why the pool can't be drained*: reaching an empty side would require infinite input of the other token. The steeper the bend, the worse the price but the safer the pool.
- **`K₀` visually** — how close you are to the balance point. Dead center → `K₀ = 1`. Near an axis → `K₀ → 0`. It's a readout of *where you are on the curve*, not a dial you turn.

See the green-curve SVG in [equation.md §4](./equation.md#L168-L193) for a picture of this shape overlaid with the straight line and hyperbola it blends.

---

## 4. If you only remember three things

1. **`x, y` are the axes; `A, D` are knobs; `K₀` is a readout.**
2. A point on the curve is a pool state. Trading slides you along the curve.
3. Flat middle = good prices, bent edges = drain protection, `A` controls how wide the flat zone is.

---

Ready to see it? Go to [desmos-walkthrough.md](./desmos-walkthrough.md) and build the graph layer by layer.
