package handlers

// TRT custom patch #75: Facebook Messenger integration (MVP — receive text/image,
// reply with text). Messenger runs on Meta's Messenger Platform: inbound DMs
// arrive on a webhook (object "page") keyed by the Facebook Page id; we upsert the
// contact by its Page-Scoped id (PSID) and store the message on the same
// contacts/messages tables (Channel=messenger) so it shows in the normal inbox.
// Sending uses POST /{page_id}/messages with the Page access token.
//
// This is a near-exact mirror of the Instagram channel (instagram.go); the only
// real differences are the webhook object ("page" vs "instagram"), the account
// key (Page id), and the user-profile fields (first_name/last_name vs username).

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

const fbGraphBase = "https://graph.facebook.com"

func fbAPIVersion(acc *models.MessengerAccount) string {
	if acc != nil && acc.APIVersion != "" {
		return acc.APIVersion
	}
	return "v21.0"
}

// fbToken returns the decrypted Page access token.
func (a *App) fbToken(acc *models.MessengerAccount) (string, error) {
	if acc == nil || !acc.IsActive || acc.AccessToken == "" {
		return "", fmt.Errorf("messenger not configured for this account")
	}
	return crypto.Decrypt(acc.AccessToken, a.Config.App.EncryptionKey)
}

// fbSendText sends a plain-text DM to a PSID via the Messenger Platform.
func (a *App) fbSendText(acc *models.MessengerAccount, psid, text string) (string, error) {
	token, err := a.fbToken(acc)
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"recipient":      map[string]any{"id": psid},
		"messaging_type": "RESPONSE",
		"message":        map[string]any{"text": text},
	}
	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("%s/%s/%s/messages?access_token=%s", fbGraphBase, fbAPIVersion(acc), acc.PageID, token)
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("messenger send failed (%d): %s", resp.StatusCode, string(raw))
	}
	var decoded map[string]any
	_ = json.Unmarshal(raw, &decoded)
	if mid, ok := decoded["message_id"].(string); ok {
		return mid, nil
	}
	return "", nil
}

// fbSendAttachment sends a media attachment (by public URL) to a PSID. TRT #76.
func (a *App) fbSendAttachment(acc *models.MessengerAccount, psid, attType, mediaURL string) (string, error) {
	token, err := a.fbToken(acc)
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"recipient":      map[string]any{"id": psid},
		"messaging_type": "RESPONSE",
		"message": map[string]any{
			"attachment": map[string]any{
				"type":    attType,
				"payload": map[string]any{"url": mediaURL, "is_reusable": true},
			},
		},
	}
	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("%s/%s/%s/messages?access_token=%s", fbGraphBase, fbAPIVersion(acc), acc.PageID, token)
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("messenger media send failed (%d): %s", resp.StatusCode, string(raw))
	}
	var decoded map[string]any
	_ = json.Unmarshal(raw, &decoded)
	if mid, ok := decoded["message_id"].(string); ok {
		return mid, nil
	}
	return "", nil
}

// ---- Webhook ----------------------------------------------------------------

// MessengerWebhookVerify handles the GET verification handshake. Meta sends
// hub.mode=subscribe, hub.verify_token, hub.challenge — echo the challenge when
// the token matches any configured Messenger account. GET /api/messenger/webhook
func (a *App) MessengerWebhookVerify(r *fastglue.Request) error {
	mode := string(r.RequestCtx.QueryArgs().Peek("hub.mode"))
	token := string(r.RequestCtx.QueryArgs().Peek("hub.verify_token"))
	challenge := string(r.RequestCtx.QueryArgs().Peek("hub.challenge"))
	if mode != "subscribe" || token == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "bad verify request", nil, "")
	}
	var count int64
	a.DB.Model(&models.MessengerAccount{}).Where("webhook_verify_token = ?", token).Count(&count)
	if count == 0 {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "verify token mismatch", nil, "")
	}
	r.RequestCtx.SetStatusCode(fasthttp.StatusOK)
	r.RequestCtx.SetBodyString(challenge)
	return nil
}

type fbWebhookPayload struct {
	Object string `json:"object"`
	Entry  []struct {
		ID        string `json:"id"`
		Messaging []struct {
			Sender    struct{ ID string `json:"id"` } `json:"sender"`
			Recipient struct{ ID string `json:"id"` } `json:"recipient"`
			Timestamp int64 `json:"timestamp"`
			Message   *struct {
				MID         string `json:"mid"`
				Text        string `json:"text"`
				IsEcho      bool   `json:"is_echo"`
				Attachments []struct {
					Type    string `json:"type"`
					Payload struct {
						URL string `json:"url"`
					} `json:"payload"`
				} `json:"attachments"`
			} `json:"message"`
		} `json:"messaging"`
	} `json:"entry"`
}

