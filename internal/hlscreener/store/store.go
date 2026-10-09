// Package store wraps Postgres access. Single-file for clarity; split per-table
// repos in larger projects.
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

func New(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 10
	cfg.MinConns = 1
	cfg.MaxConnIdleTime = 5 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, err
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

// ============================================================================
// CVDBars
// ============================================================================

type CVDBar struct {
	Coin   string
	Bucket int64
	Buy    float64
	Sell   float64
	Delta  float64
}

func (s *Store) UpsertCVDBar(ctx context.Context, b CVDBar) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO cvd_bars (coin, bucket, buy, sell, delta)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (coin, bucket) DO UPDATE SET
			buy   = cvd_bars.buy   + EXCLUDED.buy,
			sell  = cvd_bars.sell  + EXCLUDED.sell,
			delta = cvd_bars.delta + EXCLUDED.delta
	`, b.Coin, b.Bucket, b.Buy, b.Sell, b.Delta)
	return err
}

func (s *Store) CVDBaseline(ctx context.Context, coin string) (float64, error) {
	var b float64
	err := s.pool.QueryRow(ctx,
		`SELECT baseline FROM cvd_baselines WHERE coin=$1`, coin).Scan(&b)
	if err != nil {
		return 0, err
	}
	return b, nil
}

func (s *Store) SetCVDBaseline(ctx context.Context, coin string, baseline float64) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO cvd_baselines (coin, baseline, updated_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (coin) DO UPDATE SET baseline = EXCLUDED.baseline, updated_at = EXCLUDED.updated_at
	`, coin, baseline, time.Now().UnixMilli())
	return err
}

// SumCVDBefore returns the sum of all deltas for `coin` with bucket
// strictly less than `cutoffMs`. Used by the CVD recorder to maintain
// a baseline anchor (Superior Terminal's pan-stable trick).
func (s *Store) SumCVDBefore(ctx context.Context, coin string, cutoffMs int64) (float64, error) {
	var sum float64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(delta), 0)
		FROM cvd_bars
		WHERE coin = $1 AND bucket < $2
	`, coin, cutoffMs).Scan(&sum)
	return sum, err
}

// ComputeCurrentCVD walks cvd_bars from `fromMs` forward, returning the
// running cumulative delta. Caller is expected to add a baseline if needed.
//
// Mirrors Superior Terminal's GET /api/orderflow/cvd semantics:
//   - o = cvd at bar open (previous bar's cvd)
//   - h/l = intrabar high/low of the running path
//   - c = cvd at bar close
type CVDPoint struct {
	T     int64   `json:"t"`
	O     float64 `json:"o"`
	H     float64 `json:"h"`
	L     float64 `json:"l"`
	C     float64 `json:"c"`
	Delta float64 `json:"delta"`
	Buy   float64 `json:"buy"`
	Sell  float64 `json:"sell"`
}

func (s *Store) CVDPanel(ctx context.Context, coin string, fromMs, toMs int64) ([]CVDPoint, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT bucket, buy, sell, delta
		FROM cvd_bars
		WHERE coin=$1 AND bucket >= $2 AND bucket < $3
		ORDER BY bucket ASC
	`, coin, fromMs, toMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	baseline, err := s.CVDBaseline(ctx, coin)
	if err != nil {
		// no baseline yet, start at 0
		baseline = 0
	}

	var (
		points []CVDPoint
		cvd    = baseline
	)
	for rows.Next() {
		var (
			bucket int64
			delta  float64 // NUMERIC in PG doesn't fit int64 directly
			buy, sell float64
		)
		if err := rows.Scan(&bucket, &buy, &sell, &delta); err != nil {
			return nil, err
		}
		o := cvd
		cvd += delta
		points = append(points, CVDPoint{
			T:     bucket,
			O:     o,
			H:     cvd, // simplified: bar high/low need sub-bucket walking; we keep flat for now
			L:     cvd,
			C:     cvd,
			Delta: delta,
			Buy:   buy,
			Sell:  sell,
		})
	}
	return points, rows.Err()
}

// ============================================================================
// Liquidation levels
// ============================================================================

type LiqLevel struct {
	Coin         string
	BinStart     float64
	BinEnd       float64
	NotionalUSD  float64
	Positions    int
	FirstSeen    int64
	LastSeen     int64
}

