# Curve V1 in Desmos — Build It Layer by Layer

> **Prerequisite:** if `x, y, A, D, K₀` still feel like random letters, read [reading-the-graph.md](./reading-the-graph.md) first — this doc assumes you know what each symbol *is*.

The goal: paste one equation at a time into Desmos and watch the Curve V1 shape emerge from its two ingredients (constant sum + constant product). Each step adds one line and tells you what to look for.

---

## 0. Setup — create the sliders

Before any equation, create two sliders in Desmos. Just type the letter on its own line — Desmos will offer "add slider". Click it and set:

- **`D`** — range `100` to `1000`, step `10`, start at `200`. This is the pool size.
- **`A`** — range `1` to `500`, step `1`, start at `10`. This is the amplification (how "stablecoin-like" the pool is).

These are the two knobs. You'll drag them later.

**Desmos syntax quick reference:**
- Type `K_0` to get the `K₀` subscript.
- `*` is optional — juxtaposing letters like `A K D` multiplies them.
- Use `^{...}` for exponents: `D^{2}`, `(D/2)^{2}`.

---

## 1. Layer 1 — just constant sum

**Paste:**
```
x + y = D
```

**Observe:** a straight diagonal line from `(D, 0)` to `(0, D)`, passing through the balance point `(D/2, D/2)`.

**Why it matters:** if the AMM rule were "total must equal `D`", every point on this line would be a valid pool state. Great prices (slope = −1 = 1:1) — but the pool could drain all the way to either axis. Not safe.

---

## 2. Layer 2 — just constant product

**Paste:**
```
x y = (D/2)^{2}
```

**Observe:** a hyperbola passing through the same balance point `(D/2, D/2)`, flying off to infinity along both axes.

**Why it matters:** this curve never touches either axis — that's drain protection. But the price moves a lot even on small trades (the slope changes fast). Great safety, bad prices.

---

## 3. Layer 3 — the full Curve V1 (the hybrid)

**Paste these two lines** (each on its own Desmos expression):

```
A K_{0} D (x + y) + x y = A K_{0} D^{2} + (D/2)^{2}
```

```
K_{0} = (x y) / (D/2)^{2}
```

**Observe:** a curve that **hugs the straight line** in the middle and **bends toward the hyperbola** at the edges.

**Why it matters:** *this is the whole trick.* Middle = line-like (low slippage, good prices). Edges = hyperbola-like (drain-proof). `K₀` is computed from wherever you are on the curve — you don't set it; it's a readout.

---

## 4. Play the `A` slider

Drag `A` from `1` up to `500`.

**Observe:**
- At `A = 1`, the curve barely hugs the line — it looks mostly like the hyperbola.
- At `A = 500`, there's a wide **flat middle zone** that sticks tight to the diagonal line before bending sharply at the edges.

**Why it matters:** `A` controls how "stablecoin-like" the pool is. Big `A` = wide flat zone = great prices near balance. Small `A` = behaves more like plain Uniswap.

---

## 5. Play the `D` slider

Drag `D` between `100` and `1000`.

**Observe:** the whole curve scales up and down keeping its shape. The balance point moves along the diagonal from `(50, 50)` to `(500, 500)`.

**Why it matters:** `D` = pool size. Bigger `D` = same shape, bigger pool. More liquidity, same trading experience.

---

## 6. What to do next — try V2 (concentrated liquidity)

Once the V1 shape feels intuitive, read [equation.md §5](./equation.md#L199) (Curve V2) and add one more slider:

- **`g`** — range `0.00001` to `0.1`, step `0.00001`, start at `0.01`. This is γ (gamma).

Then add one more expression (the sharpened K):

```
K = K_{0} (g / (g + 1 - K_{0}))^{2}
```

And edit the main equation to replace every `K_{0}` with `K`:

```
A K D (x + y) + x y = A K D^{2} + (D/2)^{2}
```

Now drag `g` down toward `0.00001` and watch the flat zone **snap** thin — that's concentrated liquidity. Small γ = razor-thin flat zone = stablecoin-level prices only in a narrow band around the peg, hyperbola everywhere else.

---

Stuck on what a letter means? Back to [reading-the-graph.md](./reading-the-graph.md).
