# 09 — Next Steps: Architecture

**Status: [NEXT STEP] — DO NOT IMPLEMENT for the challenge.**

This file is interview ammunition. Read it so you can answer "what would you do next with more time?" credibly. **Do not build any of this for the case study** — the PDF explicitly excludes persistence, replication, sharding, and queue infrastructure.

Topics:
1. LMAX Disruptor — single-writer, ring-buffer architecture
2. Persistence — WAL + event sourcing
3. Replication — primary/secondary, deterministic replay
4. Sharding — multi-symbol scale-out
5. Recovery — snapshots + WAL replay

## 1. LMAX Disruptor

The LMAX exchange (London, FX, ~2010) published their architecture and it became the canonical reference for high-throughput single-writer matching engines.

### Core idea

Replace channels (which have queue locks and goroutine wakeups) with a **ring buffer** of pre-allocated slots, accessed by sequence numbers.

```
        ┌───────────────────────────────────────────────┐
        │  Ring buffer of N slots (N is power of 2)     │
        │  ┌────┬────┬────┬────┬────┬────┬────┬────┐    │
        │  │  0 │  1 │  2 │  3 │  4 │  5 │  6 │  7 │    │
        │  └────┴────┴────┴────┴────┴────┴────┴────┘    │
        │     ↑                          ↑              │
        │   reader                    writer            │
        │  (cursor=2)               (cursor=5)          │
        └───────────────────────────────────────────────┘
```

- One **writer** (the matching engine) advances its cursor as it produces events.
- Multiple **readers** (persistence, market data fan-out, replication) follow at their own pace.
- No locks. Coordination via atomic sequence numbers.
- Slots are pre-allocated structs — no GC pressure.

### Why it's fast

- **No allocation** — slots are reused.
- **No locks** — atomic CAS on sequence numbers only.
- **Cache-friendly** — the ring is contiguous memory, slots are accessed in order.
- **Mechanical sympathy** — designed for CPU cache lines; slots padded to 64 bytes to avoid false sharing.

LMAX hit ~6M ops/sec on commodity hardware in 2011. Modern engines (Hyperliquid, Crypto.com, dYdX v3) follow the same pattern.

### Sketch in Go

```go
type Event struct {
    Seq   uint64
    Kind  EventKind   // place, cancel, snapshot
    Data  unsafe.Pointer
    _pad  [40]byte    // pad to 64 bytes (cache line)
}

type Disruptor struct {
    buffer    []Event   // size = N (power of 2)
    mask      uint64    // N - 1, for fast modulo
    writerSeq atomic.Uint64
    readerSeqs []*atomic.Uint64
}

func (d *Disruptor) Publish(kind EventKind, data unsafe.Pointer) {
    seq := d.writerSeq.Add(1)
    slot := &d.buffer[seq & d.mask]
    // wait until slowest reader has passed this slot
    for d.minReaderSeq() < seq - uint64(len(d.buffer)) {
        runtime.Gosched()
    }
    slot.Seq = seq
    slot.Kind = kind
    slot.Data = data
    // (cursor publish happens after slot is fully written)
}
```

### Why you don't build this for the challenge

- ~500 lines of careful concurrency code.
- Without a real load test, you can't prove the perf claim.
- A `sync.Mutex` is 30× faster than the perf budget needs and 10× simpler.

### What to say in the interview

> "If we needed >100k orders/sec on a single symbol, I'd move to a Disruptor-style ring buffer with one writer and multiple readers — persistence, market-data publish, replication consumer. Each reader has its own sequence cursor; the writer advances atomically; no locks on the hot path. That's the LMAX model. For this challenge it would be over-engineering."

## 2. Persistence — WAL + Event Sourcing

The challenge says in-memory only. Production exchanges must survive restarts without losing orders or trades.

### Event sourcing

The engine state at any moment = `replay(initial_state, command_sequence)`. Persist the **commands**, not the state.