func (s *Store) UpsertLiqLevel(ctx context.Context, l LiqLevel) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO liq_levels (coin, bin_start, bin_end, liquidation_usd, positions_count, first_seen, last_seen)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (coin, bin_start) DO UPDATE SET
			bin_end         = EXCLUDED.bin_end,
			liquidation_usd = EXCLUDED.liquidation_usd,
			positions_count = EXCLUDED.positions_count,
			last_seen       = EXCLUDED.last_seen
	`, l.Coin, l.BinStart, l.BinEnd, l.NotionalUSD, l.Positions, l.FirstSeen, l.LastSeen)
	return err
}

func (s *Store) GetLiqHeatmap(ctx context.Context, coin string) ([]LiqLevel, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT coin, bin_start::float8, bin_end::float8,
		       liquidation_usd::float8, positions_count, first_seen, last_seen
		FROM liq_levels
		WHERE coin=$1
		ORDER BY bin_start ASC
	`, coin)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LiqLevel
	for rows.Next() {
		var l LiqLevel
		if err := rows.Scan(&l.Coin, &l.BinStart, &l.BinEnd, &l.NotionalUSD, &l.Positions, &l.FirstSeen, &l.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ============================================================================
// Candidate pool
// ============================================================================

type Candidate struct {
	Coin        string
	Score       float64
	Reasons     map[string]any
	InPool      bool
	GeneratedAt int64
	ExpiresAt   int64
}

func (s *Store) ReplaceActivePool(ctx context.Context, cands []Candidate) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Drop existing active rows
	if _, err := tx.Exec(ctx, `TRUNCATE candidate_active`); err != nil {
		return err
	}

	for _, c := range cands {
		if !c.InPool {
			continue
		}
		raw, _ := json.Marshal(c.Reasons)
		if _, err := tx.Exec(ctx, `
			INSERT INTO candidate_active (coin, score, reasons, activated_at, expires_at)
			VALUES ($1, $2, $3, $4, $5)
		`, c.Coin, c.Score, raw, c.GeneratedAt, c.ExpiresAt); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) GetActivePool(ctx context.Context) ([]Candidate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT coin, score::float8, reasons, activated_at, expires_at
		FROM candidate_active
		WHERE expires_at > $1
		ORDER BY score DESC
	`, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Candidate
	for rows.Next() {
		var c Candidate
		var raw []byte
		if err := rows.Scan(&c.Coin, &c.Score, &raw, &c.GeneratedAt, &c.ExpiresAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &c.Reasons)
		c.InPool = true
		out = append(out, c)
	}
	return out, rows.Err()
}

// ============================================================================
// OHLC + funding snapshots
// ============================================================================

type OHLC struct {
	Coin string
	T    int64
	O, H, L, C, V float64
}

func (s *Store) UpsertOHLC(ctx context.Context, o OHLC) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO ohlc_1m (coin, t, o, h, l, c, v) VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (coin, t) DO UPDATE SET
			o = EXCLUDED.o, h = EXCLUDED.h, l = EXCLUDED.l, c = EXCLUDED.c, v = EXCLUDED.v
	`, o.Coin, o.T, o.O, o.H, o.L, o.C, o.V)
	return err
}

func (s *Store) UpsertFunding(ctx context.Context, coin string, t int64, rate, markPx float64) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO funding (coin, t, rate, mark_px) VALUES ($1,$2,$3,$4)
		ON CONFLICT (coin, t) DO NOTHING
	`, coin, t, rate, markPx)
	return err
}

func (s *Store) UpsertOI(ctx context.Context, coin string, t int64, oiUSD float64) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO oi_snapshots (coin, t, oi_usd) VALUES ($1,$2,$3)
		ON CONFLICT (coin, t) DO UPDATE SET oi_usd = EXCLUDED.oi_usd
	`, coin, t, oiUSD)
	return err
}

// OISnapshot is a single row from oi_snapshots.
type OISnapshot struct {
	Coin  string
	T     int64
	OIUSD float64
}

