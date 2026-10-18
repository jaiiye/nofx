package hyperliquid

import (
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/sonirico/go-hyperliquid"

	"nofx/logger"
)

// A builder fee is charged per fill on top of the venue fee, so every basis point
// here is a direct cost on both legs of every round trip. Measured on this
// deployment it was the largest single cost item — larger than the venue's taker
// fee — so the default is NO builder routing. A deployment that wants it opts in:
//
//	NOFX_HL_BUILDER_ADDRESS=0x...   (the account must have approved it on-chain)
//	NOFX_HL_BUILDER_FEE_BPS=5       (basis points; the venue caps it at 100)
//
// With no address, or a fee of 0, orders are sent without a builder and nothing
// extra is charged.
const (
	EnvBuilderAddress = "NOFX_HL_BUILDER_ADDRESS"
	EnvBuilderFeeBps  = "NOFX_HL_BUILDER_FEE_BPS"
	// maxBuilderFeeBps is the venue's cap (0.1%).
	maxBuilderFeeBps = 100
)

type builderSettings struct {
	info   *hyperliquid.BuilderInfo
	feeBps int
}

var (
	builderOnce sync.Once
	builderVal  builderSettings
)

// resolveBuilder reads the routing configuration once per process.
func resolveBuilder() builderSettings {
	builderOnce.Do(func() {
		address := strings.TrimSpace(os.Getenv(EnvBuilderAddress))
		feeBps := 0
		if raw := strings.TrimSpace(os.Getenv(EnvBuilderFeeBps)); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil {
				feeBps = n
			}
		}
		if feeBps > maxBuilderFeeBps {
			feeBps = maxBuilderFeeBps
		}
		if address == "" || feeBps <= 0 {
			builderVal = builderSettings{}
			return
		}
		// BuilderInfo.Fee is expressed in tenths of a basis point.
		builderVal = builderSettings{
			info:   &hyperliquid.BuilderInfo{Builder: address, Fee: feeBps * 10},
			feeBps: feeBps,
		}
	})
	return builderVal
}

// builderInfo returns the builder to attach to an order, or nil for none.
func builderInfo() *hyperliquid.BuilderInfo { return resolveBuilder().info }

// BuilderFeeEnabled reports whether orders carry a builder fee. The start-up
// approval gate consults this: an unapproved builder only matters when one is
// actually configured.
func BuilderFeeEnabled() bool { return resolveBuilder().info != nil }

// BuilderFeeBps reports the configured builder fee in basis points (0 = none).
func BuilderFeeBps() int { return resolveBuilder().feeBps }

// logBuilderRouting makes the cost visible in the logs rather than only in the
// fill history.
func logBuilderRouting() {
	s := resolveBuilder()
	if s.info == nil {
		logger.Infof("💱 Hyperliquid builder routing disabled — no builder fee is charged")
		return
	}
	logger.Infof("💱 Hyperliquid builder routing: %s at %d bp per fill", s.info.Builder, s.feeBps)
}
