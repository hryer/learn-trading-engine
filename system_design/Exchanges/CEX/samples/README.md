# Samples

Runnable Go programs that illustrate each concept from the markdown files. Each `main.go` is self-contained and demonstrative — these are **learning aids, not production code**. They lean on `fmt.Println` to show what's happening internally.

## Run any sample

```bash
cd samples/<name>
go mod tidy   # first time only — pulls shopspring/decimal and google/btree
go run main.go
```

If you don't have a Go module here yet:

```bash
cd samples
go mod init samples
go get github.com/shopspring/decimal
go get github.com/google/btree
```

Then run `go run ./<name>` from inside `samples/`.

## What each sample shows

| Sample | Concept | Reference |
|---|---|---|
| `decimal/` | Why `shopspring/decimal` over float, JSON round-trip safety | [05](../05-decimal-arithmetic.md) |
| `orderbook/` | Two-layer book: btree of price levels, FIFO queue per level, O(1) cancel | [02](../02-data-structures.md) |
| `matching/` | Full match loop with partial fills, walking levels, maker-price trades | [03](../03-matching-engine.md) |
| `stopbook/` | Armed stop orders, trigger scan, cascade processing | [04](../04-stop-orders.md) |
| `engine/` | All of the above wired together — minimal end-to-end engine, no HTTP | [06](../06-concurrency-determinism.md), [07](../07-self-match-prevention.md) |

The full case-study answer adds the HTTP layer ([08](../08-http-api-and-testing.md)) on top of `engine/`. That's left as your exercise — the samples here cover everything below the HTTP boundary.

## How to use these

1. **Read the matching markdown file first.** The samples assume you understand the concepts.
2. **Run the sample.** Watch the printed output match the explanation.
3. **Modify a value, re-run.** Add a new test case, predict the output, verify.
4. **Then write your own.** These are scaffolds, not solutions. Your case-study answer will be more focused (no `fmt.Println`, more tests).
