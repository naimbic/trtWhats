package handlers

// TRT custom patch #35: Meta (Facebook) offline conversions.
//
// When a WhatsApp client places an order (auto-tagged "Converted"), send a
// single server event to Meta's Conversions API so the ad platform can
// attribute the sale to the Click-to-WhatsApp (CTWA) ad and optimize campaigns
// for people who actually buy. This posts DIRECTLY to graph.facebook.com — no
// third-party gateway and no per-event cost. Everything here is a safe no-op
// unless Meta CAPI is configured (WHATOMATE_META__* env vars in Coolify).

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
)

// captureCTWAReferral stores the ad click id (ctwa_clid) + ad source on the
// contact when an inbound message arrives from a Click-to-WhatsApp ad. Kept in
// contact.Metadata so it survives until the contact converts later (which can
// be many messages/days after the click).
func (a *App) captureCTWAReferral(contact *models.Contact, msg IncomingTextMessage) {
	if msg.Referral == nil || msg.Referral.CtwaClid == "" {
		return
	}
	meta := contact.Metadata
	if meta == nil {
		meta = models.JSONB{}
	}
	if _, ok := meta["ctwa_first_seen"]; !ok {
		meta["ctwa_first_seen"] = time.Now().UTC().Format(time.RFC3339)
	}
	// Keep the most recent ad click — it's the most relevant for the
	// attribution window if the customer clicked more than one ad.
	meta["ctwa_clid"] = msg.Referral.CtwaClid
	if msg.Referral.SourceID != "" {
		meta["ctwa_source_id"] = msg.Referral.SourceID
	}
	if msg.Referral.SourceType != "" {
		meta["ctwa_source_type"] = msg.Referral.SourceType
	}
	meta["ctwa_last_seen"] = time.Now().UTC().Format(time.RFC3339)
	contact.Metadata = meta
	if err := a.DB.Model(&models.Contact{}).Where("id = ?", contact.ID).
		Update("metadata", meta).Error; err != nil {
		a.Log.Error("captureCTWAReferral: failed to persist", "error", err, "contact", contact.ID)
		return
	}
	a.Log.Info("Captured CTWA referral", "contact", contact.ID, "source_id", msg.Referral.SourceID)
}

// orderValueFromFlow tries to read an order value from the submitted order-form
// fields. Belle Tulipe's COD form may not carry a price, in which case this
// returns 0 and the sender falls back to Meta.DefaultValue.
func orderValueFromFlow(data map[string]any) float64 {
	if data == nil {
		return 0
	}
	for _, key := range []string{"value", "total", "amount", "price", "prix", "montant", "total_price", "order_total"} {
		if v, ok := data[key]; ok {
			if f := parseFloatLoose(v); f > 0 {
				return f
			}
		}
	}
	return 0
}

func parseFloatLoose(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	case string:
		s := strings.ReplaceAll(strings.TrimSpace(n), ",", ".")
		s = strings.Map(func(r rune) rune {
			if (r >= '0' && r <= '9') || r == '.' {
				return r
			}
			return -1
		}, s)
		f, _ := strconv.ParseFloat(s, 64)
		return f
	}
	return 0
}

// sendMetaConversion posts one offline conversion to Meta's Conversions API.
// No-op unless Meta CAPI is fully configured. Safe to call in a goroutine.
func (a *App) sendMetaConversion(contact *models.Contact, value float64) {
	m := a.Config.Meta
	if !m.CAPIEnabled || m.DatasetID == "" || m.AccessToken == "" {
		return // integration not configured -> no-op
	}

	// Match keys: hashed phone (always) + ctwa_clid (best, when the customer
	// came from a Click-to-WhatsApp ad).
	userData := map[string]any{}
	if phone := digitsOnly(contact.PhoneNumber); phone != "" {
		userData["ph"] = []string{sha256Hex(phone)}
	}
	ctwaClid, _ := contact.Metadata["ctwa_clid"].(string)
	if ctwaClid != "" {
		userData["ctwa_clid"] = ctwaClid
	}
	if len(userData) == 0 {
		a.Log.Warn("sendMetaConversion: no match key (phone/ctwa_clid), skipping", "contact", contact.ID)
		return
	}

	if value <= 0 {
		value = m.DefaultValue
	}
	currency := firstNonEmpty(m.Currency, "MAD")
	eventName := firstNonEmpty(m.EventName, "Purchase")
	apiVersion := firstNonEmpty(m.APIVersion, a.Config.WhatsApp.APIVersion, "v21.0")
	base := firstNonEmpty(a.Config.WhatsApp.BaseURL, "https://graph.facebook.com")

	customData := map[string]any{"currency": currency}
	if value > 0 {
		customData["value"] = value
	}

	event := map[string]any{
		"event_name":        eventName,
		"event_time":        time.Now().Unix(),
		"action_source":     "business_messaging",
		"messaging_channel": "whatsapp",
		"event_id":          "trtwhats-" + contact.ID.String(), // dedup: one per contact
		"user_data":         userData,
		"custom_data":       customData,
	}

	body := map[string]any{
		"data":         []any{event},
		"access_token": m.AccessToken,
	}
	if m.TestEventCode != "" {
		body["test_event_code"] = m.TestEventCode
	}
	// Log the event we're about to send (never the access token) so the payload
	// can be verified in Events Manager / logs. Info in test mode, else Debug.
	if evJSON, e := json.Marshal(event); e == nil {
		if m.TestEventCode != "" {
			a.Log.Info("Meta conversion payload", "dataset", m.DatasetID, "test_event_code", m.TestEventCode, "event", string(evJSON))
		} else {
			a.Log.Debug("Meta conversion payload", "dataset", m.DatasetID, "event", string(evJSON))
		}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		a.Log.Error("sendMetaConversion: marshal failed", "error", err)
		return
	}

	url := fmt.Sprintf("%s/%s/%s/events", strings.TrimRight(base, "/"), apiVersion, m.DatasetID)
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(payload))
	if err != nil {
		a.Log.Error("sendMetaConversion: request build failed", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.HTTPClient.Do(req)
	if err != nil {
		a.Log.Error("sendMetaConversion: request failed", "error", err, "contact", contact.ID)
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		a.Log.Error("sendMetaConversion: Meta rejected event",
			"status", resp.StatusCode, "body", string(respBody), "contact", contact.ID)
		return
	}
	a.Log.Info("Sent Meta offline conversion",
		"contact", contact.ID, "event", eventName, "value", value, "currency", currency,
		"has_ctwa", ctwaClid != "", "test_mode", m.TestEventCode != "")
}

func digitsOnly(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}

// sha256Hex returns the lowercase hex SHA-256 of the normalized input, as Meta
// requires for hashed match keys (e.g. phone number).
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(s))))
	return hex.EncodeToString(sum[:])
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
