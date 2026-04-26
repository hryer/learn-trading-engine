# 08 — HTTP API & Testing

**Status: [CHALLENGE] — required**

The thinnest layer in the system. Its only jobs: parse JSON, call the engine, serialize the result, return HTTP status codes that mean what they say.

## The four endpoints

From the PDF:

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/orders` | Place any order type |
| `DELETE` | `/orders/{id}` | Cancel a resting or armed order |
| `GET` | `/orderbook?depth=10` | Snapshot of top N price levels |
| `GET` | `/trades?limit=50` | Most recent trades |

That's the entire surface. Resist adding more.

## DTOs vs domain types

The engine has its own types (`Order`, `Trade`, `Side`, `OrderType`, `OrderStatus`). The HTTP layer has its own types — request and response shapes that match the JSON contract.

```go
// internal/api/dto.go
type PlaceOrderRequest struct {
    UserID       string          `json:"user_id"`
    Side         string          `json:"side"`           // "buy" or "sell"
    Type         string          `json:"type"`           // "limit" / "market" / "stop" / "stop_limit"
    Price        decimal.Decimal `json:"price,omitempty"`
    TriggerPrice decimal.Decimal `json:"trigger_price,omitempty"`
    Quantity     decimal.Decimal `json:"quantity"`
}

type OrderResponse struct {
    ID                string          `json:"id"`
    UserID            string          `json:"user_id"`
    Side              string          `json:"side"`
    Type              string          `json:"type"`
    Price             decimal.Decimal `json:"price,omitempty"`
    TriggerPrice      decimal.Decimal `json:"trigger_price,omitempty"`
    Quantity          decimal.Decimal `json:"quantity"`
    RemainingQuantity decimal.Decimal `json:"remaining_quantity"`
    Status            string          `json:"status"`
    CreatedAt         time.Time       `json:"created_at"`
}

type PlaceOrderResponse struct {
    Order  OrderResponse    `json:"order"`
    Trades []TradeResponse  `json:"trades"`
}
```

Why separate types from the engine's:
- **Decoupling.** You can change the engine's internal representation without breaking the API contract.
- **Validation.** DTOs are dumb structs; validation happens at the boundary.
- **Side/Type as strings, not iota enums.** JSON doesn't carry Go's iota; strings round-trip cleanly.

## Handler shape

```go
func (h *Handler) PlaceOrder(w http.ResponseWriter, r *http.Request) {
    var req PlaceOrderRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        writeError(w, http.StatusBadRequest, "invalid json")
        return
    }

    cmd, err := req.toCommand()   // validates and converts strings → enums
    if err != nil {
        writeError(w, http.StatusBadRequest, err.Error())
        return
    }

    order, trades, err := h.engine.Place(cmd)
    if err != nil {
        writeError(w, http.StatusUnprocessableEntity, err.Error())
        return
    }

    resp := PlaceOrderResponse{
        Order:  toOrderResponse(order),
        Trades: toTradeResponses(trades),
    }
    writeJSON(w, http.StatusOK, resp)
}
```

The pattern: decode → validate → engine call → encode. Keep handlers under 30 lines.

## HTTP status codes that mean something

| Status | When |
|---|---|
| `200 OK` | Success (place, cancel, snapshot, trades) |
| `201 Created` | Optional for `POST /orders`. `200` is fine too — pick one and stick with it |
| `400 Bad Request` | Malformed JSON, missing fields, invalid enum values |
| `404 Not Found` | Cancel an unknown order ID |
| `409 Conflict` | Cancel an order that's already filled/cancelled (optional refinement) |
| `422 Unprocessable Entity` | Engine business rule violation (e.g. stop trigger already hit) |
| `500 Internal Server Error` | Panic or unexpected engine error — should be rare |

Don't return `200` with `{"error": "..."}` in the body. Use real status codes.

## Routing

Standard library is plenty:

```go
mux := http.NewServeMux()
mux.HandleFunc("POST /orders", h.PlaceOrder)
mux.HandleFunc("DELETE /orders/{id}", h.CancelOrder)
mux.HandleFunc("GET /orderbook", h.GetOrderBook)
mux.HandleFunc("GET /trades", h.GetTrades)

