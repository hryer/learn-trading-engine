# TradFi HFT Architecture

Two views of the same system:

- **Diagram A** — HFT architecture as ByteMonk's video presents it. Interview-level / conceptual. Source: [Inside a Real High-Frequency Trading System](https://www.youtube.com/watch?v=iwRaNYa8yTw) and the backing [ByteMonk blog post](https://blog.bytemonk.io/p/inside-high-frequency-trading).
- **Diagram B** — Closer to how real HFT firms (Jump, Citadel Securities, Jane Street, Hudson River, Tower, XTX, Optiver, IMC) actually run it. Simplified, but with the components the video hides made explicit.

---

## Diagram A — ByteMonk video (conceptual)

```
                  +------------------------------+
                  |           Exchange           |
                  |      (feeds + orders)        |
                  +--------------+---------------+
                                 |
                                 | multicast UDP
                                 v
              +-------------------------------------+
              | [1] Market Data Ingestion           |
              |     kernel-bypass NIC               |
              |     (Solarflare / Xilinx)           |
              +-------------------+-----------------+
                                  v
              +-------------------------------------+
              | [2] Feed Handler                    |
              |     parse millions of msg/sec       |
              +-------------------+-----------------+
                                  v
              +-------------------------------------+
              | [3] In-Memory Order Book            |
              |     RAM-resident, per-symbol        |
              +-------------------+-----------------+
                                  v
              +-------------------------------------+
              | [4] Event-Driven Pipeline           |
              |     low-latency bus                 |
              +-------------------+-----------------+
                                  |
                   +--------------+--------------+
                   v                             v
        +----------------------+      +----------------------+
        | [5] Strategy Engine  |      | [6] FPGA Accelerator |
        |     CPU, lookup tbls |      |     Verilog          |
        |     10-50us t-to-t   |      |     sub-us t-to-t    |
        +----------+-----------+      +----------+-----------+
                   |                             |
                   +--------------+--------------+
                                  v
              +-------------------------------------+
              | [7] Risk Management Engine          |
              |     pre-trade checks, us            |
              +-------------------+-----------------+
                                  v
              +-------------------------------------+
              | [8] Smart Order Router              |
              |     venue + fee optimization        |
              +-------------------+-----------------+
                                  v
              +-------------------------------------+
              | [9] Execution / OMS                 |
              |     send orders to exchange         |
              +-------------------+-----------------+
                                  v
                            [ Exchange ]


  ===== cross-cutting ============================================
  +-------------------------------------------------------+
  | [10] Monitoring & Observability                       |
  |      ns-precision clocks, p99 latency, jitter, HA     |
  +-------------------------------------------------------+
```

### Components

1. **Market Data Ingestion** — kernel-bypass NIC (Solarflare `ef_vi`, Xilinx), direct exchange feeds, bypass the Linux TCP/IP stack.
2. **Feed Handler** — parse binary exchange protocol into order-book events at millions of msg/sec; must stay perfect under burst load.
3. **In-Memory Order Book** — RAM-resident bid/ask ladder per symbol; microsecond updates; derived stats like VWAP computed inline.
4. **Event-Driven Pipeline** — low-latency internal bus delivering book updates to downstream strategies.
5. **Strategy / Decision Engine** — CPU, lookup-table-driven (no runtime math); ~10–50µs tick-to-trade.
6. **FPGA Accelerator** — Verilog / HDL pipeline; sub-µs tick-to-trade for latency-critical signals.
7. **Risk Management Engine** — pre-trade checks in microseconds: position limits, order size, notional, correlation.
8. **Smart Order Router** — chooses venue based on liquidity, fees, historical fill rate, adverse-selection probability.
9. **Execution / OMS** — sends validated orders to exchange; tracks acks / fills.
10. **Monitoring & Observability** — nanosecond clocks, p99 latency dashboards, worst-case jitter, automated failover.

---

## Diagram B — Production-realistic (simplified)

