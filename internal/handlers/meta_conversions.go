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

// sendMetaConversion posts one offline conversion to Meta's Conversions API for
// the given space (WhatsApp account). The partner access token is global (env);
// the dataset id, enable switch and other settings belong to the space, so each
// client/number reports to its own ad account. No-op unless the space has it
// enabled with a dataset AND a global token is configured. Safe in a goroutine.
// Returns (sent, detail): sent is true only when an event was actually posted
// to Meta; detail carries Meta's error message when it wasn't (so the agent can
// see exactly why it was rejected).
func (a *App) sendMetaConversion(account *models.WhatsAppAccount, contact *models.Contact, value float64, quantity int) (bool, string) {
	if account == nil {
		return false, ""
	}
	// Prefer this space's own token; fall back to the global partner token (env).
	token := firstNonEmpty(account.MetaAccessToken, a.Config.Meta.AccessToken)
	if !account.MetaCapiEnabled || account.MetaDatasetID == "" || token == "" {
		return false, "" // not configured for this space -> no-op
	}
	datasetID := account.MetaDatasetID
	testEventCode := account.MetaTestEventCode

	// Match keys: hashed phone (always) + ctwa_clid (best, when the customer
	// came from a Click-to-WhatsApp ad).
	userData := map[string]any{}
	if phone := digitsOnly(contact.PhoneNumber); phone != "" {
		userData["ph"] = []string{sha256Hex(phone)}
	}
	ctwaClid, _ := contact.Metadata["ctwa_clid"].(string)
	if ctwaClid != "" {
		userData["ctwa_clid"] = ctwaClid
		// Meta's Click-to-WhatsApp Conversions API expects the WhatsApp Business
		// Account id alongside ctwa_clid.
		if account.BusinessID != "" {
			userData["whatsapp_business_account_id"] = account.BusinessID
		}
	}
	// Meta requires page_id in user_data for business_messaging / whatsapp events
	// (the Facebook Page behind the Click-to-WhatsApp ads).
	if account.MetaPageID != "" {
		userData["page_id"] = account.MetaPageID
	}
	// Meta requires a ctwa_clid for business_messaging/whatsapp conversions — it
	// only exists for customers who arrived by clicking a Click-to-WhatsApp ad
	// (and only for messages received after we started capturing it). Without it
	// there is no ad to attribute the order to, so Meta would reject the event;
	// stop here with a clear message instead.
	if ctwaClid == "" {
		return false, "this customer didn't come from a Click-to-WhatsApp ad (no ad click id), so Meta can't attribute this order"
	}

	if value <= 0 {
		value = account.MetaDefaultValue
	}
	if value <= 0 {
		value = a.Config.Meta.DefaultValue
	}
	currency := firstNonEmpty(account.MetaCurrency, a.Config.Meta.Currency, "MAD")
	eventName := firstNonEmpty(a.Config.Meta.EventName, "Purchase")
	apiVersion := firstNonEmpty(account.APIVersion, a.Config.WhatsApp.APIVersion, "v21.0")
	base := firstNonEmpty(a.Config.WhatsApp.BaseURL, "https://graph.facebook.com")

	customData := map[string]any{"currency": currency}
	if value > 0 {
		customData["value"] = value
	}
	if quantity > 0 {
		customData["num_items"] = quantity
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
		"access_token": token,
	}
	if testEventCode != "" {
		body["test_event_code"] = testEventCode
	}
	// Log the event we're about to send (never the access token) so the payload
	// can be verified in Events Manager / logs. Info in test mode, else Debug.
	if evJSON, e := json.Marshal(event); e == nil {
		if testEventCode != "" {
			a.Log.Info("Meta conversion payload", "space", account.Name, "dataset", datasetID, "test_event_code", testEventCode, "event", string(evJSON))
		} else {
			a.Log.Debug("Meta conversion payload", "space", account.Name, "dataset", datasetID, "event", string(evJSON))
		}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		a.Log.Error("sendMetaConversion: marshal failed", "error", err)
		return false, "internal error building the request"
	}

	url := fmt.Sprintf("%s/%s/%s/events", strings.TrimRight(base, "/"), apiVersion, datasetID)
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(payload))
	if err != nil {
		a.Log.Error("sendMetaConversion: request build failed", "error", err)
		return false, "internal error building the request"
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.HTTPClient.Do(req)
	if err != nil {
		a.Log.Error("sendMetaConversion: request failed", "error", err, "contact", contact.ID)
		return false, "could not reach Meta (network error)"
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		metaErr := parseMetaError(respBody)
		a.Log.Error("sendMetaConversion: Meta rejected event",
			"status", resp.StatusCode, "meta_error", metaErr, "body", string(respBody), "contact", contact.ID)
		return false, metaErr
	}
	a.Log.Info("Sent Meta offline conversion",
		"space", account.Name, "contact", contact.ID, "event", eventName, "value", value,
		"quantity", quantity, "currency", currency, "has_ctwa", ctwaClid != "", "test_mode", testEventCode != "")
	return true, ""
}

// parseMetaError pulls Meta's human-readable error message (and code) out of a
// Graph API error response body, so the agent sees the real reason.
func parseMetaError(body []byte) string {
	var e struct {
		Error struct {
			Message        string `json:"message"`
			Type           string `json:"type"`
			Code           int    `json:"code"`
			ErrorSubcode   int    `json:"error_subcode"`
			ErrorUserTitle string `json:"error_user_title"`
			ErrorUserMsg   string `json:"error_user_msg"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &e); err == nil && e.Error.Message != "" {
		// error_user_msg / error_user_title usually name the exact bad field.
		msg := e.Error.Message
		if e.Error.ErrorUserMsg != "" {
			msg = msg + " — " + e.Error.ErrorUserMsg
		} else if e.Error.ErrorUserTitle != "" {
			msg = msg + " — " + e.Error.ErrorUserTitle
		}
		codes := ""
		if e.Error.Code != 0 {
			codes = fmt.Sprintf(" (code %d", e.Error.Code)
			if e.Error.ErrorSubcode != 0 {
				codes += fmt.Sprintf("/subcode %d", e.Error.ErrorSubcode)
			}
			codes += ")"
		}
		return msg + codes
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 300 {
		s = s[:300]
	}
	if s == "" {
		return "unknown error from Meta"
	}
	return s
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
