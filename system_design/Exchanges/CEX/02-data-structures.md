# 02 — Data Structures

**Status: [CHALLENGE] — required**

The PDF says: *"Choose the data structure for the book deliberately. You will be asked to defend it."* This file is your defense.

## What the engine must do, fast

Every operation in the matching loop, with required big-O for "good":

| Operation | Frequency | Need |
|---|---|---|
| Get best bid / best ask | Every match step | O(1) or O(log n) |
| Pop FIFO head at a level | Every fill | O(1) |
| Insert resting order at a price | Every limit-rest | O(log n) |
| Cancel by order ID | Cancel API | O(log n) for level lookup + O(1) FIFO removal |
| Drop empty level | After last order leaves | O(log n) |
| Iterate top N levels (snapshot) | Every snapshot read | O(N) — should not require sorting |

The data structures below give you all of these.

## The two-layer structure

```
                   OrderBook (per side)
                          │
         ┌────────────────┴────────────────┐
         │  Sorted map: price → *Level     │   ← outer layer (price tree)
         │  bids: descending key order     │
         │  asks: ascending key order      │
         └────────────────┬────────────────┘
                          │
                  *Level at one price
         ┌────────────────┴────────────────┐
         │  FIFO doubly-linked list        │   ← inner layer (queue at level)
         │  head ↔ order ↔ order ↔ tail    │
         └─────────────────────────────────┘
```

Plus a side-band index:
```
map[OrderID] → *listNode    // for O(1) cancel-by-id
```

That's it. Three structures total per book. Keep it boring.

## Layer 1: the price tree

You need a structure ordered by price that supports:
- `Min()` / `Max()` in O(log n) or O(1)
- `Insert(price)` in O(log n)
- `Delete(price)` in O(log n)
- In-order iteration for the snapshot

### Option A — Red-black tree (recommended)

Balanced BST. `google/btree` or `emirpasic/gods` give you this off-the-shelf.

```go
import "github.com/google/btree"

type priceLevel struct {
    price decimal.Decimal
    queue *list.List
}

// btree wants a Less function
func (l *priceLevel) Less(than btree.Item) bool {
    return l.price.LessThan(than.(*priceLevel).price)
}

bids := btree.New(32) // descending: invert Less for the bid book, OR
asks := btree.New(32) // ascending
```

**Pros:**
- Guaranteed O(log n) all ops, no degeneracy.
- In-order iteration is trivial — perfect for snapshot.
- Battle-tested implementations.

**Cons:**
- Each node is a heap allocation. ~80 ns per insert.
- Cache-unfriendly compared to arrays.

### Option B — Skip list

Probabilistic balanced structure. Used by Hyperliquid (their book is a skip list per side). `huandu/skiplist` for Go.

**Pros:**
- Same O(log n) characteristics.
- Easier to make lock-free than a tree (relevant for [09](09-next-steps-architecture.md)).

**Cons:**
- Slightly more memory per node.
- For a single-writer engine you don't get the lock-free benefit.

### Option C — Sorted slice / heap

A `[]*priceLevel` sorted on insert.

**Pros:**
- Cache-friendly. Fast for very small books.

**Cons:**
- O(n) insert at arbitrary price. Books with thousands of levels die.
- A `container/heap` gives you O(log n) push/pop **but no in-order iteration** — you can't snapshot top 10 without destroying the heap.

### Option D — Array of levels indexed by price tick

Used in some HFT systems where the price grid is small and known (e.g. a futures contract with fixed ticks).

```go
levels [maxTicks]*priceLevel
bestBid int  // index
```

**Pros:**
- O(1) everything. Zero allocation. Cache-perfect.

**Cons:**
- Requires a bounded, known price range. For BTC/IDR with prices like 500_000_000 and tick size 1000, that's 500_000 buckets. Doable but fiddly.
- Doesn't generalize — the moment a new pair has a different tick or wider range, you redesign.

### What to pick for the challenge

**Red-black tree** (`google/btree`). Reasons to give in the interview:

1. **Clear O(log n) all the way down**, no surprise degenerate cases.
2. **In-order iteration matches snapshot need exactly** — heap can't do this.
3. **Battle-tested** — I'm not writing my own balanced tree under a 5–8h budget.
4. **The bottleneck isn't the tree.** A real engine spends most time in fill math, allocation, and serialization. Picking a skip list saves nanoseconds; correctness wins the rubric.

### The Hyperliquid answer (only if asked)

