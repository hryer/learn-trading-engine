// Sample: why we use shopspring/decimal instead of float64.
//
// Run:   go run ./decimal
//
// Reference: ../../05-decimal-arithmetic.md
package main

import (
	"encoding/json"
	"fmt"

	"github.com/shopspring/decimal"
)

func main() {
	demoFloatBroken()
	demoDecimalCorrect()
	demoJSONRoundTrip()
	demoCrossingComparison()
}

// 1. Floats lose precision on simple decimal arithmetic.
func demoFloatBroken() {
	fmt.Println("=== float64 (broken) ===")
	a, b := 0.1, 0.2
	sum := a + b
	fmt.Printf("0.1 + 0.2 = %.20f  (want exactly 0.3)\n", sum)
	fmt.Printf("0.1 + 0.2 == 0.3? %v\n\n", sum == 0.3)
}

// 2. Decimal is exact.
func demoDecimalCorrect() {
	fmt.Println("=== decimal (correct) ===")
	a := decimal.RequireFromString("0.1")
	b := decimal.RequireFromString("0.2")
	sum := a.Add(b)
	expected := decimal.RequireFromString("0.3")
	fmt.Printf("0.1 + 0.2 = %s  (exact)\n", sum)
	fmt.Printf("equal to 0.3? %v\n\n", sum.Equal(expected))
}

// 3. JSON in/out preserves precision when fields are decimal.Decimal.
//
// shopspring/decimal marshals to a JSON *string* by default. That's the
// trick — JSON's number type is float64-shaped, so a number literal would
// be parsed back as float64 (precision lost). A string round-trips exactly.
func demoJSONRoundTrip() {
	fmt.Println("=== JSON round-trip ===")
	type Order struct {
		Price    decimal.Decimal `json:"price"`
		Quantity decimal.Decimal `json:"quantity"`
	}

	original := Order{
		Price:    decimal.RequireFromString("500000000.123456789"),
		Quantity: decimal.RequireFromString("0.00000001"), // 1 satoshi
	}

	encoded, _ := json.Marshal(original)
	fmt.Printf("encoded: %s\n", encoded)

	var decoded Order
	_ = json.Unmarshal(encoded, &decoded)
	fmt.Printf("decoded price:    %s\n", decoded.Price)
	fmt.Printf("decoded quantity: %s\n", decoded.Quantity)
	fmt.Printf("equal to original? %v / %v\n\n",
		decoded.Price.Equal(original.Price),
		decoded.Quantity.Equal(original.Quantity))
}

// 4. The "does this order cross the book?" comparison.
//
// This is the operation called millions of times per second in a real engine.
// Use the Decimal methods, not <= / >= (those don't compile on Decimal anyway).
func demoCrossingComparison() {
	fmt.Println("=== crossing comparison ===")
	takerBuyPrice := decimal.RequireFromString("500050000")
	bestAskPrice := decimal.RequireFromString("500050000")

	// Buy crosses when taker.price >= ask price.
	crosses := takerBuyPrice.GreaterThanOrEqual(bestAskPrice)
	fmt.Printf("buy %s vs ask %s -> crosses? %v (expect true)\n",
		takerBuyPrice, bestAskPrice, crosses)

	bestAskPrice = decimal.RequireFromString("500050001")
	crosses = takerBuyPrice.GreaterThanOrEqual(bestAskPrice)
	fmt.Printf("buy %s vs ask %s -> crosses? %v (expect false)\n",
		takerBuyPrice, bestAskPrice, crosses)
}
