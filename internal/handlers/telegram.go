package handlers

// TRT custom patch #92: Telegram sale notifications.
//
// When a WhatsApp/Instagram/Messenger client is marked sold (a conversion value
// is saved on the contact), push a formatted order card to a Telegram chat so
// the team gets an instant ping with the customer, amount, city and address.
//
// Design rules (kept deliberately small — one builder + one sender):
//   - The bot token lives only in TelegramSettings (encrypted at rest) and the
//     request URL; it is NEVER logged. On a send error we log a generic reason,
//     not the error (which embeds the token URL).
//   - All user-supplied strings are HTML-escaped before going into the message.
//   - The send is fire-and-forget off the save path: it never blocks, never
//     fails, and never throws back into the conversion save.
//   - 8s timeout per send.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

const telegramMask = "••••••••"

// ---- Settings CRUD ----------------------------------------------------------

type telegramSettingsResponse struct {
	Enabled     bool   `json:"enabled"`
	ChatID      string `json:"chat_id"`
	HasBotToken bool   `json:"has_bot_token"`
}

func telegramSettingsToResponse(s models.TelegramSettings) telegramSettingsResponse {
	return telegramSettingsResponse{
		Enabled:     s.Enabled,
		ChatID:      s.ChatID,
		HasBotToken: s.BotToken != "",
	}
}

// loadTelegramSettings returns the org's row (zero-value if none yet).
func (a *App) loadTelegramSettings(orgID uuid.UUID) (models.TelegramSettings, bool) {
	var s models.TelegramSettings
	if err := a.DB.Where("organization_id = ?", orgID).First(&s).Error; err != nil {
		return models.TelegramSettings{OrganizationID: orgID}, false
	}
	return s, true
}

// GetTelegramSettings — GET /api/telegram/settings
func (a *App) GetTelegramSettings(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceSettingsNotification, models.ActionRead)
	if err != nil {
		return nil
	}
	s, _ := a.loadTelegramSettings(orgID)
	return r.SendEnvelope(telegramSettingsToResponse(s))
}

type telegramSettingsRequest struct {
	Enabled  *bool  `json:"enabled"`
	ChatID   string `json:"chat_id"`
	BotToken string `json:"bot_token"`
}

// UpdateTelegramSettings — PUT /api/telegram/settings (upsert)
func (a *App) UpdateTelegramSettings(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceSettingsNotification, models.ActionWrite)
	if err != nil {
		return nil
	}
	var req telegramSettingsRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}

	s, existed := a.loadTelegramSettings(orgID)
	if req.Enabled != nil {
		s.Enabled = *req.Enabled
	}
	s.ChatID = strings.TrimSpace(req.ChatID)
	// Only overwrite the stored token when a real new value is sent (the UI sends
	// the mask when the field is left untouched).
	if tok := strings.TrimSpace(req.BotToken); tok != "" && tok != telegramMask {
		enc, e := crypto.Encrypt(tok, a.Config.App.EncryptionKey)
		if e != nil {
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "failed to save bot token", nil, "")
		}
		s.BotToken = enc
	}

	if existed {
		if err := a.DB.Model(&models.TelegramSettings{}).Where("organization_id = ?", orgID).
			Updates(map[string]any{"enabled": s.Enabled, "chat_id": s.ChatID, "bot_token": s.BotToken}).Error; err != nil {
			a.Log.Error("telegram: update settings failed", "err", err)
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "failed to save Telegram settings", nil, "")
		}
	} else {
		s.ID = uuid.New()
		s.OrganizationID = orgID
		if err := a.DB.Create(&s).Error; err != nil {
			a.Log.Error("telegram: create settings failed", "err", err)
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "failed to save Telegram settings", nil, "")
		}
	}
	return r.SendEnvelope(telegramSettingsToResponse(s))
}

// TestTelegram — POST /api/telegram/settings/test — sends a probe message so the
// user can confirm the token + chat id before relying on it.
func (a *App) TestTelegram(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceSettingsNotification, models.ActionWrite)
	if err != nil {
		return nil
	}
	s, ok := a.loadTelegramSettings(orgID)
	if !ok || strings.TrimSpace(s.ChatID) == "" || s.BotToken == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Set a bot token and chat id first", nil, "")
	}
	s.DecryptSecrets(a.Config.App.EncryptionKey)
	msg := "✅ <b>trtWhats</b>\nطروا كونيكطي مزيان — التبليغات ديال البيعات غادي توصل هنا.\n<i>Connexion Telegram OK — les ventes arriveront ici.</i>"
	if reason := a.sendTelegram(s.BotToken, s.ChatID, msg); reason != "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway, "Telegram rejected the test: "+reason, nil, "")
	}
	return r.SendEnvelope(map[string]any{"sent": true})
}

// ---- Notification on sale ---------------------------------------------------

// notifyTelegramSale pushes a sale card to the org's Telegram chat. Safe no-op
// when Telegram is off or unconfigured. Fire-and-forget: call it in a goroutine.
func (a *App) notifyTelegramSale(orgID uuid.UUID, contact *models.Contact) {
	defer func() {
		if rec := recover(); rec != nil {
			a.Log.Error("notifyTelegramSale: recovered from panic", "org", orgID)
		}
	}()
	s, ok := a.loadTelegramSettings(orgID)
	if !ok || !s.Enabled || s.BotToken == "" || strings.TrimSpace(s.ChatID) == "" {
		return
	}
	s.DecryptSecrets(a.Config.App.EncryptionKey)
	msg := buildTelegramSaleMessage(contact)
	if reason := a.sendTelegram(s.BotToken, s.ChatID, msg); reason != "" {
		a.Log.Warn("notifyTelegramSale: send failed", "org", orgID, "contact", contact.ID, "reason", reason)
	}
}

