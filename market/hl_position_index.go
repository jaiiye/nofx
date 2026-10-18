package market

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"nofx/logger"
	"nofx/provider/hyperliquid"
)

// Holder-position index: the local replacement for a paid "cost/liquidation
// heatmap" feed.
//
// Hyperliquid is the source of truth here, and it is public: the trades stream
// names both sides of every print, and the venue reports every open position of
// any address (entry price, size, leverage and — directly — liquidationPx).
// Combining the two gives an address-level view of where holders entered and
// where they get forced out, which is what the profile in position_bins.go turns
// into a distribution.
//
// Two properties keep this honest:
//   - It is context, never authority. A missing or stale snapshot is reported as
//     absent; nothing here may block a trade.
//   - Coverage is measured, not assumed. Only addresses seen trading recently are
//     known, so every snapshot carries how many positions it actually placed.
const (
	// hlPositionAddressTTL is how long an address stays a candidate after its last
	// print. Long enough to survive a quiet session, short enough that a closed
	// account stops being polled.
	hlPositionAddressTTL = 6 * time.Hour
	// hlPositionAddressCap bounds the candidate set per market.
	hlPositionAddressCap = 6000
	// hlPositionRefreshInterval is how often each market is re-snapped.
	hlPositionRefreshInterval = 5 * time.Minute
	// hlPositionRequestsPerSecond paces venue reads. There is no batch endpoint,
	// so one refresh is one request per address and the rate limit is the real
	// budget.
	hlPositionRequestsPerSecond = 8
	// hlPositionMaxAddressesPerRefresh caps one market's reads. The candidate set
	// is walked with a rotating offset, so a large market is covered over several
	// refreshes instead of being truncated to a fixed subset.
	hlPositionMaxAddressesPerRefresh = 600
	// hlPositionDefaultBandPct and hlPositionDefaultBinCount describe the band
	// around the mark price the buckets cover.
	hlPositionDefaultBandPct  = 15.0
	hlPositionDefaultBinCount = 40
)

// PositionSnapshot is one market's holder distribution at one moment.
type PositionSnapshot struct {
	MarketType string
	Symbol     string
	Timestamp  time.Time
	MarkPrice  float64
	BandPct    float64
	BinCount   int
	Coverage   PositionCoverage
	Bins       []PriceBin
}

// PositionSnapshotSink persists one snapshot. It is a function rather than an
// interface over a store so this package stays free of database types; the
// store-backed writer is wired at boot.
type PositionSnapshotSink func(*PositionSnapshot) error

// positionAddressSet remembers which addresses traded a market recently.
type positionAddressSet struct {
	lastSeen map[string]int64
}

// PositionIndex keeps holder positions for a rotating set of markets and hands
// one snapshot per market per refresh to its sink.
type PositionIndex struct {
	mu        sync.RWMutex
	sink      PositionSnapshotSink
	symbols   map[string]string // our symbol -> venue coin
	addresses map[string]*positionAddressSet
	cursor    map[string]int    // rotation offset per symbol
	lastSnap  map[string]int64  // symbol -> newest stored snapshot (ms)
	lastNote  map[string]string // symbol -> last state note, to log transitions once
	stopped   bool
}

// NewPositionIndex creates an index that reports snapshots to sink. A nil sink
// makes the index inert, which is what tests and deployments without a store get.
func NewPositionIndex(sink PositionSnapshotSink) *PositionIndex {
	return &PositionIndex{
		sink:      sink,
		symbols:   make(map[string]string),
		addresses: make(map[string]*positionAddressSet),
		cursor:    make(map[string]int),
		lastSnap:  make(map[string]int64),
		lastNote:  make(map[string]string),
	}
}

// Start runs the refresh loop until Stop or ctx ends. It never fails the caller:
// without addresses (the trades feed is what discovers them) the loop simply
// reports nothing.
func (idx *PositionIndex) Start(ctx context.Context) {
	if idx == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(hlPositionRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				idx.refreshAll(ctx)
			}
		}
	}()
}