```
            +--------+   +--------+   +--------+
            | NYSE   |   | NASDAQ |   | CME    |   ... N venues
            +---+----+   +----+---+   +---+----+
                |             |            |
                +------+------+------------+
                       | multicast UDP
                       v
          +--------------------------------------------+
          | Kernel-bypass NIC + NIC hardware timestamp |
          | (ef_vi / DPDK, SmartNIC protocol offload)  |
          +--+-----------+-----------+----------+------+
             |           |           |          |
             v           v           v          v
          +------+    +------+    +------+   +------+
          | FH   |    | FH   |    | FH   |   | FH   |     per-venue
          | prim |    |backup|    | prim |   | ...  |     feed handlers
          +--+---+    +--+---+    +--+---+   +--+---+     (primary + backup)
             |           |           |          |
             +-----------+-----+-----+----------+
                               v
                 +-----------------------------+
                 | Normalizer                  |
                 | unified internal msg format |
                 +--------------+--------------+
                                v
         +-----------------------------------------------+
         | Order Book  (primary, pinned core, IRQ off)   |
         |        |                                      |
         |        +---> Order Book (hot standby replica) |
         +----------------------+------------------------+
                                v
                 +-----------------------------+
                 | Strategy Orchestrator       |
                 | capital alloc, symbol map,  |
                 | per-strategy on/off & limits|
                 +--+------+-------+------+----+
                    |      |       |      |
                    v      v       v      v
                +------+ +------+ +------+ +------+
                | MM   | |StatA | |ETF   | | Lat  |   many strategies
                |      | |      | |Arb   | | Arb  |   running in parallel
                +--+---+ +--+---+ +--+---+ +--+---+
                   |        |        |        |
                   |   +----v----------------+ |
                   |   | FPGA sidecar        | |   selective: only
                   |   | hot-path strategies |<+   latency-critical
                   |   +---------+-----------+     strategies use it
                   |             |
                   +------+------+
                          v
            +--------------------------------+
            | HW pre-trade kill switch       |    regulatory (post-Knight);
            | fat-finger, notional, throttle |    cannot be bypassed
            +--------------+-----------------+
                           v
            +--------------------------------+
            | SW risk: position, PnL,        |
            | correlation, exposure          |
            +--------------+-----------------+
                           v
            +--------------------------------+
            |      Smart Order Router        |
            +---+---------+----------+-------+
                |         |          |
                v         v          v
            +-------+ +--------+ +----------+
            | FIX   | | OUCH   | | ITCH /   |   per-venue OMS gateways
            |gateway| |gateway | | proprietary|  each with venue-specific
            +---+---+ +---+----+ +-----+----+   order types, throttles
                |         |            |
                v         v            v
            +-------+ +--------+ +---------+
            | NYSE  | | NASDAQ | | CME ... |
            +-------+ +--------+ +---------+


  ===== backbones (feed every component above) ================================
  +----------------------------+   +---------------------------------+
  | PTP / GPS clock backbone   |   | Tick-capture -> cold storage    |
  | boundary clocks, drift     |   | -> offline backtest / sim,      |
  | alerts, NIC timestamping   |   | fork-replay of yesterday's book |
  +----------------------------+   +---------------------------------+

  +-----------------------------------------------------------------+
  | Monitoring: HW timestamps, clock drift, per-strategy PnL,       |
  | per-venue fill rate, jitter, hot/standby failover health        |
  +-----------------------------------------------------------------+
```

### What's new or different vs. Diagram A