// MessengerWebhook receives inbound Page messaging events. POST /api/messenger/webhook (public)
func (a *App) MessengerWebhook(r *fastglue.Request) error {
	rawBody := r.RequestCtx.PostBody()
	var payload fbWebhookPayload
	if err := json.Unmarshal(rawBody, &payload); err != nil {
		return r.SendEnvelope(map[string]any{"ok": true})
	}
	if payload.Object != "page" {
		return r.SendEnvelope(map[string]any{"ok": true})
	}

	for _, entry := range payload.Entry {
		for _, m := range entry.Messaging {
			if m.Message == nil || m.Message.IsEcho {
				continue // skip echoes of our own sends and non-message events
			}
			// recipient.id is our Page; fall back to entry.id.
			pageID := m.Recipient.ID
			if pageID == "" {
				pageID = entry.ID
			}
			var acc models.MessengerAccount
			if err := a.DB.Where("page_id = ?", pageID).First(&acc).Error; err != nil {
				a.Log.Warn("messenger webhook: unknown page", "page_id", pageID)
				continue
			}
			// Verify signature when an app secret is configured.
			if acc.AppSecret != "" {
				if secret, derr := crypto.Decrypt(acc.AppSecret, a.Config.App.EncryptionKey); derr == nil {
					sig := string(r.RequestCtx.Request.Header.Peek("X-Hub-Signature-256"))
					if !fbVerifySignature(sig, rawBody, secret) {
						a.Log.Warn("messenger webhook: bad signature", "page_id", pageID)
						continue
					}
				}
			}
			a.handleMessengerInbound(&acc, m.Sender.ID, m.Message.MID, m.Message.Text, m.Message.Attachments)
		}
	}
	return r.SendEnvelope(map[string]any{"ok": true})
}

// handleMessengerInbound upserts the contact (by PSID) and stores the message.
func (a *App) handleMessengerInbound(acc *models.MessengerAccount, psid, mid, text string, attachments []struct {
	Type    string `json:"type"`
	Payload struct {
		URL string `json:"url"`
	} `json:"payload"`
}) {
	if psid == "" {
		return
	}
	// Upsert contact by (org, channel=messenger, external_id=PSID).
	var contact models.Contact
	err := a.DB.Where("organization_id = ? AND channel = ? AND external_id = ?",
		acc.OrganizationID, models.ChannelMessenger, psid).First(&contact).Error
	now := time.Now()
	if err != nil {
		contact = models.Contact{
			BaseModel:       models.BaseModel{ID: uuid.New()},
			OrganizationID:  acc.OrganizationID,
			Channel:         models.ChannelMessenger,
			ExternalID:      psid,
			ProfileName:     a.fbFetchName(acc, psid),
			WhatsAppAccount: acc.Name, // group under the Page in the inbox
			LastInboundAt:   &now,
		}
		if err := a.DB.Create(&contact).Error; err != nil {
			a.Log.Error("messenger: failed to create contact", "err", err)
			return
		}
	}

	messageType := models.MessageTypeText
	content := text
	mediaURL := ""
	mediaMime := ""
	if len(attachments) > 0 {
		at := attachments[0]
		switch strings.ToLower(at.Type) {
		case "image":
			messageType = models.MessageTypeImage
		case "video":
			messageType = models.MessageTypeVideo
		case "audio":
			messageType = models.MessageTypeAudio
		default:
			messageType = models.MessageTypeDocument
		}
		// TRT #87: download the CDN media locally so it renders via /api/media/{id}
		// (the frontend can't fetch the Meta CDN URL directly). Fall back to the CDN
		// URL if the download fails.
		if rel, m, derr := a.downloadRemoteMedia(at.Payload.URL, ""); derr == nil {
			mediaURL = rel
			mediaMime = m
		} else {
			mediaURL = at.Payload.URL
			a.Log.Warn("messenger: media download failed, keeping CDN url", "err", derr)
		}
	}

	message := models.Message{
		BaseModel:         models.BaseModel{ID: uuid.New()},
		OrganizationID:    acc.OrganizationID,
		WhatsAppAccount:   acc.Name,
		Channel:           models.ChannelMessenger,
		ContactID:         contact.ID,
		WhatsAppMessageID: mid,
		Direction:         models.DirectionIncoming,
		MessageType:       messageType,
		Content:           content,
		MediaURL:          mediaURL,
		MediaMimeType:     mediaMime,
		Status:            models.MessageStatusDelivered,
	}
	if err := a.DB.Create(&message).Error; err != nil {
		a.Log.Error("messenger: failed to save message", "err", err)
		return
	}

	preview := content
	if messageType != models.MessageTypeText {
		preview = "[" + string(messageType) + "]"
	}
	if len(preview) > 100 {
		preview = preview[:97] + "..."
	}
	a.DB.Model(&contact).Updates(map[string]any{
		"last_message_at":      now,
		"last_inbound_at":      now,
		"last_message_preview": preview,
		"is_read":              false,
	})
	a.broadcastNewMessage(acc.OrganizationID, &message, &contact)
	a.Log.Info("messenger: inbound message", "account", acc.Name, "psid", psid, "type", messageType)
}