// Stop ends the refresh loop.
func (idx *PositionIndex) Stop() {
	if idx == nil {
		return
	}
	idx.mu.Lock()
	idx.stopped = true
	idx.mu.Unlock()
}

// SetSymbols makes the indexed set match the given symbols (positions plus
// candidates). Symbols already indexed keep their collected addresses.
func (idx *PositionIndex) SetSymbols(symbols []string) {
	if idx == nil {
		return
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()

	desired := make(map[string]string, len(symbols))
	for _, symbol := range symbols {
		coin := venueCoinName(symbol)
		if coin == "" {
			continue
		}
		desired[Normalize(symbol)] = coin
	}
	for symbol := range idx.symbols {
		if _, keep := desired[symbol]; keep {
			continue
		}
		delete(idx.symbols, symbol)
		delete(idx.addresses, symbol)
		delete(idx.cursor, symbol)
	}
	for symbol, coin := range desired {
		idx.symbols[symbol] = coin
	}
}

// CaptureAddresses records the addresses behind one venue print. Called from the
// trades feed, so it stays cheap and never blocks.
func (idx *PositionIndex) CaptureAddresses(symbol string, addresses []string, tsMs int64) {
	if idx == nil || len(addresses) == 0 {
		return
	}
	if tsMs <= 0 {
		tsMs = time.Now().UnixMilli()
	}
	symbol = Normalize(symbol)

	idx.mu.Lock()
	defer idx.mu.Unlock()

	set, ok := idx.addresses[symbol]
	if !ok {
		set = &positionAddressSet{lastSeen: make(map[string]int64)}
		idx.addresses[symbol] = set
	}
	for _, address := range addresses {
		address = strings.ToLower(strings.TrimSpace(address))
		if len(address) < 10 { // a plausible EVM address, not a venue placeholder
			continue
		}
		set.lastSeen[address] = tsMs
	}
	idx.pruneLocked(set, tsMs)
}

// AddressCoverage reports how many candidate addresses a market has and how long
// ago its newest snapshot was stored.
func (idx *PositionIndex) AddressCoverage(symbol string) (candidates int, snapshotAge time.Duration) {
	if idx == nil {
		return 0, 0
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	symbol = Normalize(symbol)
	if set, ok := idx.addresses[symbol]; ok {
		candidates = len(set.lastSeen)
	}
	if ts, ok := idx.lastSnap[symbol]; ok && ts > 0 {
		snapshotAge = time.Duration(time.Now().UnixMilli()-ts) * time.Millisecond
	}
	return candidates, snapshotAge
}

// pruneLocked drops expired addresses and enforces the cap, keeping the most
// recently active ones.
func (idx *PositionIndex) pruneLocked(set *positionAddressSet, nowMs int64) {
	for address, ts := range set.lastSeen {
		if nowMs-ts > hlPositionAddressTTL.Milliseconds() {
			delete(set.lastSeen, address)
		}
	}
	if len(set.lastSeen) <= hlPositionAddressCap {
		return
	}
	type entry struct {
		address string
		seen    int64
	}
	entries := make([]entry, 0, len(set.lastSeen))
	for address, ts := range set.lastSeen {
		entries = append(entries, entry{address: address, seen: ts})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].seen < entries[j].seen })
	for i := 0; i < len(entries)-hlPositionAddressCap; i++ {
		delete(set.lastSeen, entries[i].address)
	}
}

// refreshAll snaps every indexed market, fetching the venue mid prices once per
// dex family rather than once per symbol.
func (idx *PositionIndex) refreshAll(ctx context.Context) {
	idx.mu.RLock()
	if idx.stopped || idx.sink == nil {
		idx.mu.RUnlock()
		return
	}
	symbols := make([]string, 0, len(idx.symbols))
	for symbol := range idx.symbols {
		symbols = append(symbols, symbol)
	}
	idx.mu.RUnlock()

	mids := make(map[string]float64)
	midClient := hyperliquid.NewClient()
	for _, dex := range []string{"", "xyz"} {
		priceMap, err := midClient.GetAllMidsWithDex(ctx, dex)
		if err != nil {
			logger.Infof("⚠️ Position index: mid prices unavailable for dex %q: %v", dex, err)
			continue
		}
		for coin, raw := range priceMap {
			mids[coin] = hyperliquid.StringFloat(raw)
		}
	}

	for _, symbol := range symbols {
		if err := idx.refreshSymbol(ctx, symbol, mids); err != nil {
			idx.note(symbol, err.Error())
		}
	}
}

