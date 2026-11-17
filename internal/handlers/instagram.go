package handlers

// TRT custom patch #68: Instagram Direct integration (MVP — receive text/image,
// reply with text). Instagram runs on Meta's Messenger Platform: inbound DMs
// arrive on a webhook (object "instagram") keyed by the IG professional account
// id; we upsert the contact by its Instagram-scoped id (IGSID) and store the
// message on the same contacts/messages tables (Channel=instagram) so it shows in
// the normal inbox. Sending uses POST /{ig_user_id}/messages with the page token.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

const igGraphBase = "https://graph.facebook.com"

func igAPIVersion(acc *models.InstagramAccount) string {
	if acc != nil && acc.APIVersion != "" {
		return acc.APIVersion
	}
	return "v21.0"
}

// igToken returns the decrypted page/IG access token.
func (a *App) igToken(acc *models.InstagramAccount) (string, error) {
	if acc == nil || !acc.IsActive || acc.AccessToken == "" {
		return "", fmt.Errorf("instagram not configured for this account")
	}
	return crypto.Decrypt(acc.AccessToken, a.Config.App.EncryptionKey)
}

// igSendText sends a plain-text DM to an IGSID via the Messenger Platform.
func (a *App) igSendText(acc *models.InstagramAccount, igsid, text string) (string, error) {
	token, err := a.igToken(acc)
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"recipient": map[string]any{"id": igsid},
		"message":   map[string]any{"text": text},
	}
	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("%s/%s/%s/messages?access_token=%s", igGraphBase, igAPIVersion(acc), acc.IGUserID, token)
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
		return "", fmt.Errorf("instagram send failed (%d): %s", resp.StatusCode, string(raw))
	}
	var decoded map[string]any
	_ = json.Unmarshal(raw, &decoded)
	if mid, ok := decoded["message_id"].(string); ok {
		return mid, nil
	}
	return "", nil
}

// igSendAttachment sends a media attachment (by public URL) to an IGSID. TRT #76.
func (a *App) igSendAttachment(acc *models.InstagramAccount, igsid, attType, mediaURL string) (string, error) {
	token, err := a.igToken(acc)
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"recipient": map[string]any{"id": igsid},
		"message": map[string]any{
			"attachment": map[string]any{
				"type":    attType,
				"payload": map[string]any{"url": mediaURL, "is_reusable": true},
			},
		},
	}
	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("%s/%s/%s/messages?access_token=%s", igGraphBase, igAPIVersion(acc), acc.IGUserID, token)
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
		return "", fmt.Errorf("instagram media send failed (%d): %s", resp.StatusCode, string(raw))
	}
	var decoded map[string]any
	_ = json.Unmarshal(raw, &decoded)
	if mid, ok := decoded["message_id"].(string); ok {
		return mid, nil
	}
	return "", nil
}

// ---- Webhook ----------------------------------------------------------------

// InstagramWebhookVerify handles the GET verification handshake. Meta sends
// hub.mode=subscribe, hub.verify_token, hub.challenge — echo the challenge when
// the token matches any configured IG account. GET /api/instagram/webhook
func (a *App) InstagramWebhookVerify(r *fastglue.Request) error {
	mode := string(r.RequestCtx.QueryArgs().Peek("hub.mode"))
	token := string(r.RequestCtx.QueryArgs().Peek("hub.verify_token"))
	challenge := string(r.RequestCtx.QueryArgs().Peek("hub.challenge"))
	if mode != "subscribe" || token == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "bad verify request", nil, "")
	}
	var count int64
	a.DB.Model(&models.InstagramAccount{}).Where("webhook_verify_token = ?", token).Count(&count)
	if count == 0 {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "verify token mismatch", nil, "")
	}
	r.RequestCtx.SetStatusCode(fasthttp.StatusOK)
	r.RequestCtx.SetBodyString(challenge)
	return nil
}