// fbFetchName best-effort fetches the sender's name (non-fatal).
func (a *App) fbFetchName(acc *models.MessengerAccount, psid string) string {
	token, err := a.fbToken(acc)
	if err != nil {
		return ""
	}
	url := fmt.Sprintf("%s/%s/%s?fields=name,first_name,last_name&access_token=%s", fbGraphBase, fbAPIVersion(acc), psid, token)
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	resp, err := a.HTTPClient.Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var d map[string]any
	_ = json.Unmarshal(raw, &d)
	if n, ok := d["name"].(string); ok && n != "" {
		return n
	}
	first, _ := d["first_name"].(string)
	last, _ := d["last_name"].(string)
	return strings.TrimSpace(first + " " + last)
}

// fbVerifySignature checks X-Hub-Signature-256: sha256=<hmac(body, appSecret)>.
func fbVerifySignature(header string, body []byte, secret string) bool {
	parts := strings.SplitN(header, "=", 2)
	if len(parts) != 2 || parts[0] != "sha256" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(parts[1]))
}

// clearUnhandledOnAgentReply marks an Instagram/Messenger chat "handled" when a
// human agent replies — clears the orange order-pending/"not answered" bubble and
// broadcasts contact_read, mirroring the WhatsApp send path (messages.go). Bot /
// auto replies pass nil and never clear it. TRT #85.
func (a *App) clearUnhandledOnAgentReply(orgID uuid.UUID, contact *models.Contact, sentByUserID *uuid.UUID) {
	if sentByUserID == nil || contact == nil {
		return
	}
	// Mark the contact's unread incoming messages read so the unread badge clears —
	// IG/Messenger inbound stay "delivered" (no read receipts), so without this the
	// badge would never clear after answering. Only a human agent reply does this.
	a.DB.Model(&models.Message{}).
		Where("contact_id = ? AND direction = ? AND status <> ?", contact.ID, models.DirectionIncoming, models.MessageStatusRead).
		Update("status", models.MessageStatusRead)
	a.DB.Model(&models.Contact{}).Where("id = ?", contact.ID).
		Updates(map[string]any{"order_pending": false, "is_read": true})
	contact.OrderPending = false
	a.broadcastContactRead(orgID, contact.ID)
}

// sendMessengerReply sends an agent's text reply to a Messenger contact and
// records it. Used by SendMessage when contact.Channel == messenger.
func (a *App) sendMessengerReply(contact *models.Contact, text string, sentByUserID *uuid.UUID) (*models.Message, error) {
	var acc models.MessengerAccount
	q := a.DB.Where("organization_id = ?", contact.OrganizationID)
	if contact.WhatsAppAccount != "" {
		q = q.Where("name = ?", contact.WhatsAppAccount)
	}
	if err := q.First(&acc).Error; err != nil {
		if err := a.DB.Where("organization_id = ? AND is_active = ?", contact.OrganizationID, true).First(&acc).Error; err != nil {
			return nil, fmt.Errorf("no Messenger account for this contact")
		}
	}

	msg := &models.Message{
		BaseModel:       models.BaseModel{ID: uuid.New()},
		OrganizationID:  contact.OrganizationID,
		WhatsAppAccount: acc.Name,
		Channel:         models.ChannelMessenger,
		ContactID:       contact.ID,
		Direction:       models.DirectionOutgoing,
		MessageType:     models.MessageTypeText,
		Content:         text,
		Status:          models.MessageStatusPending,
		SentByUserID:    sentByUserID,
	}
	if err := a.DB.Create(msg).Error; err != nil {
		return nil, err
	}

	mid, err := a.fbSendText(&acc, contact.ExternalID, text)
	now := time.Now()
	if err != nil {
		a.DB.Model(msg).Updates(map[string]any{"status": models.MessageStatusFailed, "error_message": err.Error()})
		return nil, err
	}
	a.DB.Model(msg).Updates(map[string]any{"status": models.MessageStatusSent, "whats_app_message_id": mid})
	msg.Status = models.MessageStatusSent
	msg.WhatsAppMessageID = mid

	preview := text
	if len(preview) > 100 {
		preview = preview[:97] + "..."
	}
	a.DB.Model(contact).Updates(map[string]any{"last_message_at": now, "last_message_preview": preview, "is_read": true})
	a.broadcastNewMessage(contact.OrganizationID, msg, contact)
	a.clearUnhandledOnAgentReply(contact.OrganizationID, contact, sentByUserID)
	return msg, nil
}