server := &http.Server{Addr: ":8080", Handler: mux}
```

Go 1.22+ has method-aware patterns and path parameters in the standard `http.ServeMux`. No need for `gorilla/mux` or `chi` for four endpoints.

If you're on older Go, or want middleware, `chi` is the boring choice.

## What about middleware?

For this scope: **none required.** The PDF says no auth, no rate limiting.

If you want to add request logging for your own debugging, one tiny middleware:

```go
func logRequest(h http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        start := time.Now()
        h.ServeHTTP(w, r)
        log.Printf("%s %s %v", r.Method, r.URL.Path, time.Since(start))
    })
}
```

Wrap once at the server level. Don't over-engineer.

## File layout

```
cmd/server/main.go                     # wire engine + handlers + server
internal/engine/                       # pure: no http, no json
    order.go book.go stop_book.go engine.go decimal.go
internal/api/
    dto.go        # request/response types
    convert.go    # DTO ↔ engine type converters
    handlers.go   # http.HandlerFunc methods
    errors.go     # writeError, writeJSON helpers
internal/api/integration_test.go       # one end-to-end HTTP test
internal/engine/engine_test.go         # the matching tests
internal/engine/determinism_test.go
```

The hard rule: **`internal/engine/` has no `import "net/http"` or `import "encoding/json"`.** Audit this. It's a scoring signal.

## Testing strategy

The PDF explicitly asks for: **unit tests for matching logic + at least one integration test hitting the HTTP API.**

### Tier 1 — engine unit tests (the most important)

Table-driven, one function per scenario class:

```go
func TestMatchLimitFullFill(t *testing.T) {
    e := newTestEngine(t)
    place(t, e, sell, limit, "100", "1.0", "u1")
    _, trades, _ := e.Place(buyLimit("u2", "100", "1.0"))

    require.Len(t, trades, 1)
    assert.Equal(t, "1.0", trades[0].Quantity.String())
    assert.Equal(t, "100", trades[0].Price.String())
    assert.Equal(t, buy, trades[0].TakerSide)
}
```

Coverage map (write one test per row):

| Scenario | Why |
|---|---|
| Limit fully fills against single maker | Happy path |
| Limit partial-fills, remainder rests | Common path |
| Limit walks two price levels | Outer loop correctness |
| Limit at non-crossing price → rests, no trades | crosses() correctness |
| Market consumes book, partial unfilled remainder rejected | Spec-critical |
| Market on empty side → rejected, 0 trades | Edge |
| Cancel resting → removed from book + index | Cancel happy path |
| Cancel non-existent → ErrNotFound | Cancel sad path |
| Cancel after fully filled → ErrNotFound | Status check |
| Stop order placed, doesn't appear in snapshot | Visibility |
| Stop triggers on a subsequent trade | Trigger logic |
| Stop already triggered at placement → rejected | Spec edge |
| Stop cascade: stop A triggers, its trade triggers stop B | Cascade correctness |
| Stop_limit triggers but limit doesn't cross → rests | Stop_limit specificity |
| Cancel armed stop → not in stop book, no future triggers | Cancel + stops |
| Self-match: cancel newest | SMP policy |
| Determinism: 100-command sequence × 10 runs identical | Non-functional |
| Snapshot returns top N levels in correct order, aggregated | Snapshot correctness |
| Snapshot excludes armed stops | Visibility |

A test per row, ~15 lines each. Total ~300 lines. Do this **first**, before HTTP.

### Tier 2 — concurrency stress test

```go
func TestConcurrentPlace(t *testing.T) {
    e := newTestEngine(t)
    var wg sync.WaitGroup
    for i := 0; i < 50; i++ {
        wg.Add(1)
        go func(i int) {
            defer wg.Done()
            for j := 0; j < 100; j++ {
                _, _, _ = e.Place(randomCmd(i, j))
            }
        }(i)
    }
    wg.Wait()
    // Invariants:
    snap := e.Snapshot(1000)
    if len(snap.Bids) > 0 && len(snap.Asks) > 0 {
        require.True(t, snap.Bids[0].Price.LessThan(snap.Asks[0].Price), "book crossed")
    }
}
```

Run with `go test -race`.

### Tier 3 — HTTP integration test

One end-to-end test that proves the wiring works:

```go
func TestHTTPIntegration(t *testing.T) {
    e := engine.New(engine.RealClock{})
    h := api.NewHandler(e)
    server := httptest.NewServer(h)
    defer server.Close()

    // POST a sell limit
    body := `{"user_id":"u1","side":"sell","type":"limit","price":"100","quantity":"1.0"}`
    resp, _ := http.Post(server.URL+"/orders", "application/json", strings.NewReader(body))
    require.Equal(t, 200, resp.StatusCode)

    // Snapshot — should show the order
    resp, _ = http.Get(server.URL + "/orderbook?depth=10")
    var snap api.SnapshotResponse
    json.NewDecoder(resp.Body).Decode(&snap)
    require.Len(t, snap.Asks, 1)
    require.Equal(t, "100", snap.Asks[0].Price.String())

    // POST a buy market that fills it
    body = `{"user_id":"u2","side":"buy","type":"market","quantity":"1.0"}`
    resp, _ = http.Post(server.URL+"/orders", "application/json", strings.NewReader(body))
    var placed api.PlaceOrderResponse
    json.NewDecoder(resp.Body).Decode(&placed)
    require.Equal(t, "filled", placed.Order.Status)
    require.Len(t, placed.Trades, 1)
}
```

One test like this is enough for the PDF requirement. Don't write 20 HTTP tests — they duplicate the engine tests with more boilerplate.

## Test discipline

- **Use `httptest.NewServer`**, not a manually-bound port. Lets tests run in parallel without port collisions.
- **No real clock** in engine tests. Inject a fake clock that returns `time.Unix(0, n).UTC()` where `n` increments by 1 ns per call. Trade timestamps become `0000-01-01T00:00:00.000000001Z`-ish — predictable and stable.
- **No real RNG.** All IDs come from the engine's monotonic counter (file 06).
- **Snapshot tests** that compare full JSON output get fragile fast. Compare struct fields with assertions, not string-equal on JSON.
- **`go test -race ./...`** in CI. Required to validate the concurrency story.

## What NOT to do

- **Don't write a benchmark test as evidence of "performance."** The PDF doesn't ask for benchmarks; you'd be optimizing the wrong thing.
- **Don't mock the engine in HTTP tests.** The integration test exists precisely to catch wiring bugs.
- **Don't load-test in CI.** Out of scope, brittle, slow.
- **Don't add OpenAPI/Swagger.** Document the four endpoints in the README. Done.

## README content (the PDF wants)

Half a page is enough. Cover, in this order:

1. **Run instructions.** `go run ./cmd/server` and `go test ./...`. Optional `docker-compose up`.
2. **Data structure choices.** btree per side + container/list per level + map index. One paragraph defending.
3. **Concurrency model.** sync.Mutex, single writer, why not lock-free.
4. **Self-match policy.** Cancel-newest, why.
5. **What you'd do next.** WAL/persistence, multi-symbol sharding, WebSocket diff feeds, fee schedules, advanced order types (IOC/FOK/post-only/iceberg), Prometheus metrics. Reference [09](09-next-steps-architecture.md) and [10](10-next-steps-features.md) in this curriculum for the full list.

Don't go over a page. The PDF says "half a page is enough" — that's a hint that long READMEs lose points.

## Common bugs at the HTTP layer

| Bug | Detection |
|---|---|
| Decoding "0.5" as float64 → precision loss | Decimal type with String tag, integration test |
| Returning live engine pointers in snapshots → mutation after lock release | Always copy out under the lock |
| Trailing slash sensitivity (`/orders` vs `/orders/`) | Use ServeMux properly; test both |
| Status enum mismatch between engine and DTO | Single source of truth — convert in one function |
| Forgetting Content-Type: application/json on responses | `writeJSON` helper sets it once |
| Goroutine spawned per request that outlives the response | Don't do this; engine call is synchronous |

## Defending in the interview

> "REST, four endpoints, standard library mux. DTOs are separate from engine types so the API contract isn't coupled to internal representation. Decimals serialize as strings to avoid float precision loss. Engine call is synchronous from the handler — handler decodes, calls Place(), encodes the result. The integration test spins up a real server, runs a place→snapshot→fill scenario end-to-end. The matching tests are table-driven against the engine directly with ~20 scenarios."

That covers API design + testing strategy in 30 seconds.