// OIHistory returns the most recent `limit` OI snapshots for a
// coin, in ascending time order. Used by the screener's structure
// layer to detect OI trend (rising OI + rising price = strong
// trend; falling OI = weakening).
func (s *Store) OIHistory(ctx context.Context, coin string, limit int) ([]OISnapshot, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT coin, t, oi_usd::float8
		FROM oi_snapshots
		WHERE coin = $1
		ORDER BY t DESC
		LIMIT $2
	`, coin, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OISnapshot
	for rows.Next() {
		var s OISnapshot
		if err := rows.Scan(&s.Coin, &s.T, &s.OIUSD); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Reverse to ascending order (oldest first)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// ============================================================================
// Diagnostics
// ============================================================================

func (s *Store) Ping(ctx context.Context) error {
	row := s.pool.QueryRow(ctx, `SELECT 1`)
	var x int
	return row.Scan(&x)
}

// StatusReport is a freshness snapshot of the main time-series tables.
// MaxT values are epoch ms (0 if the table is empty).
type StatusReport struct {
	CVDBars      int64
	CVDMaxBucket int64
	CoinsInCVD   int64
	LiqLevels    int64
	LiqMaxSeen   int64
	OIRows       int64
	OIMaxT       int64
	ScreenerRows int64
}

// Status queries row counts and latest timestamps. Used by serve mode
// to log liveness and by operators to verify the 24h run is healthy.
func (s *Store) Status(ctx context.Context) (StatusReport, error) {
	var r StatusReport
	q := func(dest *int64, query string) error {
		return s.pool.QueryRow(ctx, query).Scan(dest)
	}
	if err := q(&r.CVDBars, `SELECT COUNT(*) FROM cvd_bars`); err != nil {
		return r, err
	}
	if err := q(&r.CVDMaxBucket, `SELECT COALESCE(MAX(bucket),0) FROM cvd_bars`); err != nil {
		return r, err
	}
	if err := q(&r.CoinsInCVD, `SELECT COUNT(DISTINCT coin) FROM cvd_bars`); err != nil {
		return r, err
	}
	if err := q(&r.LiqLevels, `SELECT COUNT(*) FROM liq_levels`); err != nil {
		return r, err
	}
	if err := q(&r.LiqMaxSeen, `SELECT COALESCE(MAX(last_seen),0) FROM liq_levels`); err != nil {
		return r, err
	}
	if err := q(&r.OIRows, `SELECT COUNT(*) FROM oi_snapshots`); err != nil {
		return r, err
	}
	if err := q(&r.OIMaxT, `SELECT COALESCE(MAX(t),0) FROM oi_snapshots`); err != nil {
		return r, err
	}
	if err := q(&r.ScreenerRows, `SELECT COUNT(*) FROM candidate_active`); err != nil {
		return r, err
	}
	return r, nil
}

func (s *Store) String() string {
	return fmt.Sprintf("pgxpool<%d/%d>", s.pool.Stat().AcquiredConns(), s.pool.Stat().TotalConns())
}

// ============================================================================
// Retention — drop time-series data older than the cutoff.
// ============================================================================

// RetentionReport is the result of a RunRetention pass.
type RetentionReport struct {
	CVDRowsDeleted    int64
	OHLCRowsDeleted   int64
	FundingDeleted    int64
	OIDeleted         int64
	ScreenerRunsKept  int64
	PoolRowsKept      int64
}

const (
	DefaultCVDRetentionMs    = 7 * 24 * 3600 * 1000
	DefaultOHLCRetentionMs   = 30 * 24 * 3600 * 1000
	DefaultFundingRetentionMs = 30 * 24 * 3600 * 1000
	DefaultOIRetentionMs     = 7 * 24 * 3600 * 1000
)

// RunRetention deletes old time-series rows. The 4 windows are
// configurable; defaults match the schema's intended usage.
//
// candidate_active is never deleted (it expires via expires_at).
// screener_runs keeps the last 1000 entries for audit; older is purged.
func (s *Store) RunRetention(ctx context.Context, cvdMs, ohlcMs, fundingMs, oiMs int64) (RetentionReport, error) {
	var r RetentionReport
	now := time.Now().UnixMilli()

	cmd, err := s.pool.Exec(ctx, `DELETE FROM cvd_bars WHERE bucket < $1`, now-cvdMs)
	if err != nil {
		return r, err
	}
	r.CVDRowsDeleted = cmd.RowsAffected()

	cmd, err = s.pool.Exec(ctx, `DELETE FROM ohlc_1m WHERE t < $1`, now-ohlcMs)
	if err != nil {
		return r, err
	}
	r.OHLCRowsDeleted = cmd.RowsAffected()

	cmd, err = s.pool.Exec(ctx, `DELETE FROM funding WHERE t < $1`, now-fundingMs)
	if err != nil {
		return r, err
	}
	r.FundingDeleted = cmd.RowsAffected()

	cmd, err = s.pool.Exec(ctx, `DELETE FROM oi_snapshots WHERE t < $1`, now-oiMs)
	if err != nil {
		return r, err
	}
	r.OIDeleted = cmd.RowsAffected()

	// Keep last 1000 screener_runs
	cmd, err = s.pool.Exec(ctx, `
		DELETE FROM screener_runs
		WHERE id NOT IN (
		  SELECT id FROM screener_runs ORDER BY id DESC LIMIT 1000
		)
	`)
	if err != nil {
		return r, err
	}
	r.ScreenerRunsKept = 1000 - cmd.RowsAffected()

	// Pool history: keep last 30 days
	cmd, err = s.pool.Exec(ctx, `DELETE FROM candidate_pool WHERE generated_at < $1`, now-30*24*3600*1000)
	if err != nil {
		return r, err
	}
	r.PoolRowsKept = -1 // not tracked, deleted count suffices

	// Refresh all CVD baselines after the delete
	_, _ = s.pool.Exec(ctx, `
		UPDATE cvd_baselines b
		SET baseline = COALESCE((
		  SELECT SUM(delta) FROM cvd_bars WHERE coin = b.coin
		    AND bucket < $1
		), 0),
		updated_at = $2
	`, now-cvdMs, now)

	return r, nil
}
