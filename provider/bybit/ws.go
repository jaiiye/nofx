package bybit

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"nofx/logger"
)

// AllLiquidation is one liquidation print, normalized to the side that was
// liquidated.
//
// Bybit's S field is the side of the position, not the aggressor: "Buy" means a
// long was liquidated, "Sell" means a short was liquidated. Binance uses the
// opposite convention, so the mapping is done here, once.
type AllLiquidation struct {
	Symbol      string
	Side        string // "long" or "short" — the liquidated side
	Price       float64
	Qty         float64
	NotionalUSD float64
	TimeMs      int64
}

const (
	liquidationTopic = "allLiquidation."
	subscribeChunk   = 10
	wsPingInterval   = 20 * time.Second
	wsSyncInterval   = 30 * time.Second
	wsMaxBackoff     = 30 * time.Second
)

type subscribeRequest struct {
	Op   string   `json:"op"`
	Args []string `json:"args"`
}

// StreamAllLiquidation consumes the liquidation stream for the symbols returned
// by symbols until ctx ends. The getter is consulted every 30s so the symbol set
// can follow the analysis cycle without reconnecting; a healthy connection
// resets the reconnect backoff even while the market is quiet.
func (c *Client) StreamAllLiquidation(ctx context.Context, symbols func() []string, handler func(AllLiquidation)) error {
	if handler == nil {
		return nil
	}

	backoff := time.Second
	lastErr := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		connected, err := c.streamLiquidationsOnce(ctx, symbols, handler)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if connected {
			backoff = time.Second
		}
		if err != nil {
			message := err.Error()
			if message != lastErr {
				logger.Warnf("⚠️ Bybit liquidation stream disconnected: %v (retrying in %s)", err, backoff)
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

func (c *Client) streamLiquidationsOnce(ctx context.Context, symbols func() []string, handler func(AllLiquidation)) (bool, error) {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, c.wsURL, nil)
	if err != nil {
		return false, err
	}
	defer conn.Close()

	done := make(chan struct{})
	defer close(done)
	conn.SetReadLimit(1 << 20)

	var writeMu sync.Mutex
	writeJSON := func(payload any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return conn.WriteJSON(payload)
	}

	subscribed := map[string]bool{}
	syncSubscriptions := func() error {
		next := make(map[string]bool)
		if symbols != nil {
			for _, symbol := range symbols() {
				if symbol = strings.ToUpper(strings.TrimSpace(symbol)); symbol != "" {
					next[symbol] = true
				}
			}
		}

		var add, remove []string
		for symbol := range next {
			if !subscribed[symbol] {
				add = append(add, liquidationTopic+symbol)
			}
		}
		for symbol := range subscribed {
			if !next[symbol] {
				remove = append(remove, liquidationTopic+symbol)
			}
		}

		if err := sendChunked(writeJSON, "subscribe", add); err != nil {
			return err
		}
		if err := sendChunked(writeJSON, "unsubscribe", remove); err != nil {
			return err
		}
		subscribed = next
		return nil
	}

	if err := syncSubscriptions(); err != nil {
		return false, err
	}

	writerErr := make(chan error, 1)
	go func() {
		ping := time.NewTicker(wsPingInterval)
		defer ping.Stop()
		sync := time.NewTicker(wsSyncInterval)
		defer sync.Stop()

		for {
			select {
			case <-ctx.Done():
				conn.Close()
				return
			case <-done:
				return
			case <-ping.C:
				if err := writeJSON(map[string]string{"op": "ping"}); err != nil {
					writerErr <- err
					conn.Close()
					return
				}
			case <-sync.C:
				if err := syncSubscriptions(); err != nil {
					writerErr <- err
					conn.Close()
					return
				}
			}
		}
	}()

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			select {
			case writerErrValue := <-writerErr:
				return true, writerErrValue
			default:
				return true, err
			}
		}
		for _, liquidation := range parseAllLiquidation(message) {
			handler(liquidation)
		}
	}
}

func sendChunked(writeJSON func(any) error, op string, topics []string) error {
	for start := 0; start < len(topics); start += subscribeChunk {
		end := start + subscribeChunk
		if end > len(topics) {
			end = len(topics)
		}
		if err := writeJSON(subscribeRequest{Op: op, Args: topics[start:end]}); err != nil {
			return err
		}
	}
	return nil
}

// parseAllLiquidation decodes one stream message. Control frames (pong,
// subscribe acks) and malformed payloads yield no prints, so a protocol change
// shows up as a stale feed rather than fabricated data.
func parseAllLiquidation(message []byte) []AllLiquidation {
	var raw struct {
		Topic string `json:"topic"`
		Type  string `json:"type"`
		Ts    int64  `json:"ts"`
		Data  []struct {
			Time   int64  `json:"T"`
			Symbol string `json:"s"`
			Side   string `json:"S"`
			Qty    string `json:"v"`
			Price  string `json:"p"`
		} `json:"data"`
	}
	if err := json.Unmarshal(message, &raw); err != nil {
		return nil
	}
	if !strings.HasPrefix(raw.Topic, liquidationTopic) || len(raw.Data) == 0 {
		return nil
	}

	prints := make([]AllLiquidation, 0, len(raw.Data))
	for _, item := range raw.Data {
		symbol := strings.ToUpper(strings.TrimSpace(item.Symbol))
		if symbol == "" {
			symbol = strings.ToUpper(strings.TrimPrefix(raw.Topic, liquidationTopic))
		}
		side := liquidatedSide(item.Side)
		price := parseFloat(item.Price)
		qty := parseFloat(item.Qty)
		timeMs := item.Time
		if timeMs <= 0 {
			timeMs = raw.Ts
		}
		if symbol == "" || side == "" || price <= 0 || qty <= 0 || timeMs <= 0 {
			continue
		}
		prints = append(prints, AllLiquidation{
			Symbol:      symbol,
			Side:        side,
			Price:       price,
			Qty:         qty,
			NotionalUSD: price * qty,
			TimeMs:      timeMs,
		})
	}
	return prints
}

// liquidatedSide maps Bybit's position side to the side that was closed.
func liquidatedSide(positionSide string) string {
	switch strings.ToLower(strings.TrimSpace(positionSide)) {
	case "buy":
		return "long"
	case "sell":
		return "short"
	default:
		return ""
	}
}
