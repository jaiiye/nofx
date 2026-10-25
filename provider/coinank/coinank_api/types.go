package coinank_api

import "errors"

// ErrAPIFailure is returned when the open endpoint answers with success=false.
var ErrAPIFailure = errors.New("coinank open api failure")

// CoinankResponse is the envelope shared by the open endpoints.
type CoinankResponse[T any] struct {
	Success bool   `json:"success"`
	Code    string `json:"code"`
	Data    T      `json:"data"`
}

// KlineResult is one open kline row.
type KlineResult struct {
	StartTime int64   `json:"startTime"`
	EndTime   int64   `json:"endTime"`
	Open      float64 `json:"open"`
	Close     float64 `json:"close"`
	High      float64 `json:"high"`
	Low       float64 `json:"low"`
	Volume    float64 `json:"volume"`
	Quantity  float64 `json:"quantity"`
	Count     float64 `json:"count"`
}
