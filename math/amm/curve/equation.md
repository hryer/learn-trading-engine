# Curve V1 & V2 AMM Equations — A Friendly Walkthrough

> **TL;DR** — Curve V1 blends a *straight line* (constant sum) with a *hyperbola* (constant product) to give low slippage near the peg but still protect the pool from being drained. Curve V2 is the same equation, but with one extra knob (γ, gamma) that **sharpens** the blend so liquidity concentrates around any chosen price — not just 1:1.

---

## Table of Contents
1. [Background: What is an AMM?](#1-background)
2. [The Two Building Blocks](#2-the-two-building-blocks)
3. [Curve V1 — The Hybrid](#3-curve-v1--the-hybrid)
4. [The K₀ Function — The "Balance Meter"](#4-the-k-function--the-balance-meter)
5. [Curve V2 — Adding Concentrated Liquidity](#5-curve-v2--adding-concentrated-liquidity)
6. [Side-by-Side Comparison](#6-side-by-side-comparison)
7. [Glossary / Cheat Sheet](#7-glossary--cheat-sheet)
8. [Further Reading](#8-further-reading)

---

## 1. Background

An **Automated Market Maker (AMM)** is just a math formula that decides the price when you trade tokens. Instead of buyers and sellers in an order book, an AMM keeps an **invariant** — a value that must stay constant before and after each trade.

- Pool holds `x` units of token A and `y` units of token B.
- Some function `f(x, y) = D` must hold true.
- Different `f`'s give different *curve shapes*, and shape = behavior.

---

## 2. The Two Building Blocks

### 2a. Constant Sum (a straight line)

$$x + y = D$$

- **Great** price: 1 unit in → (almost exactly) 1 unit out. Zero slippage.
- **Bad** safety: someone can drain *all* of `x` or *all* of `y`. The pool can go empty.

### 2b. Constant Product (a hyperbola) — Uniswap V2 style

$$x \cdot y = k$$

- **Great** safety: price approaches infinity as `x → 0`, so the pool can *never* be drained.
- **Bad** price: even small trades move the price a lot — painful for stablecoins.

### Visual: the two curves on the same axes

```
 y ^
   |\
   | \ ← constant sum  (x + y = D)
   |  \        straight line, but pool can drain
   |   \
   |    \
   | .   \
   |  \.  \
   |   \__\
   |       \___
   |           \_________  ← constant product (x · y = k)
   |                     \____        safe but high-slip
   +---------------------------→ x
```

<!-- SVG: both curves overlaid -->
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 340 240" width="460">
  <line x1="40" y1="200" x2="240" y2="200" stroke="#444" stroke-width="1"/>
  <line x1="40" y1="200" x2="40" y2="20" stroke="#444" stroke-width="1"/>
  <text x="245" y="204" font-size="11" fill="#444">x</text>
  <text x="34" y="15" font-size="11" fill="#444">y</text>
  <text x="20" y="25" font-size="10" fill="#888">D</text>
  <text x="230" y="215" font-size="10" fill="#888">D</text>
  <!-- constant sum: dashed blue line from (40,20) to (220,200) -->
  <line x1="40" y1="20" x2="220" y2="200" stroke="#2563eb" stroke-width="2" stroke-dasharray="5,3"/>
  <!-- constant product: red polyline -->
  <polyline points="85,20 94,50 103,71.5 112,87.5 121,99.9 130,110 139,118.1 148,125 157,130.8 166,135.7 175,140 184,143.7 193,147.1 202,150 211,152.6 220,155" stroke="#dc2626" stroke-width="2" fill="none"/>
  <!-- legend -->
  <g transform="translate(250, 40)">
    <line x1="0" y1="5" x2="22" y2="5" stroke="#2563eb" stroke-width="2" stroke-dasharray="5,3"/>
    <text x="27" y="9" font-size="11" fill="#2563eb">x + y = D</text>
    <line x1="0" y1="28" x2="22" y2="28" stroke="#dc2626" stroke-width="2"/>
    <text x="27" y="32" font-size="11" fill="#dc2626">x · y = k</text>
  </g>
</svg>

> **Curve's big idea:** combine both — behave like the straight line when the pool is balanced, and like the hyperbola when it's getting drained.

---

## 3. Curve V1 — The Hybrid

Curve V1 builds up the equation in four steps. Follow along — each step adds one piece.

### Step 1 — Start with constant sum
$$\sum_{i=1}^{N} x_i = D$$

All token balances add up to `D`. `N` is the number of tokens in the pool (e.g. 2 for USDC/USDT, 3 for 3pool).

### Step 2 — Add constant product
$$\sum_{i=1}^{N} x_i \;+\; \prod_{i=1}^{N} x_i \;=\; D + \left(\frac{D}{N}\right)^N$$

Both sides still hold when the pool is perfectly balanced (every $x_i = D/N$).

### Step 3 — Scale units so the two sides match
Multiply the sum side by $D^{N-1}$ so its units match the product side ($D^N$):
$$D^{N-1}\sum_{i=1}^{N} x_i \;+\; \prod_{i=1}^{N} x_i \;=\; D^N + \left(\frac{D}{N}\right)^N$$

### Step 4 — Add the two tuning parameters: **A** and **K₀**
$$A \cdot K_0 \cdot D^{N-1} \sum_{i=1}^{N} x_i \;+\; \prod_{i=1}^{N} x_i \;=\; A \cdot K_0 \cdot D^N + \left(\frac{D}{N}\right)^N$$

- **A** = *amplification coefficient*. Just a plain number. Higher A → flatter middle → better prices.
- **K₀** = a function that measures how "balanced" the pool is (explained next).

---

## 4. The K₀ Function — The "Balance Meter"

$$K_0 \;=\; \frac{\prod_{i=1}^{N} x_i}{(D/N)^N}$$

K₀ is a single number between 0 and 1 that tells the equation how balanced the pool is:

| Pool state | K₀ value | Curve behavior |
|---|---|---|
| Perfectly balanced (all $x_i = D/N$) | **K₀ = 1** | Constant-sum term is fully on → flat, low-slippage |
| Somewhat imbalanced | 0 < K₀ < 1 | Blended shape |
| Extremely imbalanced (one token nearly 0) | **K₀ → 0** | Constant-sum term vanishes → pure constant product (drain-proof) |

### Visual: K₀ as a "balance meter"

<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 360 110" width="460">
  <!-- meter track -->
  <rect x="40" y="40" width="280" height="20" fill="#e5e7eb" stroke="#444"/>
  <!-- gradient fill representing K0 -->
  <defs>
    <linearGradient id="k0grad" x1="0%" x2="100%">
      <stop offset="0%" stop-color="#dc2626"/>
      <stop offset="50%" stop-color="#16a34a"/>
      <stop offset="100%" stop-color="#dc2626"/>
    </linearGradient>
  </defs>
  <rect x="40" y="40" width="280" height="20" fill="url(#k0grad)" opacity="0.3"/>
  <!-- markers -->
  <line x1="40" y1="35" x2="40" y2="65" stroke="#111" stroke-width="1.5"/>
  <line x1="180" y1="35" x2="180" y2="65" stroke="#111" stroke-width="1.5"/>
  <line x1="320" y1="35" x2="320" y2="65" stroke="#111" stroke-width="1.5"/>
  <!-- labels below -->
  <text x="40" y="80" font-size="11" text-anchor="middle" fill="#dc2626">K₀ = 0</text>
  <text x="40" y="94" font-size="10" text-anchor="middle" fill="#666">(drained)</text>
  <text x="180" y="80" font-size="11" text-anchor="middle" fill="#16a34a">K₀ = 1</text>
  <text x="180" y="94" font-size="10" text-anchor="middle" fill="#666">(perfectly balanced)</text>
  <text x="320" y="80" font-size="11" text-anchor="middle" fill="#dc2626">K₀ = 0</text>
  <text x="320" y="94" font-size="10" text-anchor="middle" fill="#666">(drained)</text>
  <!-- title -->
  <text x="180" y="25" font-size="12" text-anchor="middle" fill="#111" font-weight="bold">The K₀ Balance Meter</text>
</svg>

### Why this is clever

Look at the Curve V1 equation again:

$$\underbrace{A K_0 D^{N-1} \sum x_i}_{\text{constant-sum part}} + \underbrace{\prod x_i}_{\text{constant-product part}} = \text{const.}$$

- When K₀ = 1 → constant-sum part is **strong** → curve is flat (good prices).
- When K₀ = 0 → constant-sum part **vanishes** → curve becomes pure constant product (safe from drain).

So **K₀ automatically decides which regime the AMM is in**, with no manual switching.

### Visual: shape of Curve V1 vs its two ingredients

<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 360 240" width="500">
  <line x1="40" y1="200" x2="240" y2="200" stroke="#444" stroke-width="1"/>
  <line x1="40" y1="200" x2="40" y2="20" stroke="#444" stroke-width="1"/>
  <text x="245" y="204" font-size="11" fill="#444">x</text>
  <text x="34" y="15" font-size="11" fill="#444">y</text>
  <!-- constant sum dashed -->
  <line x1="40" y1="20" x2="220" y2="200" stroke="#2563eb" stroke-width="1.5" stroke-dasharray="4,3" opacity="0.7"/>
  <!-- constant product -->
  <polyline points="85,20 94,50 103,71.5 112,87.5 121,99.9 130,110 139,118.1 148,125 157,130.8 166,135.7 175,140 184,143.7 193,147.1 202,150 211,152.6 220,155" stroke="#dc2626" stroke-width="1.5" fill="none" stroke-dasharray="4,3" opacity="0.7"/>
  <!-- Curve V1 — smooth stableswap-looking path -->
  <path d="M 55,25 C 90,55 125,115 130,110 C 135,105 170,165 205,195" stroke="#16a34a" stroke-width="2.8" fill="none"/>
  <!-- annotation for flat middle -->
  <circle cx="130" cy="110" r="3" fill="#16a34a"/>
  <text x="135" y="108" font-size="10" fill="#16a34a">balanced → follows the line</text>
  <!-- annotation for bent ends -->
  <text x="55" y="18" font-size="10" fill="#16a34a">edges → follow the hyperbola</text>
  <!-- legend -->
  <g transform="translate(250, 40)">
    <line x1="0" y1="5" x2="22" y2="5" stroke="#2563eb" stroke-width="1.5" stroke-dasharray="4,3"/>
    <text x="27" y="9" font-size="11" fill="#2563eb">x+y=D</text>
    <line x1="0" y1="28" x2="22" y2="28" stroke="#dc2626" stroke-width="1.5" stroke-dasharray="4,3"/>
    <text x="27" y="32" font-size="11" fill="#dc2626">x·y=k</text>
    <line x1="0" y1="51" x2="22" y2="51" stroke="#16a34a" stroke-width="2.8"/>
    <text x="27" y="55" font-size="11" fill="#16a34a">Curve V1</text>
  </g>
</svg>

The green curve (Curve V1) **hugs the blue straight line** in the middle (low slippage) but **bends toward the red hyperbola** at the edges (drain protection).

---

## 5. Curve V2 — Adding Concentrated Liquidity

Curve V1 is amazing for stablecoin pairs like USDC/USDT where the fair price is ~1:1. But what about **volatile** pairs like BTC/USDC, where the fair price is 60,000:1? Curve V1's "flat zone" is always around 1:1, so it doesn't help.

### The only change in V2: replace K₀ with a sharper K

$$\boxed{\; K \;=\; K_0 \cdot \left(\frac{\gamma}{\gamma + 1 - K_0}\right)^2 \;}$$

- **γ (gamma)** is a small positive number, typically $10^{-4}$ to $10^{-5}$ in real pools.
- Otherwise the equation is identical — just swap every K₀ for K:

$$A \cdot K \cdot D^{N-1}\sum_{i=1}^{N} x_i + \prod_{i=1}^{N} x_i = A \cdot K \cdot D^N + \left(\frac{D}{N}\right)^N$$

### Why γ creates concentration

Look at the multiplier $\left(\frac{\gamma}{\gamma + 1 - K_0}\right)^2$:

- If K₀ = 1 (balanced): multiplier = $(\gamma / \gamma)^2 = 1$. So K = K₀. Same as V1 — flat and low-slip.
- If K₀ drops just a little (say 0.99) and γ is tiny (0.0001): denominator jumps from γ to ~0.01, multiplier becomes $(0.0001/0.01)^2 = 0.0001$. K collapses toward 0 **almost instantly**.

In other words: **V1 eases out of the flat zone gently; V2 snaps out of it fast.** Outside the flat zone, V2 is basically a pure constant-product curve (like Uniswap).

That snap = *concentrated liquidity*. All the juicy low-slippage liquidity lives in a narrow band around the current price; outside, it behaves like plain old Uniswap.

### Visual: V1 vs V2 shape

<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 360 240" width="500">
  <line x1="40" y1="200" x2="240" y2="200" stroke="#444" stroke-width="1"/>
  <line x1="40" y1="200" x2="40" y2="20" stroke="#444" stroke-width="1"/>
  <text x="245" y="204" font-size="11" fill="#444">x</text>
  <text x="34" y="15" font-size="11" fill="#444">y</text>
  <!-- V1: broader flat zone -->
  <path d="M 55,25 C 90,55 125,115 130,110 C 135,105 170,165 205,195" stroke="#16a34a" stroke-width="2.5" fill="none"/>
  <!-- V2: narrower flat zone, more hyperbola-like overall -->
  <path d="M 72,22 C 118,80 128,108 130,110 C 132,112 142,140 188,198" stroke="#9333ea" stroke-width="2.5" fill="none"/>
  <!-- mark center -->
  <circle cx="130" cy="110" r="3" fill="#111"/>
  <text x="135" y="108" font-size="10" fill="#111">balance point</text>
  <!-- annotation for V2 -->
  <text x="62" y="18" font-size="10" fill="#9333ea">V2: sharp bend → concentrated</text>
  <text x="62" y="212" font-size="10" fill="#16a34a">V1: gentle bend</text>
  <!-- legend -->
  <g transform="translate(250, 40)">
    <line x1="0" y1="5" x2="22" y2="5" stroke="#16a34a" stroke-width="2.5"/>
    <text x="27" y="9" font-size="11" fill="#16a34a">Curve V1</text>
    <line x1="0" y1="28" x2="22" y2="28" stroke="#9333ea" stroke-width="2.5"/>
    <text x="27" y="32" font-size="11" fill="#9333ea">Curve V2 (small γ)</text>
  </g>
</svg>

> **Rule of thumb:** γ → large → curve ≈ V1 (wide flat zone). γ → tiny → curve ≈ constant product outside a razor-thin flat zone around the peg.

---

## 6. Side-by-Side Comparison

| Feature | Constant Product (Uniswap V2) | Curve V1 | Curve V2 |
|---|---|---|---|
| Formula idea | `x · y = k` | Hybrid (A, K₀) | Hybrid (A, K, γ) |
| Best for | Volatile pairs (ETH/USDC) | Stable pairs (USDC/USDT) | Volatile pairs with known fair price (BTC/USDC) |
| Slippage near balance | High | Very low | Very low |
| Edge behavior | Hyperbola | Hyperbola | Hyperbola |
| Concentrated liquidity | No | Slight | **Yes** |
| Extra parameter | — | A | A, γ |

---

## 7. Glossary / Cheat Sheet

| Symbol | Name | Meaning |
|---|---|---|
| $x_i$ | Token balance | Amount of token `i` in the pool |
| $N$ | Number of tokens | 2 for a pair, 3 for 3pool, etc. |
| $D$ | Invariant | "Total pool size" — stays constant across trades |
| $A$ | Amplification | Plain number; higher = flatter middle |
| $K_0$ | Balance meter | 1 when balanced, 0 when drained |
| $\gamma$ | Concentration knob | Small positive number (V2 only). Small γ = sharper concentration |
| $K$ | Enhanced K₀ | $K_0 \cdot (\gamma/(\gamma+1-K_0))^2$ |

**One-sentence summary:**
> Curve V1 = constant sum + constant product, auto-blended by K₀. Curve V2 = V1 with K₀ replaced by a sharper K that uses γ to concentrate liquidity around any chosen price.

---

## 8. Further Reading

If you want to go deeper, these are excellent next steps:

- **Curve V1 whitepaper** — "StableSwap" by Michael Egorov (2019).
- **Curve V2 whitepaper** — "Automatic market-making with dynamic peg" by Michael Egorov (2021).
- **Desmos graphing calculator** — paste `A*K*D*(x+y) + x*y = A*K*D^2 + (D/2)^2` and `K = (x*y)/((D/2)^2) * (g/(g + 1 - (x*y)/((D/2)^2)))^2` and play with sliders for `A`, `D`, and `g`. Watching the curve change as you move γ is the fastest way to *feel* concentrated liquidity.
- **Uniswap V3 docs** — another take on concentrated liquidity (range orders instead of a smooth curve). Useful contrast.

> **Tip for understanding:** copy Curve V1's equation into Desmos first, then change `K₀` to `K` and add the γ term step-by-step. Watching each change's effect on the graph makes the math click faster than reading any paper.
