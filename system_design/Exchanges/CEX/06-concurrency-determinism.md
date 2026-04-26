# 06 — Concurrency & Determinism

**Status: [CHALLENGE] — required**

Two non-functional requirements from the PDF, joined here because they're the same problem from different angles: who's allowed to mutate engine state, and in what order.

## The two requirements

> *"Concurrent-safe. The API accepts concurrent requests; the engine must not corrupt state."*
> *"Deterministic. Given the same order sequence, the trade sequence must be identical."*

The first says: don't crash, don't race. The second says: same input → same output, every time.

Both fall out cleanly from one design decision: **single writer**.

## The single-writer principle

The engine is a state machine. Only one goroutine should mutate it at a time. Reads can be concurrent if you're careful, but writes are serialized.

Three ways to enforce single-writer in Go, in increasing order of complexity:

### Option A — `sync.Mutex` around the engine (recommended)

```go
type Engine struct {
    mu sync.Mutex
    // ... state ...
}

func (e *Engine) Place(cmd PlaceOrderCommand) (Order, []Trade, error) {
    e.mu.Lock()
    defer e.mu.Unlock()
    return e.placeLocked(cmd)
}

func (e *Engine) Cancel(id OrderID) error {
    e.mu.Lock()
    defer e.mu.Unlock()
    return e.cancelLocked(id)
}

func (e *Engine) Snapshot(depth int) BookSnapshot {
    e.mu.Lock()
    defer e.mu.Unlock()
    return e.snapshotLocked(depth)
}
```

**Pros:**
- ~5 lines of plumbing.
- Works with HTTP handlers spinning up goroutines per request.
- Easy to reason about: lock held → I'm the only writer.

