package binance

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"nofx/logger"
)

// ForceOrder is one liquidation print, normalized to the side that was
// liquidated.
//
// Binance reports the aggressor side of the forced order: SELL means a long
// position was sold out, BUY means a short was bought back. That is the
// opposite of Bybit's convention, so the mapping happens here, once, and every
// consumer sees "long"/"short".
type ForceOrder struct {
	Symbol      string
	Side        string // "long" or "short" — the liquidated side
	Price       float64
	Qty         float64
	NotionalUSD float64
	TimeMs      int64
	OrderID     string
}

const (
	forceOrderStream = "/!forceOrder@arr"
	wsMaxBackoff     = 30 * time.Second
)

// StreamForceOrders consumes the all-market liquidation stream until ctx ends.
// It reconnects with exponential backoff; a healthy connection resets the
// backoff even when no liquidation has printed yet (quiet markets are normal).
func (c *Client) StreamForceOrders(ctx context.Context, handler func(ForceOrder)) error {
	if handler == nil {
		return nil
	}

	backoff := time.Second
	lastErr := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		connected, err := c.streamForceOrdersOnce(ctx, handler)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if connected {
			backoff = time.Second
		}
		if err != nil {
			message := err.Error()
			if message != lastErr {
				logger.Warnf("⚠️ Binance liquidation stream disconnected: %v (retrying in %s)", err, backoff)
				lastErr = message
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < wsMaxBackoff {
			backoff *= 2
			if backoff > wsMaxBackoff {
				backoff = wsMaxBackoff
			}
		}
	}
}

func (c *Client) streamForceOrdersOnce(ctx context.Context, handler func(ForceOrder)) (bool, error) {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, c.wsURL+forceOrderStream, nil)
	if err != nil {
		return false, err
	}
	defer conn.Close()

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()

	conn.SetReadLimit(1 << 20)
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			return true, err
		}
		if order, ok := parseForceOrder(message); ok {
			handler(order)
		}
	}
}

// parseForceOrder decodes one stream message. Malformed or non-forceOrder
// messages are dropped rather than counted, so a protocol change shows up as
// silence (which the watcher reports as a stale feed) instead of fake prints.
func parseForceOrder(message []byte) (ForceOrder, bool) {
	var raw struct {
		Event     string `json:"e"`
		EventTime int64  `json:"E"` // absorbed explicitly: Go matches JSON keys case-insensitively, so an undeclared "E" would land on "e"
		Order     struct {
			Symbol    string `json:"s"`
			Side      string `json:"S"`
			Price     string `json:"p"`
			AvgPrice  string `json:"ap"`
			Qty       string `json:"q"`
			FilledQty string `json:"z"`
			Time      int64  `json:"T"`
			OrderID   int64  `json:"i"`
		} `json:"o"`
	}
	if err := json.Unmarshal(message, &raw); err != nil {
		return ForceOrder{}, false
	}
	if !strings.EqualFold(raw.Event, "forceOrder") {
		return ForceOrder{}, false
	}

	symbol := strings.ToUpper(strings.TrimSpace(raw.Order.Symbol))
	side := liquidatedSide(raw.Order.Side)
	if symbol == "" || side == "" || raw.Order.Time <= 0 {
		return ForceOrder{}, false
	}

	price := parseFloat(raw.Order.AvgPrice)
	if price <= 0 {
		price = parseFloat(raw.Order.Price)
	}
	qty := parseFloat(raw.Order.FilledQty)
	if qty <= 0 {
		qty = parseFloat(raw.Order.Qty)
	}
	if price <= 0 || qty <= 0 {
		return ForceOrder{}, false
	}

	return ForceOrder{
		Symbol:      symbol,
		Side:        side,
		Price:       price,
		Qty:         qty,
		NotionalUSD: price * qty,
		TimeMs:      raw.Order.Time,
		OrderID:     formatOrderID(raw.Order.OrderID),
	}, true
}

// liquidatedSide maps Binance's aggressor side to the position side that was
// closed. SELL = long liquidated, BUY = short liquidated.
func liquidatedSide(aggressor string) string {
	switch strings.ToUpper(strings.TrimSpace(aggressor)) {
	case "SELL":
		return "long"
	case "BUY":
		return "short"
	default:
		return ""
	}
}

func formatOrderID(id int64) string {
	if id <= 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}