> "Hyperliquid uses skip lists per side. The reason isn't single-thread speed — they're the same. It's that skip lists are *easier to make wait-free* if you ever shard reads across cores. For a single-writer engine like this one, the choice doesn't matter; I picked the boring one."

## Layer 2: the FIFO queue at a level

You need:
- `PushBack(order)` in O(1)
- `PopFront()` in O(1)
- `Remove(node)` in O(1) — for cancels

Go's `container/list` (doubly-linked list) gives you all three.

```go
import "container/list"

level := &priceLevel{
    price: price,
    queue: list.New(),
}

node := level.queue.PushBack(order) // returns *list.Element
// later:
level.queue.Remove(node)            // O(1) — no scan
```

### Why not a slice?

`[]*Order` with `queue[0]` as head:
- `PushBack` is O(1) amortized — fine.
- `PopFront` is O(n) (shifts the slice). You can use a head index, but then memory grows unbounded unless you compact. That's a ring-buffer in disguise.
- `Remove(i)` is O(n). Cancel-by-id needs to scan the level.

A custom ring buffer per level *is* faster than `container/list` (no heap allocation per node, cache-friendly). But:
- More code.
- Cancels still need an `orderID → index` map, and slot reuse is tricky.

For the challenge: `container/list`. Move to ring buffers in profile-driven optimization later.

## Layer 3: the order index

```go
type Engine struct {
    orders map[OrderID]*orderHandle
}

type orderHandle struct {
    order *Order
    node  *list.Element  // pointer back into the level's FIFO
    level *priceLevel    // pointer to the level (for empty-level cleanup)
    side  Side
}
```

Why you need this:
- Cancel-by-id with no scan. Look up the handle, remove from FIFO, drop the level if empty.
- Status updates without traversing the book.

Cost: one extra pointer per resting order. Negligible.

## The stop book

Stop orders need different access patterns from limits:

- They don't have a price on the book (they're invisible).
- They wake on a `last_trade_price` update, not on incoming orders.
- You scan by *trigger price*, not by order price.

### Structure

Two sorted structures:

```go
type StopBook struct {
    buyStops  *btree.BTree // sorted ASC by trigger_price
    sellStops *btree.BTree // sorted DESC by trigger_price
    index     map[OrderID]*stopHandle
}
```

### Why two trees, sorted opposite ways?

When `last_trade_price` updates to `P`:
- Buy stops fire when `trigger ≤ P` → walk `buyStops` from min upward, stop when `trigger > P`.
- Sell stops fire when `trigger ≥ P` → walk `sellStops` from max downward, stop when `trigger < P`.

You only ever scan the prefix that crossed. O(k) for k triggered orders.

### What if multiple stops trigger on the same tick?

Process in **trigger price order, then time order**:
- Buy stops with the lowest trigger fire first (they were the closest to triggering).
- Within a trigger price, FIFO by `created_at`.

This matters for determinism. See [06](06-concurrency-determinism.md).

## Putting it all together

```go
type Engine struct {
    bids       *priceTree                  // *btree.BTree, descending
    asks       *priceTree                  // *btree.BTree, ascending
    stops      *StopBook
    orderIndex map[OrderID]*orderHandle
    lastPrice  decimal.Decimal
    nextOrdID  uint64
    nextTrdID  uint64
    mu         sync.Mutex
}
```

Three structures + an index + a couple of counters. That's the whole state.

## Memory and complexity summary

| Operation | Complexity |
|---|---|
| Place limit (no cross) | O(log L) where L = price levels |
| Place limit (full cross, k fills) | O(k + log L) |
| Place market (k fills) | O(k + log L) |
| Place stop | O(log S) where S = active stops |
| Cancel by ID (resting) | O(log L) |
| Cancel by ID (armed stop) | O(log S) |
| Snapshot top N | O(N) |
| Trigger scan after trade | O(k) where k = triggered orders |

All operations are sub-millisecond for any realistic book. The matching engine itself is not your bottleneck — JSON serialization, HTTP overhead, and decimal arithmetic dominate.

## Common mistakes to avoid

1. **Using a `map[Price]*Level`.** No ordering, can't snapshot, breaks determinism.
2. **Storing aggregated qty per level as a counter.** Looks like an optimization; becomes a sync hazard. Sum the FIFO at snapshot time.
3. **Single tree for both sides.** Different sort orders, different invariants. Two trees.
4. **Iterating the order index for snapshot.** The snapshot must be ordered by price, not by ID.
5. **Forgetting to remove empty levels.** They accumulate. Always check `level.queue.Len() == 0` after a remove and delete from the tree.