- **Multi-venue fan-in** — real firms trade 5–50 venues; feed handlers exist per venue, with primary+backup pairs for line redundancy.
- **Normalizer** — distinct stage between feed handlers and the book; absorbs per-venue protocol quirks so the book speaks one format.
- **Primary + hot-standby order books** — pinned to dedicated CPU cores with interrupts disabled; replication is synchronous on the local NUMA node.
- **Strategy Orchestrator** — the hidden layer above strategies. Decides which strategy trades which symbol at which time of day, allocates capital, enforces per-strategy kill switches.
- **Strategy pool** — market making, stat arb, index/ETF arb, latency arb, event-driven, etc. Not "one strategy engine"; dozens running in parallel.
- **FPGA sidecar is selective** — parallel to the CPU path, only wired into the hottest strategies. Most strategies never touch it.
- **Two-tier risk** — hardware pre-trade kill switch (regulatory, post-Knight Capital 2012) *before* software risk. The HW switch is physically non-bypassable.
- **Per-venue OMS gateways** — FIX, OUCH, ITCH, proprietary binary; each gateway owns its venue's order types, throttle limits, and self-match prevention rules.
- **PTP / GPS clock backbone** — separate distribution tree feeding every component. Hardware timestamping at the NIC; constant drift monitoring.
- **Tick-capture pipeline** — tees all inbound traffic to storage (petabytes/day) to feed backtesting, simulation, and CI that replays yesterday's market against every strategy change.
- **Monitoring** gains hardware timestamps, clock-drift alarms, per-strategy PnL attribution, per-venue fill-rate dashboards.

---

## Delta — why the video oversimplifies

**FPGA is selective, not blanket.** Only pure market making and latency arb justify full FPGA tick-to-trade. Stat arb / ETF arb / index arb have alpha signals slower than the FPGA win would buy — they stay on CPU with careful cache discipline.

**The strategy box hides the most complex layer.** Production HFT isn't one strategy; it's dozens, each with lifecycle, risk budget, symbol assignment. The orchestration layer that manages them is often bigger than any individual strategy.