**Cons:**
- Reads block writes and vice versa.
- For >100k req/sec, this is a bottleneck. (Spoiler: you won't hit that in this challenge.)

### Option B — `sync.RWMutex`

Same shape, but reads can be concurrent.

```go
func (e *Engine) Snapshot(depth int) BookSnapshot {
    e.mu.RLock()
    defer e.mu.RUnlock()
    return e.snapshotLocked(depth)
}
```

**Pros:** Snapshot reads don't block each other.

**Cons:** Marginal benefit for an engine where every Place is a write. Adds a "is this an RLock or Lock?" question to every method.

For this challenge: **`sync.Mutex` is fine.** Don't reach for RWMutex without measuring.

### Option C — Command channel + single goroutine (LMAX-style)

The engine runs in its own goroutine, consuming commands from a channel.

```go
type cmd struct {
    op       string  // "place", "cancel", "snapshot"
    payload  any
    response chan any
}

type Engine struct {
    cmds chan cmd
    // state, never accessed outside the goroutine
}

func (e *Engine) Run(ctx context.Context) {
    for {
        select {
        case <-ctx.Done():
            return
        case c := <-e.cmds:
            // dispatch, mutate state, send response
        }
    }
}

// HTTP handler:
func (h *Handler) Place(w http.ResponseWriter, r *http.Request) {
    resp := make(chan any, 1)
    h.engine.cmds <- cmd{op: "place", payload: req, response: resp}
    result := <-resp
    // ... write result
}
```

**Pros:**
- No mutex overhead. Cleanly separates "the engine" from "anything that calls it."
- Easy to add features like a sequence number per command (great for determinism + persistence later).
- This is the LMAX Disruptor model — see [09](09-next-steps-architecture.md).

**Cons:**
- More code (channel plumbing, response delivery, shutdown semantics).
- Channels in Go have their own overhead — for low contention, mutexes win.
- Harder to test in isolation.

### What to pick for the challenge

**`sync.Mutex` (Option A).** Reasons to give in the interview:

1. Fits the 5–8h budget.
2. The PDF says: *"sync.Mutex beats a hand-rolled lock-free queue unless you can explain why."*
3. The matching loop is microsecond-scale; lock contention isn't the bottleneck on this scope.
4. If we needed to scale to multi-symbol (out of scope), a command channel per symbol would be the natural next step.

## Where determinism comes from

Determinism = the trade output sequence is fully determined by the input command sequence.

The four classic non-determinism sources in Go, and how to eliminate them:

### 1. Goroutine scheduling

If two goroutines mutate engine state, the OS scheduler decides which goes first → output depends on scheduler.

**Fix:** single writer (the mutex above).

### 2. Map iteration order

```go
for k, v := range myMap { ... }   // order is randomized per Go runtime
```

If you snapshot the book by iterating `map[Price]*Level`, the order is non-deterministic.

**Fix:** never iterate a map where order matters. Use the price tree (file 02).

You can use maps as indexes (`map[OrderID]*handle`), because lookup by key doesn't depend on iteration order.

### 3. Time

```go
time.Now()   // wall-clock, depends on when you ran
```

If timestamps embedded in trades come from `time.Now()` outside the engine, replays from the same input produce different outputs.

**Fix:** the engine takes a `clock` interface, default to `time.Now` for production, inject a fake clock for tests.

```go
type Clock interface {
    Now() time.Time
}

type Engine struct {
    clock Clock
    // ...
}

func (e *Engine) now() time.Time { return e.clock.Now() }
```

For determinism in *production*, you want timestamps generated *inside the engine*, not at the API edge — even if it's the same `time.Now()` call. Reason: the API edge runs in user goroutines, racy ordering.

### 4. Random IDs

```go
id := uuid.New()   // includes a random component
```

Two replays produce different UUIDs → different trade IDs.

**Fix:** monotonic counter inside the engine.

```go
type Engine struct {
    nextOrderID uint64
    nextTradeID uint64
}

func (e *Engine) newOrderID() OrderID {
    e.nextOrderID++
    return OrderID(fmt.Sprintf("o-%d", e.nextOrderID))
}
```

Counters reset to 0 on engine restart. For the in-memory challenge, that's fine. For persistence (next step), the WAL records the last issued ID.

## Cleanly testing determinism

Write this test first:

```go
func TestDeterminism(t *testing.T) {
    cmds := []PlaceOrderCommand{
        {UserID: "u1", Side: buy, Type: limit, Price: dec("100"), Quantity: dec("1")},
        {UserID: "u2", Side: sell, Type: limit, Price: dec("101"), Quantity: dec("0.5")},
        {UserID: "u3", Side: buy, Type: market, Quantity: dec("0.3")},
        // ... a dozen more
    }

    runs := make([][]Trade, 10)
    for i := 0; i < 10; i++ {
        e := NewEngine(fakeClock{startAt: time.Unix(0, 0)})
        var trades []Trade
        for _, c := range cmds {
            _, ts, _ := e.Place(c)
            trades = append(trades, ts...)
        }
        runs[i] = trades
    }

    for i := 1; i < len(runs); i++ {
        if !reflect.DeepEqual(runs[0], runs[i]) {
            t.Fatalf("run 0 vs run %d differ", i)
        }
    }
}
```

If this passes 10× in a row across goroutines, your determinism story is real.

## Concurrency stress test

```go
func TestConcurrentSafe(t *testing.T) {
    e := NewEngine(realClock{})
    var wg sync.WaitGroup
    for i := 0; i < 100; i++ {
        wg.Add(1)
        go func(i int) {
            defer wg.Done()
            for j := 0; j < 100; j++ {
                e.Place(randomCmd(i, j))
            }
        }(i)
    }
    wg.Wait()

    // Invariants that must hold regardless of interleaving:
    snap := e.Snapshot(1000)
    if len(snap.Bids) > 0 && len(snap.Asks) > 0 {
        if snap.Bids[0].Price.GreaterThanOrEqual(snap.Asks[0].Price) {
            t.Fatal("book crossed")
        }
    }
    // sum(remaining) accounting check, etc.
}
```

Run with `go test -race ./...`. Race detector should report nothing.

## What can be parallelized (next steps)

Out of scope for the challenge but useful interview ammunition:

| Component | Parallelizable? |
|---|---|
| Matching one symbol | No (single writer per symbol) |
| Matching different symbols | Yes (one engine instance per symbol) |
| Read snapshots | Yes (RWMutex or copy-out under write lock) |
| HTTP request parsing | Yes (handler runs per goroutine, then enters engine) |
| JSON serialization of response | Yes (after engine returns) |
| Trade history streaming | Yes (separate goroutine reads from a ring buffer) |
| WAL writes | Sometimes (sequenced writer, parallelizable per shard) |

Modern CEXes scale by shard-per-symbol, not by parallelizing a single symbol's matching. Hyperliquid runs each market's matching engine on a single core; throughput comes from pinning many markets across many cores and from extreme micro-optimization of the single-thread path.

## Pitfalls specific to Go

1. **`map` as price level storage** — see above. Just don't.
2. **Goroutine leaks via response channels** — if you go with the channel model, ensure response channels are buffered or selected with a context.
3. **Defer in hot paths** — `defer e.mu.Unlock()` adds ~50 ns per call. Negligible at this scope, but don't defer in the inner match loop.
4. **`fmt.Sprintf` in IDs** — also slow. For the challenge, fine. For an HFT path, pre-allocate IDs as int64.
5. **Logging inside the locked region** — log writes can block. Build the log payload, exit the lock, then log.

## What to say in the interview

> "Single writer via `sync.Mutex` around the engine. HTTP handlers acquire, call, release; reads acquire the same lock and copy out a snapshot — no live references escape. Determinism comes from generating IDs and timestamps inside the locked region, never iterating maps where order matters, and using a `Clock` interface so tests can pin time. I have a determinism test that replays the same command sequence 10 times and `reflect.DeepEqual`s the trade output. The race detector is clean."

That covers concurrent-safe + deterministic in two sentences. Memorize it.