// buildTelegramSaleMessage is a pure builder: contact in, HTML message out. No
// I/O, no logging — unit-testable. Darija-first with a short French echo.
func buildTelegramSaleMessage(c *models.Contact) string {
	var b strings.Builder
	b.WriteString("🛒 <b>بيعة جديدة / Nouvelle vente</b>\n\n")

	name := strings.TrimSpace(c.ProfileName)
	if name == "" {
		name = "—"
	}
	b.WriteString("👤 الكليان: <b>" + html.EscapeString(name) + "</b>\n")

	if digits := telegramPhoneDigits(c.PhoneNumber); digits != "" {
		b.WriteString("📱 التيليفون: <code>" + html.EscapeString(telegramFormatPhone(digits)) +
			"</code> — <a href=\"https://wa.me/" + digits + "\">WhatsApp</a>\n")
	}
	if city := strings.TrimSpace(c.City); city != "" {
		b.WriteString("🏙️ المدينة: " + html.EscapeString(city) + "\n")
	}
	if addr := strings.TrimSpace(c.Address); addr != "" {
		b.WriteString("📍 العنوان: " + html.EscapeString(addr) + "\n")
	}

	b.WriteString("\n")
	if c.ConversionQuantity > 0 {
		b.WriteString(fmt.Sprintf("📦 الكمية: %d\n", c.ConversionQuantity))
	}
	b.WriteString("💰 المبلغ: <b>" + telegramMoney(c.ConversionValue) + " درهم</b>\n")

	// Source (tags) + channel/account.
	if tags := telegramTags(c.Tags); tags != "" {
		b.WriteString("🏷️ المصدر: " + html.EscapeString(tags) + "\n")
	}
	if ch := telegramChannelLabel(c); ch != "" {
		b.WriteString("📲 القناة: " + html.EscapeString(ch) + "\n")
	}

	b.WriteString("\n🕒 " + telegramNowCasa())
	return b.String()
}

// telegramPhoneDigits strips everything but digits (and a leading +).
func telegramPhoneDigits(p string) string {
	var b strings.Builder
	for _, r := range p {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// telegramFormatPhone renders a Moroccan number as a readable local string.
// 212612345678 -> 06 12 34 56 78 ; anything else is grouped in pairs as-is.
func telegramFormatPhone(digits string) string {
	local := digits
	if strings.HasPrefix(digits, "212") && len(digits) >= 12 {
		local = "0" + digits[3:]
	}
	// group in pairs
	var parts []string
	for i := 0; i < len(local); i += 2 {
		end := i + 2
		if end > len(local) {
			end = len(local)
		}
		parts = append(parts, local[i:end])
	}
	return strings.Join(parts, " ")
}

// telegramMoney formats whole dirhams with thousands separators, no decimals.
func telegramMoney(v float64) string {
	n := int64(v + 0.5) // round to nearest dirham
	s := fmt.Sprintf("%d", n)
	neg := false
	if strings.HasPrefix(s, "-") {
		neg = true
		s = s[1:]
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ' ')
		}
		out = append(out, c)
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

// telegramTags joins the contact's tags into a single comma list. JSONBArray is
// []any, so each element is type-asserted to a string.
func telegramTags(tags models.JSONBArray) string {
	parts := make([]string, 0, len(tags))
	for _, t := range tags {
		if s, ok := t.(string); ok {
			if s = strings.TrimSpace(s); s != "" {
				parts = append(parts, s)
			}
		}
	}
	return strings.Join(parts, ", ")
}

// telegramChannelLabel describes which inbox/number the sale came through.
func telegramChannelLabel(c *models.Contact) string {
	acct := strings.TrimSpace(c.WhatsAppAccount)
	ch := strings.TrimSpace(c.Channel)
	switch {
	case ch != "" && acct != "":
		return ch + " · " + acct
	case acct != "":
		return acct
	default:
		return ch
	}
}

// telegramNowCasa returns the current time in Africa/Casablanca, 24h.
func telegramNowCasa() string {
	loc, err := time.LoadLocation("Africa/Casablanca")
	if err != nil {
		loc = time.UTC
	}
	return time.Now().In(loc).Format("2006-01-02 15:04")
}

// sendTelegram posts an HTML message to a chat. Returns "" on success or a short,
// token-free reason on failure. Never logs the token or the raw URL/error.
func (a *App) sendTelegram(botToken, chatID, htmlText string) string {
	payload, _ := json.Marshal(map[string]any{
		"chat_id":                  chatID,
		"text":                     htmlText,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	})
	url := "https://api.telegram.org/bot" + botToken + "/sendMessage"

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "could not build the request"
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		// NB: err embeds the URL (with the token) — do not surface or log it.
		return "could not reach Telegram (network error)"
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Pull Telegram's description if present, but never the request echo.
		var body struct {
			Description string `json:"description"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		if body.Description != "" {
			return body.Description
		}
		return fmt.Sprintf("Telegram returned HTTP %d", resp.StatusCode)
	}
	return ""
}