**Research / simulation is the bigger system.** Offline infra (tick capture, backtester, microsecond-fidelity sim, CI that replays yesterday's book) often consumes more engineering than the online path. The video shows zero of it.

**The network itself is a moat.** Microwave Chicago↔NJ, hollow-core fiber, cross-connect length measured in meters. Firms like McKay Brothers exist to sell faster point-to-point links. The video treats the network as a given.

**Clock sync deserves its own backbone.** PTP (IEEE 1588), boundary clocks, GPS-disciplined oscillators, NIC hardware timestamping, continuous drift monitoring. One bullet in the video; a dedicated engineering discipline in reality.

**Kernel bypass is table stakes.** Everyone serious has it. The 2026 frontier is pinning the whole trading loop to one CPU core with IRQs off, and pushing protocol parsing into SmartNICs (Solarflare X2, NVIDIA BlueField) before the host ever sees a packet.

---

## Diagram C — CEX / Binance variant (crypto centralized exchanges)

Same conceptual spine as A/B, but the latency floor is **milliseconds, not microseconds**, so several components collapse or disappear. No FPGA, no multicast, no PTP backbone — the network RTT dominates everything.

```
        +--------+   +-------+   +--------+   +----------+
        |Binance |   | OKX   |   | Bybit  |   | Coinbase |  ... N venues
        +---+----+   +---+---+   +----+---+   +-----+----+
            |            |            |             |
            | WS diff +  | (TCP, not multicast)     |
            | snapshot   |                          |
            v            v            v             v
       +----------+  +----------+ +----------+ +----------+
       | WS pool  |  | WS pool  | | WS pool  | | WS pool  |
       | (sharded |  |          | |          | |          |
       |  by sym) |  |          | |          | |          |
       +----+-----+  +----+-----+ +----+-----+ +----+-----+
            |             |            |            |
            +-------------+------+-----+------------+
                                v
                +-------------------------------+
                | Normalizer + gap recovery     |
                | (snapshot+diff merge,         |
                |  reconnect & resync logic)    |
                +---------------+---------------+
                                v
                +-------------------------------+
                | Aggregated order book         |
                | cross-venue, per symbol       |
                +---------------+---------------+
                                v
                +-------------------------------+
                | Strategy pool                 |
                |   - cross-venue arb  <--- #1  |
                |   - funding rate arb (perp)   |
                |   - triangular arb            |
                |   - market making (VIP tier)  |
                +---------------+---------------+
                                v
                +-------------------------------+
                | Rate-limit budgeter           |
                | quota per account / IP /      |
                | FIX session (the real edge)   |
                +---------------+---------------+
                                v
                +-------------------------------+
                | SW risk: position, PnL,       |
                | cross-venue exposure          |
                +---------------+---------------+
                                v
                +-------------------------------+
                | OMS + sub-account fan-out     |
                | one quota bucket per strategy |
                +---+--------+----------+-------+
                    |        |          |
                    v        v          v
                +------+ +------+ +---------+
                | FIX  | | FIX  | | REST/WS |   venue APIs
                | Bn   | | OKX  | | fallback|   (FIX where avail)
                +---+--+ +--+---+ +----+----+
                    |       |         |
                    v       v         v
                +-------+ +-----+ +---------+
                |Binance| | OKX | |Coinbase |
                |  ME   | |  ME | |   ME    |   matching engines
                | (AWS  | |     | |         |   (Binance: AWS Tokyo)
                | Tokyo)| |     | |         |
                +-------+ +-----+ +---------+


  ===== deployment ========================================================
  +----------------------------------------------------------------+
  | EC2 in AWS ap-northeast-1, same AZ as Binance matching engine  |
  | AWS Direct Connect / PrivateLink for dedicated network path    |
  +----------------------------------------------------------------+

  ===== monitoring ========================================================
  +----------------------------------------------------------------+
  | RTT per venue, quota burn rate, matching-engine lag detection, |
  | funding-rate timer, WS reconnect rate, sub-account PnL         |
  +----------------------------------------------------------------+
```

### What's new or different vs. Diagram B

- **No colocation cage, no multicast UDP.** "Colocation" = same AWS region/AZ as the exchange's matching engine. RTT floor ≈ 500µs–1ms.
- **No FPGA.** Network dominates; shaving ns off decode buys nothing.
- **No PTP / GPS clock backbone.** AWS doesn't give you NIC hardware timestamping at that level, and it wouldn't matter anyway at ms-scale latency.
- **Feed handlers are WebSocket clients, not UDP decoders.** Gap recovery via snapshot-then-diff merge; reconnect storms are a real failure mode.
- **Aggregated cross-venue book is the core data structure** — because the dominant alpha is cross-exchange arbitrage, not single-venue microstructure (no Reg NMS equivalent forces venues to honor each other's prices).
- **Rate-limit budgeter replaces the throughput-oriented risk path.** You plan orders against a *quota* (e.g. Binance: 50 spot orders / 10s per IP, higher for VIP), not against per-µs throughput.
- **Sub-account fan-out** at the OMS — each strategy gets its own API key / account for an independent rate-limit bucket. This is how firms multiply their effective quota.
- **VIP tier is the real latency edge.** Tier determines fee rebate, rate-limit size, and access to dedicated FIX endpoints. Negotiated directly with the exchange.
- **Matching engine is non-deterministic under load.** Binance ME has publicly lagged during volatility spikes (2021). Firms must detect ME lag and pause strategies, because fills during a lag window are adversely selected.
- **Strategy mix inverts.** Pure single-venue market making is small; cross-exchange arb, funding-rate arb (perp vs spot), and triangular arb within one venue are the bread and butter.
- **Offline research still exists** (backtesting on historical tick data from Kaiko, Tardis, or captured WS streams) but is cheaper to run — no petabyte multicast capture, just WS recordings.

### Binance-specific practical facts (as of 2026)

- Matching engines run on **AWS ap-northeast-1 (Tokyo)**. Put your EC2 there.
- **FIX API went live in 2024** for spot and margin; gives ~1ms improvement over WebSocket/REST order paths.
- VIP 9 (highest tier) rebates make tight-spread market making viable; below VIP 6, single-venue MM is hard to profit from.
- Documented matching-engine lag during 2021 volatility (LUNA crash, BTC flash moves) — a known risk to plan around, not a one-off.
