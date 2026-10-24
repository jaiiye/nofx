package market

import (
	"sort"
	"strings"
	"time"
)

// Cross-venue positioning.
//
// Venues are kept separate on purpose. The same position can be opened on one
// venue and hedged on another, so summing open interest across venues
// double-counts and must never be presented as the market's real exposure. The
// total exists for display; the per-venue rows and the funding spread are what a
// decision should use.

const (
	// fundingAssumedIntervalH is the settlement interval assumed when a venue
	// does not report one. It is flagged on the reading so the assumption stays
	// visible instead of being silently baked into the normalized rate.
	fundingAssumedIntervalH = 8.0
	// crowdingFundingThreshold1hPct is the OI-weighted hourly funding above
	// which the book reads as one-sided. 0.005%/h is 0.04%/8h — elevated, but
	// well below the extremes that mark a squeeze.
	crowdingFundingThreshold1hPct = 0.005
)

// VenueQuote is one venue's raw reading for one market, before normalization.
type VenueQuote struct {
	Symbol           string
	Venue            string
	VenueSymbol      string
	MarkPrice        float64
	OIBase           float64
	OIUSD            float64
	FundingRate      float64 // fraction per settlement interval, signed
	FundingIntervalH float64 // 0 when the venue did not report one
	PremiumPct       float64
	TimestampMs      int64
	SourceOK         bool
}

// VenueMetrics is one venue's normalized reading.
type VenueMetrics struct {
	Venue            string  `json:"venue"`
	VenueSymbol      string  `json:"venueSymbol"`
	MarkPrice        float64 `json:"markPrice"`
	OIBase           float64 `json:"oiBase"`
	OIUSD            float64 `json:"oiUsd"`
	Funding1hPct     float64 `json:"funding1hPct"`
	FundingIntervalH float64 `json:"fundingIntervalH"`
	FundingAssumed   bool    `json:"fundingAssumed"`
	PremiumPct       float64 `json:"premiumPct"`
	AgeMs            int64   `json:"ageMs"`
}

// CrossVenue is the per-symbol cross-venue view.
type CrossVenue struct {
	Symbol             string             `json:"symbol"`
	Venues             []VenueMetrics     `json:"venues"`
	OITotalUSD         float64            `json:"oiTotalUsd"`
	OISharePct         map[string]float64 `json:"oiSharePct"`
	FundingAvg1hPct    float64            `json:"fundingAvg1hPct"`
	FundingSpread1hPct float64            `json:"fundingSpread1hPct"`
	FundingSpreadPair  string             `json:"fundingSpreadPair"`
	FundingSpreadOK    bool               `json:"fundingSpreadOk"`
	CrowdedSide        string             `json:"crowdedSide"`
	ComputedAtMs       int64              `json:"computedAtMs"`
}

// NormalizeFunding converts a per-settlement rate into percent per hour, so
// venues with different intervals (HL hourly, Binance 8h, Bybit 8h/4h/1h) can be
// compared. The assumed flag reports whether the interval had to be defaulted.
func NormalizeFunding(rate float64, intervalH float64) (pctPerHour float64, assumed bool) {
	if intervalH <= 0 {
		intervalH = fundingAssumedIntervalH
		assumed = true
	}
	return rate / intervalH * 100, assumed
}