// sendMessengerMediaReply sends an agent's media reply to a Messenger contact by
// public URL and records it. TRT #76. Any caption is sent as a follow-up text.
func (a *App) sendMessengerMediaReply(contact *models.Contact, relPath, publicURL, mimeType, caption string, sentByUserID *uuid.UUID) (*models.Message, error) {
	var acc models.MessengerAccount
	q := a.DB.Where("organization_id = ?", contact.OrganizationID)
	if contact.WhatsAppAccount != "" {
		q = q.Where("name = ?", contact.WhatsAppAccount)
	}
	if err := q.First(&acc).Error; err != nil {
		if err := a.DB.Where("organization_id = ? AND is_active = ?", contact.OrganizationID, true).First(&acc).Error; err != nil {
			return nil, fmt.Errorf("no Messenger account for this contact")
		}
	}

	msgType := messageTypeFromMime(mimeType)
	msg := &models.Message{
		BaseModel:       models.BaseModel{ID: uuid.New()},
		OrganizationID:  contact.OrganizationID,
		WhatsAppAccount: acc.Name,
		Channel:         models.ChannelMessenger,
		ContactID:       contact.ID,
		Direction:       models.DirectionOutgoing,
		MessageType:     msgType,
		Content:         caption,
		MediaURL:        relPath,
		MediaMimeType:   mimeType,
		Status:          models.MessageStatusPending,
		SentByUserID:    sentByUserID,
	}
	if err := a.DB.Create(msg).Error; err != nil {
		return nil, err
	}

	mid, err := a.fbSendAttachment(&acc, contact.ExternalID, attachmentTypeFromMime(mimeType), publicURL)
	now := time.Now()
	if err != nil {
		a.DB.Model(msg).Updates(map[string]any{"status": models.MessageStatusFailed, "error_message": err.Error()})
		return nil, err
	}
	a.DB.Model(msg).Updates(map[string]any{"status": models.MessageStatusSent, "whats_app_message_id": mid})
	msg.Status = models.MessageStatusSent
	msg.WhatsAppMessageID = mid

	if strings.TrimSpace(caption) != "" {
		_, _ = a.fbSendText(&acc, contact.ExternalID, caption)
	}

	a.DB.Model(contact).Updates(map[string]any{"last_message_at": now, "last_message_preview": "[" + string(msgType) + "]", "is_read": true})
	a.broadcastNewMessage(contact.OrganizationID, msg, contact)
	a.clearUnhandledOnAgentReply(contact.OrganizationID, contact, sentByUserID)
	return msg, nil
}

// ---- Messenger account CRUD (settings) --------------------------------------

type messengerAccountResponse struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	PageID             string `json:"page_id"`
	PageName           string `json:"page_name"`
	APIVersion         string `json:"api_version"`
	WebhookVerifyToken string `json:"webhook_verify_token"`
	IsActive           bool   `json:"is_active"`
	HasAccessToken     bool   `json:"has_access_token"`
	HasAppSecret       bool   `json:"has_app_secret"`
}

func messengerAccountToResponse(acc models.MessengerAccount) messengerAccountResponse {
	return messengerAccountResponse{
		ID:                 acc.ID.String(),
		Name:               acc.Name,
		PageID:             acc.PageID,
		PageName:           acc.PageName,
		APIVersion:         acc.APIVersion,
		WebhookVerifyToken: acc.WebhookVerifyToken,
		IsActive:           acc.IsActive,
		HasAccessToken:     acc.AccessToken != "",
		HasAppSecret:       acc.AppSecret != "",
	}
}