// refreshSymbol reads one market's holder positions and emits a snapshot.
func (idx *PositionIndex) refreshSymbol(ctx context.Context, symbol string, mids map[string]float64) error {
	idx.mu.RLock()
	if idx.stopped || idx.sink == nil {
		idx.mu.RUnlock()
		return nil
	}
	coin := idx.symbols[symbol]
	sink := idx.sink
	set := idx.addresses[symbol]
	cursor := idx.cursor[symbol]
	idx.mu.RUnlock()

	if coin == "" {
		return nil
	}
	if set == nil || len(set.lastSeen) == 0 {
		idx.note(symbol, "no addresses seen yet (trades feed is what discovers them)")
		return nil
	}

	markPrice := mids[coin]
	if markPrice <= 0 {
		idx.note(symbol, "no mark price for "+coin)
		return nil
	}

	addresses := idx.candidateAddresses(symbol, cursor, hlPositionMaxAddressesPerRefresh)
	idx.mu.Lock()
	idx.cursor[symbol] = cursor + len(addresses)
	idx.mu.Unlock()

	dex := positionDexFor(symbol)
	positions := make([]AddressPosition, 0, len(addresses))
	fetched, failed := 0, 0
	pacer := time.NewTicker(time.Second / time.Duration(hlPositionRequestsPerSecond))
	defer pacer.Stop()

	for _, address := range addresses {
		select {
		case <-ctx.Done():
			pacer.Stop()
			return nil
		case <-pacer.C:
		}

		state, err := hyperliquid.GetClearinghouseState(ctx, address, dex)
		if err != nil {
			failed++
			continue
		}
		fetched++
		positions = append(positions, positionsForCoin(state, address, coin)...)
	}

	bins, coverage := BinPositions(positions, markPrice, hlPositionDefaultBandPct, hlPositionDefaultBinCount)
	coverage.AddressesSeen = len(addresses)
	if len(bins) == 0 || coverage.PositionsPlaced == 0 {
		idx.note(symbol, "no positions placed in the band on the last refresh")
		return nil
	}

	// The profile is derived here so the refresh log says what the distribution
	// actually looks like while collection is being watched; the prompt block and
	// the factor table consume the same function.
	profile := BuildPositionProfile(bins, coverage, markPrice, hlPositionDefaultBandPct)
	if profile == nil {
		idx.note(symbol, "distribution carries no notional")
		return nil
	}

	snapshot := &PositionSnapshot{
		MarketType: positionMarketType(symbol),
		Symbol:     symbol,
		Timestamp:  time.Now().UTC(),
		MarkPrice:  markPrice,
		BandPct:    hlPositionDefaultBandPct,
		BinCount:   hlPositionDefaultBinCount,
		Coverage:   coverage,
		Bins:       bins,
	}
	if err := sink(snapshot); err != nil {
		idx.note(symbol, "store rejected the snapshot: "+err.Error())
		return err
	}

	idx.mu.Lock()
	idx.lastSnap[symbol] = time.Now().UnixMilli()
	idx.lastNote[symbol] = ""
	idx.mu.Unlock()

	logger.Infof("📊 Position index %s: %d addresses read (%d failed), %d positions placed, %d outside, mark %.4f, cost POC %.4f, liq cluster %.4f",
		symbol, fetched, failed, coverage.PositionsPlaced, coverage.PositionsOutside, markPrice, profile.CostPOC, profile.LiqCluster)
	return nil
}