// BuildCrossVenue normalizes the quotes and derives the cross-venue view. It
// returns nil when fewer than two venues have a usable reading: a single venue
// is not a cross-venue signal, and the venue-native positioning reading already
// covers that case.
func BuildCrossVenue(symbol string, quotes []VenueQuote, nowMs int64) *CrossVenue {
	if len(quotes) == 0 {
		return nil
	}
	if nowMs <= 0 {
		nowMs = time.Now().UnixMilli()
	}

	byVenue := make(map[string]VenueQuote, len(quotes))
	for _, quote := range quotes {
		if !quote.SourceOK || quote.MarkPrice <= 0 {
			continue
		}
		venue := strings.TrimSpace(quote.Venue)
		if venue == "" {
			continue
		}
		existing, ok := byVenue[venue]
		if !ok || quote.TimestampMs > existing.TimestampMs {
			byVenue[venue] = quote
		}
	}
	if len(byVenue) < 2 {
		return nil
	}

	venues := make([]VenueMetrics, 0, len(byVenue))
	for venue, quote := range byVenue {
		funding1h, assumed := NormalizeFunding(quote.FundingRate, quote.FundingIntervalH)
		interval := quote.FundingIntervalH
		if interval <= 0 {
			interval = fundingAssumedIntervalH
		}
		venues = append(venues, VenueMetrics{
			Venue:            venue,
			VenueSymbol:      quote.VenueSymbol,
			MarkPrice:        quote.MarkPrice,
			OIBase:           quote.OIBase,
			OIUSD:            quote.OIUSD,
			Funding1hPct:     funding1h,
			FundingIntervalH: interval,
			FundingAssumed:   assumed,
			PremiumPct:       quote.PremiumPct,
			AgeMs:            maxInt64(0, nowMs-quote.TimestampMs),
		})
	}
	sort.Slice(venues, func(i, j int) bool { return venueOrder(venues[i].Venue) < venueOrder(venues[j].Venue) })

	totalOI := 0.0
	for _, venue := range venues {
		if venue.OIUSD > 0 {
			totalOI += venue.OIUSD
		}
	}
	shares := make(map[string]float64, len(venues))
	if totalOI > 0 {
		for _, venue := range venues {
			shares[venue.Venue] = venue.OIUSD / totalOI * 100
		}
	}

	// OI-weighted average funding: the crowded side is a property of the book,
	// not of one venue, so each venue contributes in proportion to its exposure.
	weightedFunding, weight := 0.0, 0.0
	for _, venue := range venues {
		w := venue.OIUSD
		if w <= 0 {
			w = 1
		}
		weightedFunding += venue.Funding1hPct * w
		weight += w
	}
	avgFunding := 0.0
	if weight > 0 {
		avgFunding = weightedFunding / weight
	}

	crowded := ""
	switch {
	case avgFunding >= crowdingFundingThreshold1hPct:
		crowded = "long"
	case avgFunding <= -crowdingFundingThreshold1hPct:
		crowded = "short"
	}

	spread, pair, spreadOK := fundingSpread(venues)

	return &CrossVenue{
		Symbol:             symbol,
		Venues:             venues,
		OITotalUSD:         totalOI,
		OISharePct:         shares,
		FundingAvg1hPct:    avgFunding,
		FundingSpread1hPct: spread,
		FundingSpreadPair:  pair,
		FundingSpreadOK:    spreadOK,
		CrowdedSide:        crowded,
		ComputedAtMs:       nowMs,
	}
}

// fundingSpread prefers the HL/Binance pair because they settle on very
// different schedules, which makes the gap itself informative. With any other
// pair it reports the widest gap and names it.
func fundingSpread(venues []VenueMetrics) (spread float64, pair string, ok bool) {
	byVenue := make(map[string]VenueMetrics, len(venues))
	for _, venue := range venues {
		byVenue[venue.Venue] = venue
	}

	if hl, hasHL := byVenue["hyperliquid"]; hasHL {
		if bn, hasBN := byVenue["binance"]; hasBN {
			return hl.Funding1hPct - bn.Funding1hPct, "hyperliquid/binance", true
		}
	}

	var high, low VenueMetrics
	for i, venue := range venues {
		if i == 0 {
			high, low = venue, venue
			continue
		}
		if venue.Funding1hPct > high.Funding1hPct {
			high = venue
		}
		if venue.Funding1hPct < low.Funding1hPct {
			low = venue
		}
	}
	if high.Venue == low.Venue {
		return 0, "", false
	}
	return high.Funding1hPct - low.Funding1hPct, high.Venue + "/" + low.Venue, true
}

// venueOrder fixes the display order so the prompt reads the same way every
// cycle, regardless of map iteration order.
func venueOrder(venue string) int {
	switch venue {
	case "binance":
		return 0
	case "bybit":
		return 1
	case "hyperliquid":
		return 2
	default:
		return 3
	}
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
