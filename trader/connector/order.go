package connector

// OrderResult is the unified result of an order submission or status query,
// carrying the fee so callers don't have to dig it out of an untyped map.
//
// Field conventions every adapter must follow:
//   - OrderID: exchange order id as a string ("0" means unknown/untracked).
//   - Status: NEW / FILLED / PARTIALLY_FILLED / CANCELED / EXPIRED, or
//     "NO_POSITION" for a close that found nothing to close (a no-op, not a
//     fill). Adapters may instead return an error for that case — callers must
//     accept either.
//   - Fee: accumulated fee as a positive COST. Some venues report negative
//     fees for outflow — negate before returning, never pass the rate through.
//   - Errors are returned as (nil, err); a zero-value result must never be
//     used to signal "I don't know".
type OrderResult struct {
	OrderID      string
	Symbol       string
	Status       string
	AvgFillPrice float64
	FilledQty    float64
	Fee          float64
	FeeAsset     string
	IsMaker      bool
}