type igWebhookPayload struct {
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
		// TRT #77: comments on the account's media arrive as changes (field="comments").
		Changes []struct {
			Field string `json:"field"`
			Value struct {
				ID   string `json:"id"` // comment id
				Text string `json:"text"`
				From struct {
					ID       string `json:"id"`
					Username string `json:"username"`
				} `json:"from"`
				Media struct {
					ID              string `json:"id"`
					MediaProductType string `json:"media_product_type"`
				} `json:"media"`
				ParentID string `json:"parent_id"`
			} `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}

// InstagramWebhook receives inbound IG messaging events. POST /api/instagram/webhook (public)
func (a *App) InstagramWebhook(r *fastglue.Request) error {
	rawBody := r.RequestCtx.PostBody()
	// TRT #79 diagnostic: log the raw body so we can see exactly what Instagram
	// sends for DMs vs comments (structure/object), to pinpoint non-delivery.
	{
		bs := string(rawBody)
		if len(bs) > 2000 {
			bs = bs[:2000]
		}
		a.Log.Info("IG webhook RAW", "len", len(rawBody), "body", bs)
	}
	var payload igWebhookPayload
	if err := json.Unmarshal(rawBody, &payload); err != nil {
		return r.SendEnvelope(map[string]any{"ok": true})
	}
	if payload.Object != "instagram" {
		return r.SendEnvelope(map[string]any{"ok": true})
	}

	for _, entry := range payload.Entry {
		for _, m := range entry.Messaging {
			if m.Message == nil || m.Message.IsEcho {
				continue // skip echoes of our own sends and non-message events
			}
			// recipient.id is our IG account; fall back to entry.id.
			igUserID := m.Recipient.ID
			if igUserID == "" {
				igUserID = entry.ID
			}
			var acc models.InstagramAccount
			if err := a.DB.Where("ig_user_id = ?", igUserID).First(&acc).Error; err != nil {
				a.Log.Warn("instagram webhook: unknown account", "ig_user_id", igUserID)
				continue
			}
			// Verify signature when an app secret is configured.
			if acc.AppSecret != "" {
				if secret, derr := crypto.Decrypt(acc.AppSecret, a.Config.App.EncryptionKey); derr == nil {
					sig := string(r.RequestCtx.Request.Header.Peek("X-Hub-Signature-256"))
					if !igVerifySignature(sig, rawBody, secret) {
						a.Log.Warn("instagram webhook: bad signature", "ig_user_id", igUserID)
						continue
					}
				}
			}
			a.handleInstagramInbound(&acc, m.Sender.ID, m.Message.MID, m.Message.Text, m.Message.Attachments)
		}

		// TRT #77: comments on the account's media (field="comments"). The account is
		// identified by entry.id (the IG professional account that owns the media).
		for _, ch := range entry.Changes {
			if ch.Field != "comments" {
				continue
			}
			v := ch.Value
			if v.ID == "" || v.From.ID == "" {
				continue
			}
			var acc models.InstagramAccount
			if err := a.DB.Where("ig_user_id = ?", entry.ID).First(&acc).Error; err != nil {
				a.Log.Warn("instagram webhook: unknown account for comment", "ig_user_id", entry.ID)
				continue
			}
			// Don't ingest the account's own replies as new comments.
			if v.From.ID == acc.IGUserID {
				continue
			}
			if acc.AppSecret != "" {
				if secret, derr := crypto.Decrypt(acc.AppSecret, a.Config.App.EncryptionKey); derr == nil {
					sig := string(r.RequestCtx.Request.Header.Peek("X-Hub-Signature-256"))
					if !igVerifySignature(sig, rawBody, secret) {
						a.Log.Warn("instagram webhook: bad signature (comment)", "ig_user_id", entry.ID)
						continue
					}
				}
			}
			a.handleInstagramComment(&acc, v.ID, v.Text, v.From.ID, v.From.Username, v.Media.ID)
		}
	}
	return r.SendEnvelope(map[string]any{"ok": true})
}

// handleInstagramInbound upserts the contact (by IGSID) and stores the message.
func (a *App) handleInstagramInbound(acc *models.InstagramAccount, igsid, mid, text string, attachments []struct {
	Type    string `json:"type"`
	Payload struct {
		URL string `json:"url"`
	} `json:"payload"`
}) {
	if igsid == "" {
		return
	}
	// Upsert contact by (org, channel=instagram, external_id=IGSID).
	var contact models.Contact
	err := a.DB.Where("organization_id = ? AND channel = ? AND external_id = ?",
		acc.OrganizationID, models.ChannelInstagram, igsid).First(&contact).Error
	now := time.Now()
	if err != nil {
		contact = models.Contact{
			BaseModel:       models.BaseModel{ID: uuid.New()},
			OrganizationID:  acc.OrganizationID,
			Channel:         models.ChannelInstagram,
			ExternalID:      igsid,
			ProfileName:     a.igFetchUsername(acc, igsid),
			WhatsAppAccount: acc.Name, // group under the IG account in the inbox
			LastInboundAt:   &now,
		}
		if err := a.DB.Create(&contact).Error; err != nil {
			a.Log.Error("instagram: failed to create contact", "err", err)
			return
		}
	}

	messageType := models.MessageTypeText
	content := text
	mediaURL := ""
	mediaMime := ""
	if len(attachments) > 0 {
		at := attachments[0]
		mediaURL = at.Payload.URL
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
	}

	message := models.Message{
		BaseModel:         models.BaseModel{ID: uuid.New()},
		OrganizationID:    acc.OrganizationID,
		WhatsAppAccount:   acc.Name,
		Channel:           models.ChannelInstagram,
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
		a.Log.Error("instagram: failed to save message", "err", err)
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
	a.Log.Info("instagram: inbound message", "account", acc.Name, "igsid", igsid, "type", messageType)
}

// igFetchUsername best-effort fetches the sender's IG username (non-fatal).
func (a *App) igFetchUsername(acc *models.InstagramAccount, igsid string) string {
	token, err := a.igToken(acc)
	if err != nil {
		return ""
	}
	url := fmt.Sprintf("%s/%s/%s?fields=username,name&access_token=%s", igGraphBase, igAPIVersion(acc), igsid, token)
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	resp, err := a.HTTPClient.Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var d map[string]any
	_ = json.Unmarshal(raw, &d)
	if u, ok := d["username"].(string); ok && u != "" {
		return u
	}
	if n, ok := d["name"].(string); ok {
		return n
	}
	return ""
}

// igVerifySignature checks X-Hub-Signature-256: sha256=<hmac(body, appSecret)>.
func igVerifySignature(header string, body []byte, secret string) bool {
	parts := strings.SplitN(header, "=", 2)
	if len(parts) != 2 || parts[0] != "sha256" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(parts[1]))
}

// sendInstagramReply sends an agent's text reply to an IG contact and records it.
// Used by the SendMessage handler when contact.Channel == instagram.
func (a *App) sendInstagramReply(contact *models.Contact, text string, sentByUserID *uuid.UUID) (*models.Message, error) {
	var acc models.InstagramAccount
	q := a.DB.Where("organization_id = ?", contact.OrganizationID)
	if contact.WhatsAppAccount != "" {
		q = q.Where("name = ?", contact.WhatsAppAccount)
	}
	if err := q.First(&acc).Error; err != nil {
		if err := a.DB.Where("organization_id = ? AND is_active = ?", contact.OrganizationID, true).First(&acc).Error; err != nil {
			return nil, fmt.Errorf("no Instagram account for this contact")
		}
	}

	// TRT #84: let the normal composer reply to a comment as a DM. If the contact's
	// latest inbound was a comment and we haven't opened a DM yet, send via the
	// comment Private-Reply API (recipient{comment_id}) — a plain DM send needs an
	// open 24h window a comment-only lead doesn't have. Private reply is allowed once
	// per comment, so only for the FIRST outgoing message; after that the DM is open.
	var commentReplyID string
	{
		var lastIn models.Message
		if e := a.DB.Where("contact_id = ? AND direction = ?", contact.ID, models.DirectionIncoming).
			Order("created_at DESC").First(&lastIn).Error; e == nil {
			if cid, _ := lastIn.Metadata["comment_id"].(string); cid != "" {
				if isC, _ := lastIn.Metadata["ig_comment"].(bool); isC {
					var priorOut int64
					a.DB.Model(&models.Message{}).
						Where("contact_id = ? AND direction = ?", contact.ID, models.DirectionOutgoing).
						Count(&priorOut)
					if priorOut == 0 {
						commentReplyID = cid
					}
				}
			}
		}
	}

	msg := &models.Message{
		BaseModel:       models.BaseModel{ID: uuid.New()},
		OrganizationID:  contact.OrganizationID,
		WhatsAppAccount: acc.Name,
		Channel:         models.ChannelInstagram,
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

	var mid string
	var err error
	if commentReplyID != "" {
		mid, err = a.igCommentPrivateReply(&acc, commentReplyID, text)
		if err != nil {
			// A comment accepts only ONE private reply ("already has a reply"), and the
			// DM thread is open once that first reply exists — so fall back to a normal
			// DM send instead of failing with a 502.
			a.Log.Warn("instagram: comment private reply failed, falling back to DM send", "error", err, "contact", contact.ID)
			mid, err = a.igSendText(&acc, contact.ExternalID, text)
		}
	} else {
		mid, err = a.igSendText(&acc, contact.ExternalID, text)
	}
	now := time.Now()
	if err != nil {
		a.Log.Error("instagram: reply failed", "error", err, "contact", contact.ID, "via_comment", commentReplyID != "")
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
	return msg, nil
}

// sendInstagramMediaReply sends an agent's media (image/video/audio/file) reply to
// an IG contact by public URL and records it. TRT #76. Any caption is sent as a
// follow-up text message (IG attachments carry no caption field).
func (a *App) sendInstagramMediaReply(contact *models.Contact, relPath, publicURL, mimeType, caption string, sentByUserID *uuid.UUID) (*models.Message, error) {
	var acc models.InstagramAccount
	q := a.DB.Where("organization_id = ?", contact.OrganizationID)
	if contact.WhatsAppAccount != "" {
		q = q.Where("name = ?", contact.WhatsAppAccount)
	}
	if err := q.First(&acc).Error; err != nil {
		if err := a.DB.Where("organization_id = ? AND is_active = ?", contact.OrganizationID, true).First(&acc).Error; err != nil {
			return nil, fmt.Errorf("no Instagram account for this contact")
		}
	}

	msgType := messageTypeFromMime(mimeType)
	msg := &models.Message{
		BaseModel:       models.BaseModel{ID: uuid.New()},
		OrganizationID:  contact.OrganizationID,
		WhatsAppAccount: acc.Name,
		Channel:         models.ChannelInstagram,
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

	mid, err := a.igSendAttachment(&acc, contact.ExternalID, attachmentTypeFromMime(mimeType), publicURL)
	now := time.Now()
	if err != nil {
		a.DB.Model(msg).Updates(map[string]any{"status": models.MessageStatusFailed, "error_message": err.Error()})
		return nil, err
	}
	a.DB.Model(msg).Updates(map[string]any{"status": models.MessageStatusSent, "whats_app_message_id": mid})
	msg.Status = models.MessageStatusSent
	msg.WhatsAppMessageID = mid

	if strings.TrimSpace(caption) != "" {
		_, _ = a.igSendText(&acc, contact.ExternalID, caption)
	}

	a.DB.Model(contact).Updates(map[string]any{"last_message_at": now, "last_message_preview": "[" + string(msgType) + "]", "is_read": true})
	a.broadcastNewMessage(contact.OrganizationID, msg, contact)
	return msg, nil
}

// handleInstagramComment upserts the commenter as a contact and stores the
// comment as an incoming message (tagged in metadata so the UI shows it as a
// comment and can reply publicly or via DM). TRT #77.
func (a *App) handleInstagramComment(acc *models.InstagramAccount, commentID, text, fromID, username, mediaID string) {
	var contact models.Contact
	err := a.DB.Where("organization_id = ? AND channel = ? AND external_id = ?",
		acc.OrganizationID, models.ChannelInstagram, fromID).First(&contact).Error
	now := time.Now()
	if err != nil {
		contact = models.Contact{
			BaseModel:       models.BaseModel{ID: uuid.New()},
			OrganizationID:  acc.OrganizationID,
			Channel:         models.ChannelInstagram,
			ExternalID:      fromID,
			ProfileName:     firstNonEmpty(username, a.igFetchUsername(acc, fromID)),
			WhatsAppAccount: acc.Name,
			LastInboundAt:   &now,
		}
		if e := a.DB.Create(&contact).Error; e != nil {
			a.Log.Error("instagram: failed to create contact (comment)", "err", e)
			return
		}
	}

	message := models.Message{
		BaseModel:         models.BaseModel{ID: uuid.New()},
		OrganizationID:    acc.OrganizationID,
		WhatsAppAccount:   acc.Name,
		Channel:           models.ChannelInstagram,
		ContactID:         contact.ID,
		WhatsAppMessageID: commentID,
		Direction:         models.DirectionIncoming,
		MessageType:       models.MessageTypeText,
		Content:           text,
		Status:            models.MessageStatusDelivered,
		Metadata:          models.JSONB{"ig_comment": true, "comment_id": commentID, "media_id": mediaID, "username": username},
	}
	if e := a.DB.Create(&message).Error; e != nil {
		a.Log.Error("instagram: failed to save comment", "err", e)
		return
	}

	preview := "💬 " + text
	if len(preview) > 100 {
		preview = preview[:97] + "..."
	}
	a.DB.Model(&contact).Updates(map[string]any{
		"last_message_at": now, "last_inbound_at": now, "last_message_preview": preview, "is_read": false,
	})
	a.broadcastNewMessage(acc.OrganizationID, &message, &contact)
	a.Log.Info("instagram: inbound comment", "account", acc.Name, "comment_id", commentID, "from", fromID)
}

// igCommentPublicReply posts a public reply under a comment. TRT #77.
func (a *App) igCommentPublicReply(acc *models.InstagramAccount, commentID, text string) (string, error) {
	token, err := a.igToken(acc)
	if err != nil {
		return "", err
	}
	form := "message=" + url.QueryEscape(text) + "&access_token=" + url.QueryEscape(token)
	endpoint := fmt.Sprintf("%s/%s/%s/replies", igGraphBase, igAPIVersion(acc), commentID)
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(form))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := a.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("instagram public reply failed (%d): %s", resp.StatusCode, string(raw))
	}
	var d map[string]any
	_ = json.Unmarshal(raw, &d)
	if id, ok := d["id"].(string); ok {
		return id, nil
	}
	return "", nil
}

// igCommentPrivateReply sends a private DM in response to a comment (Instagram
// Private Replies: recipient{comment_id}). TRT #77.
func (a *App) igCommentPrivateReply(acc *models.InstagramAccount, commentID, text string) (string, error) {
	token, err := a.igToken(acc)
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"recipient": map[string]any{"comment_id": commentID},
		"message":   map[string]any{"text": text},
	}
	body, _ := json.Marshal(payload)
	endpoint := fmt.Sprintf("%s/%s/%s/messages?access_token=%s", igGraphBase, igAPIVersion(acc), acc.IGUserID, url.QueryEscape(token))
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(body)))
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
		return "", fmt.Errorf("instagram private reply failed (%d): %s", resp.StatusCode, string(raw))
	}
	var d map[string]any
	_ = json.Unmarshal(raw, &d)
	if mid, ok := d["message_id"].(string); ok {
		return mid, nil
	}
	return "", nil
}

// SendInstagramCommentReply replies to a stored IG comment message, publicly
// (under the comment) or as a private DM. TRT #77.
// POST /api/messages/{id}/ig-comment-reply   body: { text, mode: "public"|"dm" }
func (a *App) SendInstagramCommentReply(r *fastglue.Request) error {
	orgID, userID, err := a.getOrgAndUserID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	if !a.HasPermission(userID, models.ResourceContacts, models.ActionWrite, orgID) {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "No permission", nil, "")
	}
	msgID, err := parsePathUUID(r, "id", "message")
	if err != nil {
		return nil
	}
	var src models.Message
	if err := a.DB.Where("id = ? AND organization_id = ?", msgID, orgID).First(&src).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Comment not found", nil, "")
	}
	commentID, _ := src.Metadata["comment_id"].(string)
	if commentID == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "This message is not an Instagram comment", nil, "")
	}
	var req struct {
		Text string `json:"text"`
		Mode string `json:"mode"` // "public" | "dm"
	}
	_ = a.decodeRequest(r, &req)
	if strings.TrimSpace(req.Text) == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Reply text is required", nil, "")
	}

	var acc models.InstagramAccount
	if err := a.DB.Where("organization_id = ? AND name = ?", orgID, src.WhatsAppAccount).First(&acc).Error; err != nil {
		if err := a.DB.Where("organization_id = ? AND is_active = ?", orgID, true).First(&acc).Error; err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "No Instagram account for this comment", nil, "")
		}
	}

	var contact models.Contact
	_ = a.DB.Where("id = ?", src.ContactID).First(&contact).Error

	if req.Mode == "public" {
		newID, serr := a.igCommentPublicReply(&acc, commentID, req.Text)
		if serr != nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadGateway, "Instagram: "+serr.Error(), nil, "")
		}
		out := models.Message{
			BaseModel:       models.BaseModel{ID: uuid.New()},
			OrganizationID:  orgID,
			WhatsAppAccount: acc.Name,
			Channel:         models.ChannelInstagram,
			ContactID:       src.ContactID,
			WhatsAppMessageID: newID,
			Direction:       models.DirectionOutgoing,
			MessageType:     models.MessageTypeText,
			Content:         req.Text,
			Status:          models.MessageStatusSent,
			SentByUserID:    &userID,
			Metadata:        models.JSONB{"ig_comment_reply": "public", "comment_id": commentID},
		}
		_ = a.DB.Create(&out).Error
		if contact.ID != uuid.Nil {
			a.broadcastNewMessage(orgID, &out, &contact)
		}
		return r.SendEnvelope(map[string]any{"ok": true, "mode": "public", "id": newID})
	}

	// Default: private DM reply (Private Replies API).
	mid, serr := a.igCommentPrivateReply(&acc, commentID, req.Text)
	if serr != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway, "Instagram: "+serr.Error(), nil, "")
	}
	out := models.Message{
		BaseModel:         models.BaseModel{ID: uuid.New()},
		OrganizationID:    orgID,
		WhatsAppAccount:   acc.Name,
		Channel:           models.ChannelInstagram,
		ContactID:         src.ContactID,
		WhatsAppMessageID: mid,
		Direction:         models.DirectionOutgoing,
		MessageType:       models.MessageTypeText,
		Content:           req.Text,
		Status:            models.MessageStatusSent,
		SentByUserID:      &userID,
	}
	_ = a.DB.Create(&out).Error
	if contact.ID != uuid.Nil {
		a.DB.Model(&contact).Updates(map[string]any{"last_message_at": time.Now(), "last_message_preview": req.Text, "is_read": true})
		a.broadcastNewMessage(orgID, &out, &contact)
	}
	return r.SendEnvelope(map[string]any{"ok": true, "mode": "dm", "id": mid})
}

// ---- Instagram account CRUD (settings) --------------------------------------

type instagramAccountResponse struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	IGUserID           string `json:"ig_user_id"`
	PageID             string `json:"page_id"`
	Username           string `json:"username"`
	APIVersion         string `json:"api_version"`
	WebhookVerifyToken string `json:"webhook_verify_token"`
	IsActive           bool   `json:"is_active"`
	HasAccessToken     bool   `json:"has_access_token"`
	HasAppSecret       bool   `json:"has_app_secret"`
}

func instagramAccountToResponse(acc models.InstagramAccount) instagramAccountResponse {
	return instagramAccountResponse{
		ID:                 acc.ID.String(),
		Name:               acc.Name,
		IGUserID:           acc.IGUserID,
		PageID:             acc.PageID,
		Username:           acc.Username,
		APIVersion:         acc.APIVersion,
		WebhookVerifyToken: acc.WebhookVerifyToken,
		IsActive:           acc.IsActive,
		HasAccessToken:     acc.AccessToken != "",
		HasAppSecret:       acc.AppSecret != "",
	}
}

// ListInstagramAccounts — GET /api/instagram/accounts
func (a *App) ListInstagramAccounts(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceAccounts, models.ActionRead)
	if err != nil {
		return nil
	}
	var accts []models.InstagramAccount
	a.DB.Where("organization_id = ?", orgID).Order("created_at").Find(&accts)
	out := make([]instagramAccountResponse, len(accts))
	for i, acc := range accts {
		out[i] = instagramAccountToResponse(acc)
	}
	return r.SendEnvelope(map[string]any{"accounts": out})
}

type instagramAccountRequest struct {
	Name               string `json:"name"`
	IGUserID           string `json:"ig_user_id"`
	PageID             string `json:"page_id"`
	Username           string `json:"username"`
	APIVersion         string `json:"api_version"`
	WebhookVerifyToken string `json:"webhook_verify_token"`
	IsActive           *bool  `json:"is_active"`
	AccessToken        string `json:"access_token"`
	AppSecret          string `json:"app_secret"`
}

// CreateInstagramAccount — POST /api/instagram/accounts
func (a *App) CreateInstagramAccount(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceAccounts, models.ActionWrite)
	if err != nil {
		return nil
	}
	var req instagramAccountRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.IGUserID) == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "name and ig_user_id are required", nil, "")
	}
	acc := models.InstagramAccount{
		BaseModel:          models.BaseModel{ID: uuid.New()},
		OrganizationID:     orgID,
		Name:               req.Name,
		IGUserID:           req.IGUserID,
		PageID:             req.PageID,
		Username:           req.Username,
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
		a.Log.Error("instagram: create account failed", "err", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "failed to create Instagram account", nil, "")
	}
	return r.SendEnvelope(instagramAccountToResponse(acc))
}

// UpdateInstagramAccount — PUT /api/instagram/accounts/{id}
func (a *App) UpdateInstagramAccount(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceAccounts, models.ActionWrite)
	if err != nil {
		return nil
	}
	id, err := parsePathUUID(r, "id", "account")
	if err != nil {
		return nil
	}
	var acc models.InstagramAccount
	if err := a.DB.Where("id = ? AND organization_id = ?", id, orgID).First(&acc).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Instagram account not found", nil, "")
	}
	var req instagramAccountRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	acc.Name = firstNonEmpty(req.Name, acc.Name)
	acc.IGUserID = firstNonEmpty(req.IGUserID, acc.IGUserID)
	acc.PageID = req.PageID
	acc.Username = req.Username
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
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "failed to update Instagram account", nil, "")
	}
	return r.SendEnvelope(instagramAccountToResponse(acc))
}

// DeleteInstagramAccount — DELETE /api/instagram/accounts/{id}
func (a *App) DeleteInstagramAccount(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceAccounts, models.ActionWrite)
	if err != nil {
		return nil
	}
	id, err := parsePathUUID(r, "id", "account")
	if err != nil {
		return nil
	}
	if err := a.DB.Where("id = ? AND organization_id = ?", id, orgID).Delete(&models.InstagramAccount{}).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "failed to delete Instagram account", nil, "")
	}
	return r.SendEnvelope(map[string]any{"ok": true})
}
