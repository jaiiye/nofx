// Package nofxhl is the self-hosted data source for nofx: it replaces
// the paid nofxos.ai / claw402 / vergex subscriptions with data from
// the nofx-hl-screener data plane (Hyperliquid public API + local
// CVD/liq collection) and the live Hyperliquid info API.
//
// Files follow the provider/nofxos layout — one file per data source:
//
//	client.go   connection management + shared helpers
//	flow.go     NetFlow ranking (cvd_bars → nofxos.NetFlowRankingData)
//	ranking.go  OI + Price rankings (HL metaAndAssetCtxs, oi_snapshots)
//	pool.go     candidate coins (candidate_active) + liq heatmap
//
// All ranking structs are the nofxos.* types verbatim, so the kernel
// prompt pipeline needs zero changes: only the Fetch* methods branch
// on this client being configured.
//
// This package reads tables written by an external process (the
// nofx-hl-screener "serve" mode). Mirror notice: the internal/hlscreener
// packages are a copy of the nofx-hl-screener repo — that repo is the
// authoritative source; keep changes in sync.
package nofxhl