// candidateAddresses returns up to limit addresses for a market, walking the
// candidate set with a rotating offset so every candidate is eventually read.
func (idx *PositionIndex) candidateAddresses(symbol string, cursor, limit int) []string {
	idx.mu.RLock()
	set := idx.addresses[symbol]
	nowMs := time.Now().UnixMilli()
	addresses := make([]string, 0, len(set.lastSeen))
	for address, ts := range set.lastSeen {
		if nowMs-ts <= hlPositionAddressTTL.Milliseconds() {
			addresses = append(addresses, address)
		}
	}
	idx.mu.RUnlock()

	sort.Strings(addresses)
	if len(addresses) == 0 {
		return nil
	}
	if limit <= 0 || limit >= len(addresses) {
		return addresses
	}
	start := cursor % len(addresses)
	out := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, addresses[(start+i)%len(addresses)])
	}
	return out
}

// note logs a market's state once, so a persistent reason (no addresses, no mark
// price, nothing placed) is visible without flooding the log every refresh.
func (idx *PositionIndex) note(symbol, note string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.lastNote[symbol] == note {
		return
	}
	idx.lastNote[symbol] = note
	logger.Infof("ℹ️ Position index %s: %s", symbol, note)
}

// positionsForCoin reduces one account's state to the single market we asked
// about, skipping dust and anything the venue reported without a size.
func positionsForCoin(state *hyperliquid.ClearinghouseState, address, coin string) []AddressPosition {
	if state == nil {
		return nil
	}
	out := make([]AddressPosition, 0, 1)
	for _, asset := range state.AssetPositions {
		position := asset.Position
		if !strings.EqualFold(strings.TrimSpace(position.Coin), coin) {
			continue
		}
		size := hyperliquid.StringFloat(position.Size)
		if size == 0 {
			continue
		}
		side := "long"
		if size < 0 {
			side = "short"
			size = -size
		}
		out = append(out, AddressPosition{
			Address:       address,
			Side:          side,
			Size:          size,
			EntryPrice:    hyperliquid.StringFloat(position.EntryPx),
			LiquidationPx: hyperliquid.StringFloat(position.LiquidationPx),
		})
	}
	return out
}

// positionDexFor returns the HIP-3 dex that lists a symbol, or "" for the core
// perps. Venue-native names carry the dex in the symbol itself ("xyz:NVDA").
func positionDexFor(symbol string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(symbol)), "xyz:") {
		return "xyz"
	}
	return ""
}

// positionMarketType names the market family a symbol belongs to.
func positionMarketType(symbol string) string {
	if positionDexFor(symbol) != "" {
		return "hip3_perp"
	}
	return "core_perp"
}

// Process-level handle, so the trading loop can steer the index and the trades
// feed can hand it addresses without threading either through call sites.
var hlPositionIndex = struct {
	mu    sync.RWMutex
	index *PositionIndex
}{}

// SetHLPositionIndex registers the running index (called from boot).
func SetHLPositionIndex(idx *PositionIndex) {
	hlPositionIndex.mu.Lock()
	hlPositionIndex.index = idx
	hlPositionIndex.mu.Unlock()
}

// UpdatePositionIndexSymbols tells the index which markets to snap. A no-op when
// the index is not running, which leaves the structure block out of the prompt.
func UpdatePositionIndexSymbols(symbols []string) {
	hlPositionIndex.mu.RLock()
	idx := hlPositionIndex.index
	hlPositionIndex.mu.RUnlock()
	if idx == nil {
		return
	}
	idx.SetSymbols(symbols)
}

// captureTradeAddresses hands one print's addresses to the index.
func captureTradeAddresses(symbol string, addresses []string, tsMs int64) {
	hlPositionIndex.mu.RLock()
	idx := hlPositionIndex.index
	hlPositionIndex.mu.RUnlock()
	if idx == nil {
		return
	}
	idx.CaptureAddresses(symbol, addresses, tsMs)
}

// HLPositionIndexCoverage exposes the index health for logging and tests.
func HLPositionIndexCoverage(symbol string) (candidates int, snapshotAge time.Duration) {
	hlPositionIndex.mu.RLock()
	idx := hlPositionIndex.index
	hlPositionIndex.mu.RUnlock()
	if idx == nil {
		return 0, 0
	}
	return idx.AddressCoverage(symbol)
}
