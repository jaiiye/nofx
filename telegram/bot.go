package telegram

import (
	"context"
	"strings"
	"sync"
	"time"

	nofxagent "nofx/agent"
	"nofx/config"
	"nofx/logger"
	"nofx/store"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Start initializes and runs the Telegram bot in a blocking supervisor loop.
// Supports hot-reload: when a signal is sent on reloadCh, the bot restarts
// with the latest token (re-read from DB or env). Must be called as a goroutine from main.go.
//
// The conversational brain is the shared NOFXi agent (nofx/agent) — the same
// instance the web UI talks to. This package only owns the transport layer:
// Telegram updates, chat binding/access control, language commands, and the
// placeholder-edit streaming UX.
func Start(cfg *config.Config, st *store.Store, reloadCh <-chan struct{}, nofxAgent *nofxagent.Agent) {
	for {
		token := resolveToken(st)
		if token == "" {
			logger.Info("Telegram bot disabled (no token configured), waiting for reload signal...")
			<-reloadCh
			continue
		}

		stopped := runBot(token, cfg, st, nofxAgent)
		if !stopped {
			return
		}

		select {
		case <-reloadCh:
			logger.Info("Reloading Telegram bot with new token...")
		}
	}
}

// resolveToken returns the bot token from DB (configured via Web UI).
func resolveToken(st *store.Store) string {
	dbCfg, err := st.TelegramConfig().Get()
	if err == nil && dbCfg.BotToken != "" {
		return string(dbCfg.BotToken)
	}
	return ""
}

// chatLanes serializes AI calls per chat (OpenClaw Lane Queue pattern): a
// second message for the same chat waits up to laneWaitTimeout, then gives up.
type chatLanes struct {
	mu    sync.Mutex
	lanes map[int64]chan struct{}
}

const laneWaitTimeout = 60 * time.Second

func (l *chatLanes) run(chatID int64, fn func()) bool {
	l.mu.Lock()
	lane, ok := l.lanes[chatID]
	if !ok {
		lane = make(chan struct{}, 1) // binary semaphore: one message at a time per chat
		l.lanes[chatID] = lane
	}
	l.mu.Unlock()

	select {
	case lane <- struct{}{}:
	case <-time.After(laneWaitTimeout):
		return false
	}
	defer func() { <-lane }()
	fn()
	return true
}

// runBot runs the bot until the updates channel closes (clean stop → true) or a fatal error (false).
func runBot(token string, cfg *config.Config, st *store.Store, nofxAgent *nofxagent.Agent) bool {
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		logger.Errorf("Telegram bot failed to start: %v", err)
		return false
	}
	logger.Infof("Telegram bot @%s started", bot.Self.UserName)

	// Allowed chat ID: read from DB binding (0 = unbound, first /start will bind).
	allowedChatID := int64(0)
	if id, err := st.TelegramConfig().GetBoundChatID(); err == nil && id != 0 {
		allowedChatID = id
	}

	// botUserID is resolved lazily and refreshes when the user registers.
	var botUserID, botUserEmail string
	lanes := &chatLanes{lanes: map[int64]chan struct{}{}}

	resolveBotUser := func() bool {
		users, err := st.User().GetAll()
		if err != nil || len(users) == 0 {
			return false
		}
		u := users[0]
		if u.ID == botUserID {
			return true
		}
		prev := botUserID
		botUserID = u.ID
		botUserEmail = u.Email
		if prev == "" {
			logger.Infof("Bot: resolved user %s (%s)", botUserID, botUserEmail)
		} else {
			logger.Infof("Bot: user changed → %s (%s)", botUserID, botUserEmail)
		}
		return true
	}
	resolveBotUser()

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	// awaitingLang is set only when the user explicitly runs /lang.
	awaitingLang := false

	for update := range updates {
		if update.Message == nil {
			continue
		}
		chatID := update.Message.Chat.ID
		text := strings.TrimSpace(update.Message.Text)

		// ── Language selection (triggered only by /lang) ──────────────────────
		if awaitingLang && chatID == allowedChatID {
			if lang := parseLangChoice(text); lang != "" {
				awaitingLang = false
				st.TelegramConfig().SetLanguage(lang) //nolint:errcheck
				sendMarkdownMsg(bot, chatID, statusMsg(st, botUserID, cfg.APIServerPort, lang))
			} else {
				sendMarkdownMsg(bot, chatID, langMenuMsg())
			}
			continue
		}

		// ── /start ────────────────────────────────────────────────────────────
		if text == "/start" {
			resolveBotUser()
			if botUserID == "" {
				sendMsg(bot, chatID,
					"No account found.\nOpen the web dashboard to register, then send /start.")
				continue
			}
			if allowedChatID == 0 {
				username := update.Message.From.UserName
				if err := st.TelegramConfig().BindUser(chatID, "@"+username); err != nil {
					logger.Errorf("Failed to bind Telegram user: %v", err)
					sendMsg(bot, chatID, "Binding failed. Please try again.")
					continue
				}
				allowedChatID = chatID
				logger.Infof("Telegram bound to @%s (chatID: %d)", username, chatID)
			} else if chatID != allowedChatID {
				sendMsg(bot, chatID, "This bot is already bound to another account.")
				continue
			} else if nofxAgent != nil {
				// Fresh conversation on /start (the shared agent supports /clear).
				_, _ = nofxAgent.HandleMessageForStoreUser(context.Background(), botUserID, chatID, "/clear")
			}
			lang := st.TelegramConfig().GetLanguage()
			sendMarkdownMsg(bot, chatID, statusMsg(st, botUserID, cfg.APIServerPort, lang))
			continue
		}

		// ── /lang ─────────────────────────────────────────────────────────────
		if text == "/lang" {
			awaitingLang = true
			sendMarkdownMsg(bot, chatID, langMenuMsg())
			continue
		}

		// ── /help ─────────────────────────────────────────────────────────────
		if text == "/help" {
			lang := st.TelegramConfig().GetLanguage()
			sendMarkdownMsg(bot, chatID, helpMsg(lang))
			continue
		}

		// ── Access control ────────────────────────────────────────────────────
		if allowedChatID != 0 && chatID != allowedChatID {
			sendMsg(bot, chatID, "Unauthorized.")
			continue
		}
		if allowedChatID == 0 {
			sendMsg(bot, chatID, "Send /start first.")
			continue
		}
		if text == "" {
			continue
		}

		// ── Refresh user before every AI call ────────────────────────────────
		resolveBotUser()
		if botUserID == "" {
			sendMsg(bot, chatID, "No account found. Open the web dashboard to register.")
			continue
		}

		lang := st.TelegramConfig().GetLanguage()

		// ── Guard: show status if no usable AI model is configured ───────────
		if !hasEnabledModel(st, botUserID) {
			sendMarkdownMsg(bot, chatID, statusMsg(st, botUserID, cfg.APIServerPort, lang))
			continue
		}

		// ── AI agent (shared NOFXi agent, Telegram transport) ────────────────
		go func(chatID int64, text, lang string) {
			sent, err := bot.Send(tgbotapi.NewMessage(chatID, "⏳"))
			placeholderID := 0
			if err == nil {
				placeholderID = sent.MessageID
			}

			var (
				mu          sync.Mutex
				accumulated string
				lastEdit    time.Time
			)
			// The agent emits incremental delta chunks (plus status events we
			// do not render in Telegram). Accumulate deltas and throttled-edit
			// the placeholder with the full text so far.
			onEvent := func(event, data string) {
				if event != nofxagent.StreamEventDelta || placeholderID == 0 {
					return
				}
				mu.Lock()
				defer mu.Unlock()
				accumulated += data
				if time.Since(lastEdit) < time.Second {
					return
				}
				lastEdit = time.Now()
				edit := tgbotapi.NewEditMessageText(chatID, placeholderID, accumulated)
				bot.Send(edit) //nolint:errcheck
			}

			var reply string
			ok := lanes.run(chatID, func() {
				// The agent understands a "[lang:xx] " prefix to switch the
				// reply language per conversation.
				msg := text
				if lang == "zh" || lang == "en" {
					msg = "[lang:" + lang + "] " + text
				}
				r, err := nofxAgent.HandleMessageStreamForStoreUser(
					context.Background(), botUserID, chatID, msg, onEvent)
				if err != nil {
					logger.Errorf("Telegram agent error: %v", err)
				}
				if r == "" {
					r = "AI assistant temporarily unavailable. Please try again."
				}
				reply = r
			})
			if !ok {
				reply = "Previous message is still being processed. Please wait a moment and try again. / 上一条消息仍在处理中，请稍等片刻后再试。"
			}

			if placeholderID != 0 {
				edit := tgbotapi.NewEditMessageText(chatID, placeholderID, reply)
				edit.ParseMode = "Markdown"
				if _, err := bot.Send(edit); err != nil {
					edit2 := tgbotapi.NewEditMessageText(chatID, placeholderID, reply)
					bot.Send(edit2) //nolint:errcheck
				}
			} else {
				msg := tgbotapi.NewMessage(chatID, reply)
				msg.ParseMode = "Markdown"
				if _, err := bot.Send(msg); err != nil {
					msg.ParseMode = ""
					bot.Send(msg) //nolint:errcheck
				}
			}
		}(chatID, text, lang)
	}

	return true
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func sendMsg(bot *tgbotapi.BotAPI, chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	bot.Send(msg) //nolint:errcheck
}

func sendMarkdownMsg(bot *tgbotapi.BotAPI, chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown"
	if _, err := bot.Send(msg); err != nil {
		plain := tgbotapi.NewMessage(chatID, text)
		bot.Send(plain) //nolint:errcheck
	}
}

// hasEnabledModel reports whether the user has an enabled AI model with
// credentials configured. Replaces the old newLLMClient() readiness probe.
func hasEnabledModel(st *store.Store, userID string) bool {
	model, err := st.AIModel().GetDefault(userID)
	return err == nil && model.Enabled && string(model.APIKey) != ""
}

// ── Status message ────────────────────────────────────────────────────────────

// statusMsg is the single entry-point message shown after /start.
// It checks what's configured and shows either a setup prompt or the ready state.
func statusMsg(st *store.Store, userID string, apiPort int, lang string) string {
	webURL := "http://localhost:3000"

	// Determine what's missing.
	hasModel := false
	if _, err := st.AIModel().GetDefault(userID); err == nil {
		hasModel = true
	}

	hasExchange := false
	if exchanges, err := st.Exchange().List(userID); err == nil {
		for _, e := range exchanges {
			if e.Enabled {
				hasExchange = true
				break
			}
		}
	}

	if !hasModel || !hasExchange {
		missing := ""
		if lang == "zh" {
			if !hasModel {
				missing += "\n❌ AI 模型 → 设置 → AI 模型 → 添加"
			}
			if !hasExchange {
				missing += "\n❌ 交易所 → 设置 → 交易所 → 添加"
			}
			return "⚙️ *需要完成初始配置*\n\n打开 Web 管理界面完成配置：\n→ " + webURL + "\n" + missing + "\n\n配置完成后发送 /start"
		}
		if !hasModel {
			missing += "\n❌ AI Model → Settings → AI Models → Add"
		}
		if !hasExchange {
			missing += "\n❌ Exchange → Settings → Exchanges → Add"
		}
		return "⚙️ *Setup required*\n\nOpen the web dashboard to complete setup:\n→ " + webURL + "\n" + missing + "\n\nSend /start when done."
	}

	// All configured — show ready state.
	if lang == "zh" {
		return `✅ *NOFX 就绪，开始交易吧！*

直接告诉我你想做什么：

📊 "查看我的持仓"
💰 "账户余额多少"
🤖 "帮我创建 BTC 趋势策略并启动"
⏹ "停止所有交易员"

/help 查看更多 · /lang 切换语言`
	}
	return `✅ *NOFX is ready!*

Just tell me what you want:

📊 "Show my positions"
💰 "What's my balance?"
🤖 "Create a BTC trend strategy and start it"
⏹ "Stop all traders"

/help for more · /lang to change language`
}

// ── Language ──────────────────────────────────────────────────────────────────

func langMenuMsg() string {
	return "🌐 *Choose your language*\n\n1 — English\n2 — 中文\n\nReply with 1 or 2"
}

func parseLangChoice(text string) string {
	switch strings.TrimSpace(text) {
	case "1", "en", "EN", "English", "english":
		return "en"
	case "2", "zh", "ZH", "中文", "chinese", "Chinese":
		return "zh"
	}
	return ""
}

// ── Help ──────────────────────────────────────────────────────────────────────

func helpMsg(lang string) string {
	if lang == "zh" {
		return `*NOFX 使用指南*

*查询*
• "查看我的持仓"
• "账户余额多少"
• "列出我的交易员"

*创建 & 启动*
• "帮我创建 BTC 趋势策略并跑起来"
• "保守型策略，只交易 BTC 和 ETH"

*控制*
• "启动交易员"
• "暂停交易员"
• "停止所有交易"

*命令*
/start — 刷新状态
/lang  — 切换语言
/help  — 帮助`
	}
	return `*NOFX Help*

*Query*
• "Show my positions"
• "What's my balance?"
• "List my traders"

*Create & start*
• "Create a BTC trend strategy and start it"
• "Conservative strategy, BTC and ETH only"

*Control*
• "Start trader"
• "Stop trader"
• "Stop all trading"

*Commands*
/start — refresh status
/lang  — change language
/help  — show this`
}