```
WAL on disk:
[SEQ=1] PLACE u1 buy limit 100 1.0
[SEQ=2] PLACE u2 sell limit 100 0.5
[SEQ=3] CANCEL o-1
[SEQ=4] PLACE u3 buy market 0.3
...
```

On startup:
1. Read the WAL from beginning.
2. For each command, call `engine.Place()` or `engine.Cancel()`.
3. State is restored exactly because the engine is deterministic (file 06).

### Write-ahead semantics

Two ordering options:

**A. Synchronous write-ahead.** Engine: take command → write to WAL → fsync → apply → respond.

- Strong durability: if the response is sent, the command is persisted.
- Slower: fsync is ~1ms on SSD, dominates latency.
- Mitigated by group commit (batch fsync every N commands or every X ms).

**B. Asynchronous write-behind.** Engine: take command → apply → respond → enqueue WAL write.

- Faster.
- Risk: crash after response, before WAL flush → caller thinks order was placed but it isn't durable.
- Acceptable for some workloads, never for an exchange.

Real exchanges do (A) with group commit, achieving ~10–50k ops/sec on modern NVMe.

### Snapshot + WAL truncation

A pure WAL grows unbounded. Mitigation:

1. Periodically (every N seconds or every K commands) take a snapshot of engine state.
2. Persist snapshot atomically.
3. On startup: load latest snapshot, replay WAL from snapshot's last sequence forward.
4. Truncate WAL up to snapshot point.

Snapshot format: serialized engine state (orders, levels, stop book, lastPrice, counters). Use a stable format (protobuf, msgpack, capn-proto). Avoid Go-specific gob.

### Recovery time

- Pure WAL replay: bounded by `(WAL_size / replay_speed)` — with a deterministic engine, replay can hit millions of ops/sec.
- Snapshot + WAL: bounded by `snapshot_load_time + recent_WAL_replay`. Sub-second for most workloads.

## 3. Replication — Primary/Secondary with Deterministic Replay

For high availability, you need ≥2 instances of the engine in sync.

### How determinism enables replication

Because the engine is deterministic (file 06), if you:
1. Order all commands with a sequence number.
2. Send the sequence to both primary and secondary.
3. Both replay the same sequence.

Then both instances reach the same state. The secondary doesn't need to receive trades or state diffs — it derives them from the same input.

This is **state-machine replication** (Lamport, 1978). Used in Raft-based systems, blockchains (Hyperliquid, dYdX), and most modern exchanges.

### Implementation sketch

```
   ┌──────────┐    1. Order entry
   │ Gateway  │
   └────┬─────┘
        │ ordered command stream
        │ (Kafka / Aeron / custom)
        ├─────────────────┬─────────────────┐
        ▼                 ▼                 ▼
   ┌─────────┐      ┌─────────┐       ┌─────────┐
   │Primary  │      │Replica 1│       │Replica 2│
   │engine   │      │engine   │       │engine   │
   └────┬────┘      └─────────┘       └─────────┘
        │
        ▼  trade events
    ┌────────┐
    │ users  │
    └────────┘
```

The gateway is the single source of truth for *order order* (sic). All engine instances replay the same stream.

Failover: when the primary dies, a replica is promoted. Because it's already at the same sequence number, the gap is one network roundtrip + the in-flight commands.

### What about consensus?

For decentralized exchanges (Hyperliquid, dYdX), the "gateway" is a Byzantine consensus protocol (HotStuff variants). All validators agree on the order, then each replays.

For centralized exchanges, simpler: a single ordering gateway, with internal replication via Raft.

## 4. Sharding — Multi-Symbol Scale-Out

The PDF locks scope to one trading pair. In production, you have hundreds.

### Per-symbol engine

Each trading pair runs an independent engine instance.