// ListMessengerAccounts — GET /api/messenger/accounts
func (a *App) ListMessengerAccounts(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceAccounts, models.ActionRead)
	if err != nil {
		return nil
	}
	var accts []models.MessengerAccount
	a.DB.Where("organization_id = ?", orgID).Order("created_at").Find(&accts)
	out := make([]messengerAccountResponse, len(accts))
	for i, acc := range accts {
		out[i] = messengerAccountToResponse(acc)
	}
	return r.SendEnvelope(map[string]any{"accounts": out})
}

type messengerAccountRequest struct {
	Name               string `json:"name"`
	PageID             string `json:"page_id"`
	PageName           string `json:"page_name"`
	APIVersion         string `json:"api_version"`
	WebhookVerifyToken string `json:"webhook_verify_token"`
	IsActive           *bool  `json:"is_active"`
	AccessToken        string `json:"access_token"`
	AppSecret          string `json:"app_secret"`
}

// CreateMessengerAccount — POST /api/messenger/accounts
func (a *App) CreateMessengerAccount(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceAccounts, models.ActionWrite)
	if err != nil {
		return nil
	}
	var req messengerAccountRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.PageID) == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "name and page_id are required", nil, "")
	}
	acc := models.MessengerAccount{
		BaseModel:          models.BaseModel{ID: uuid.New()},
		OrganizationID:     orgID,
		Name:               req.Name,
		PageID:             req.PageID,
		PageName:           req.PageName,
		APIVersion:         firstNonEmpty(req.APIVersion, "v21.0"),
		WebhookVerifyToken: req.WebhookVerifyToken,
		IsActive:           true,
	}
	if req.IsActive != nil {
		acc.IsActive = *req.IsActive
	}
	if req.AccessToken != "" {
		enc, e := crypto.Encrypt(req.AccessToken, a.Config.App.EncryptionKey)
		if e != nil {
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "failed to save token", nil, "")
		}
		acc.AccessToken = enc
	}
	if req.AppSecret != "" {
		enc, e := crypto.Encrypt(req.AppSecret, a.Config.App.EncryptionKey)
		if e != nil {
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "failed to save app secret", nil, "")
		}
		acc.AppSecret = enc
	}
	if err := a.DB.Create(&acc).Error; err != nil {
		a.Log.Error("messenger: create account failed", "err", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "failed to create Messenger account", nil, "")
	}
	return r.SendEnvelope(messengerAccountToResponse(acc))
}

// UpdateMessengerAccount — PUT /api/messenger/accounts/{id}
func (a *App) UpdateMessengerAccount(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceAccounts, models.ActionWrite)
	if err != nil {
		return nil
	}
	id, err := parsePathUUID(r, "id", "account")
	if err != nil {
		return nil
	}
	var acc models.MessengerAccount
	if err := a.DB.Where("id = ? AND organization_id = ?", id, orgID).First(&acc).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Messenger account not found", nil, "")
	}
	var req messengerAccountRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	acc.Name = firstNonEmpty(req.Name, acc.Name)
	acc.PageID = firstNonEmpty(req.PageID, acc.PageID)
	acc.PageName = req.PageName
	acc.APIVersion = firstNonEmpty(req.APIVersion, acc.APIVersion)
	acc.WebhookVerifyToken = firstNonEmpty(req.WebhookVerifyToken, acc.WebhookVerifyToken)
	if req.IsActive != nil {
		acc.IsActive = *req.IsActive
	}
	if req.AccessToken != "" {
		enc, e := crypto.Encrypt(req.AccessToken, a.Config.App.EncryptionKey)
		if e != nil {
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "failed to save token", nil, "")
		}
		acc.AccessToken = enc
	}
	if req.AppSecret != "" {
		enc, e := crypto.Encrypt(req.AppSecret, a.Config.App.EncryptionKey)
		if e != nil {
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "failed to save app secret", nil, "")
		}
		acc.AppSecret = enc
	}
	if err := a.DB.Save(&acc).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "failed to update Messenger account", nil, "")
	}
	return r.SendEnvelope(messengerAccountToResponse(acc))
}

// DeleteMessengerAccount — DELETE /api/messenger/accounts/{id}
func (a *App) DeleteMessengerAccount(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceAccounts, models.ActionWrite)
	if err != nil {
		return nil
	}
	id, err := parsePathUUID(r, "id", "account")
	if err != nil {
		return nil
	}
	if err := a.DB.Where("id = ? AND organization_id = ?", id, orgID).Delete(&models.MessengerAccount{}).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "failed to delete Messenger account", nil, "")
	}
	return r.SendEnvelope(map[string]any{"ok": true})
}