```
   ┌──────────┐
   │ Gateway  │
   └────┬─────┘
        │ route by symbol
        ├──────────┬──────────┬──────────┐
        ▼          ▼          ▼          ▼
   BTC/IDR    ETH/IDR    SOL/IDR    AVAX/IDR
   engine     engine     engine     engine
   (core 1)   (core 2)   (core 3)   (core 4)
```

- Each engine is single-writer (its own mutex or its own Disruptor).
- No coordination between engines for matching — orders for different pairs are independent.
- Pin each engine to a CPU core for cache locality.

### What about cross-pair atomic operations?

E.g. "transfer balance from a BTC trade to enable an ETH trade in the same logical operation."

These don't exist for matching — each engine matches its pair. They exist at the **risk/wallet layer**, which sits *above* the engines and gates orders before they enter an engine.

Out of scope for the challenge in *every* dimension. Mention it for context.

### What if one pair is much hotter?

BTC/USDT might do 100x the volume of an obscure altcoin. Two strategies:

- **Heterogeneous hosts**: BTC engine runs on a beefy box, alt engine runs on a smaller one.
- **Replicated hot pair**: the hot pair's engine has read-replicas serving snapshot/trades; only the writer mutates. (Trade-publishing happens via Disruptor reader.)

You don't shard a single pair across cores. The single-writer property is the source of correctness.

## 5. Recovery — Bootstrap from Snapshot + WAL

End-to-end startup sequence in a real exchange:

1. **Load latest snapshot.** O(snapshot_size) read.
2. **Verify snapshot integrity** (checksum).
3. **Open WAL at snapshot.last_sequence + 1.**
4. **Replay WAL** to current end.
5. **Open command stream** for live commands; resume.

During steps 1–4, the engine is in **recovery mode** — accepting commands into a buffer but not processing. After step 4, replay buffered live commands. Then announce ready.

Total boot time for a healthy snapshot + few seconds of WAL: < 1 second. Acceptable for failover.

## 6. What the engine produces (event taxonomy)

In production, every engine action emits an event consumed by downstream systems:

| Event | Consumers |
|---|---|
| `OrderAccepted` | User notifications, audit |
| `OrderRejected` | User notifications, audit |
| `OrderResting` | Market data (book deltas) |
| `OrderCancelled` | Market data (book deltas), user notifications |
| `Trade` | User notifications, fee billing, market data, on-chain settlement (DEX), regulator reporting |
| `BookDelta` (level price + new total qty) | Market data WebSocket subscribers |

Events are appended to the same WAL. The Disruptor pattern is perfect: one writer, many readers, each at its own pace.

## 7. Summary table — what to mention in "next steps"

| Capability | Approximate effort | Why it matters |
|---|---|---|
| WAL persistence | 2–3 days | Survives restart |
| Snapshot + truncation | 1 day | Bounded recovery time |
| Disruptor ring buffer | 3–5 days | 10–100x throughput |
| Per-symbol shard | 1 day per shard | Linear scale across pairs |
| Replication via deterministic replay | 1 week | High availability |
| Market data WebSocket | 2–3 days | Real-time clients |
| Prometheus metrics | 0.5 days | Observability |
| Structured logging | 0.5 days | Debugging |

In the README's "next steps" section, list 4–6 of these in priority order. Don't list all of them — looks like a wishlist.

## Defending the architecture story in the interview

> "The current engine is single-writer behind a mutex. To scale, the next layer is the Disruptor pattern — replace the mutex with a ring buffer, one writer goroutine, multiple consumers. That gets us to ~1M ops/sec single-symbol and naturally adds a WAL by making persistence one of the consumers. For HA, deterministic replay means a secondary just consumes the same command stream — no state diffs needed. For scale across pairs, one engine per symbol pinned to its own core. None of this is in the current code because it's not in scope, but the engine's purity (no I/O, no clock leakage, deterministic) is precisely what makes those steps possible without redesign."

That's a 60-second answer that demonstrates you understand the road from "case study" to "production." It's exactly what the design walkthrough Part 3 is testing.
