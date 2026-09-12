package handlers

// TRT custom patch #63: Ameex courier integration. Per-space credentials on the
// WhatsAppAccount (AmeexApiID / AmeexApiKey, key encrypted). A test_ key hits the
// Ameex sandbox automatically; the live key flips to production — same URLs.
//
// Flow: save a converted client as a contact (with address/city/COD) -> "Send to
// Ameex" creates a SIMPLE parcel and stores the returned parcel code -> Ameex
// pushes status changes to our webhook (HMAC-verified) and we update the contact.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

const ameexBaseURL = "https://api.ameex.app/customer"

// ameexCreds returns the decrypted Api-Id / Api-Key for an account, or an error
// when Ameex isn't configured/enabled for the space.
func (a *App) ameexCreds(acc *models.WhatsAppAccount) (id, key string, err error) {
	if acc == nil || !acc.AmeexEnabled || acc.AmeexApiID == "" || acc.AmeexApiKey == "" {
		return "", "", fmt.Errorf("ameex not configured for this number")
	}
	key, err = crypto.Decrypt(acc.AmeexApiKey, a.Config.App.EncryptionKey)
	if err != nil {
		return "", "", fmt.Errorf("failed to read Ameex key")
	}
	return acc.AmeexApiID, key, nil
}

// ameexRequest performs an Ameex API call with the auth headers. `form` non-nil
// sends an x-www-form-urlencoded POST; nil `form` is a GET. Returns the raw body.
func (a *App) ameexRequest(method, path, apiID, apiKey string, form url.Values) ([]byte, int, error) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, ameexBaseURL+path, body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("C-Api-Id", apiID)
	req.Header.Set("C-Api-Key", apiKey)
	req.Header.Set("Accept", "application/json")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := a.HTTPClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return raw, resp.StatusCode, nil
}

// digAny walks a decoded JSON payload for the first present key (case-insensitive),
// searching nested "data"/"result" wrappers. Ameex response shapes vary, so we're
// defensive rather than binding a fixed struct.
func digAny(v any, keys ...string) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			for _, want := range keys {
				if strings.EqualFold(k, want) {
					return val
				}
			}
		}
		for _, wrap := range []string{"data", "result", "parcel", "colis"} {
			if inner, ok := t[wrap]; ok {
				if found := digAny(inner, keys...); found != nil {
					return found
				}
			}
		}
	}
	return nil
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	}
	return ""
}

// ---- Cities (for the address form dropdown + connection test) ---------------

type ameexCity struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// GetAmeexCities proxies GET /Delivery/Cities for a number so the frontend can
// offer a valid city id. GET /api/accounts/{id}/ameex/cities
func (a *App) GetAmeexCities(r *fastglue.Request) error {
	orgID, userID, err := a.getOrgAndUserID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	if !a.HasPermission(userID, models.ResourceContacts, models.ActionRead, orgID) {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "No permission", nil, "")
	}
	acc, err := a.resolveAmeexAccount(r, orgID)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, err.Error(), nil, "")
	}
	id, key, err := a.ameexCreds(acc)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, err.Error(), nil, "")
	}
	raw, status, err := a.ameexRequest(http.MethodGet, "/Delivery/Cities", id, key, nil)
	if err != nil || status >= 400 {
		a.Log.Error("ameex cities failed", "status", status, "err", err, "body", string(raw))
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway, "Ameex cities request failed", nil, "")
	}
	cities := parseAmeexCities(raw)
	a.Log.Info("ameex: cities fetched", "account", acc.Name, "count", len(cities), "status", status,
		"sandbox", strings.HasPrefix(key, "test_"))
	if len(cities) == 0 {
		snippet := raw
		if len(snippet) > 600 {
			snippet = snippet[:600]
		}
		a.Log.Warn("ameex: cities empty — check response shape", "body", string(snippet))
	}
	return r.SendEnvelope(map[string]any{"cities": cities})
}

func parseAmeexCities(raw []byte) []ameexCity {
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil
	}
	// Accept either a bare array or a wrapper containing one.
	var arr []any
	switch t := decoded.(type) {
	case []any:
		arr = t
	case map[string]any:
		for _, wrap := range []string{"data", "cities", "result", "villes"} {
			if inner, ok := t[wrap].([]any); ok {
				arr = inner
				break
			}
		}
	}
	out := make([]ameexCity, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		idStr := asString(digAny(m, "id", "city_id", "ville_id"))
		id, _ := strconv.Atoi(idStr)
		name := asString(digAny(m, "name", "city", "ville", "label"))
		if id != 0 && name != "" {
			out = append(out, ameexCity{ID: id, Name: name})
		}
	}
	return out
}

// resolveAmeexAccount picks the account from ?account=Name or the space's default.
func (a *App) resolveAmeexAccount(r *fastglue.Request, orgID uuid.UUID) (*models.WhatsAppAccount, error) {
	name := string(r.RequestCtx.QueryArgs().Peek("account"))
	var acc models.WhatsAppAccount
	q := a.DB.Where("organization_id = ?", orgID)
	if name != "" {
		q = q.Where("name = ?", name)
	} else {
		q = q.Where("ameex_enabled = ?", true).Order("is_default_outgoing DESC")
	}
	if err := q.First(&acc).Error; err != nil {
		return nil, fmt.Errorf("no Ameex-enabled number found")
	}
	return &acc, nil
}

// ---- Create parcel ----------------------------------------------------------

// SendContactToAmeex creates a SIMPLE parcel for a saved contact.
// POST /api/contacts/{id}/ameex/send   body: { account?, product?, comment?, order_num? }
func (a *App) SendContactToAmeex(r *fastglue.Request) error {
	orgID, userID, err := a.getOrgAndUserID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	if !a.HasPermission(userID, models.ResourceContacts, models.ActionWrite, orgID) {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "No permission", nil, "")
	}
	contactID, err := parsePathUUID(r, "id", "contact")
	if err != nil {
		return nil
	}
	contact, err := findByIDAndOrg[models.Contact](a.DB, r, contactID, orgID, "Contact")
	if err != nil {
		return nil
	}
	a.Log.Info("ameex: send requested", "contact_id", contact.ID, "city_id", contact.AmeexCityID,
		"city", contact.City, "cod", contact.ConversionValue)

	var req struct {
		Account  string `json:"account"`
		Product  string `json:"product"`
		Comment  string `json:"comment"`
		OrderNum string `json:"order_num"`
	}
	_ = a.decodeRequest(r, &req)

	// Pick the account: request override, contact's number, or the default Ameex one.
	var acc models.WhatsAppAccount
	accName := firstNonEmpty(req.Account, contact.WhatsAppAccount)
	q := a.DB.Where("organization_id = ?", orgID)
	if accName != "" {
		q = q.Where("name = ?", accName)
	} else {
		q = q.Where("ameex_enabled = ?", true).Order("is_default_outgoing DESC")
	}
	if err := q.First(&acc).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "No Ameex-enabled number for this contact", nil, "")
	}
	apiID, apiKey, err := a.ameexCreds(&acc)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, err.Error(), nil, "")
	}

	// Validate the fields Ameex requires.
	receiver := firstNonEmpty(contact.ProfileName, contact.PhoneNumber)
	phone := ameexLocalPhone(contact.PhoneNumber)
	if len(phone) < 9 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Contact phone must have at least 9 digits", nil, "")
	}
	// Resolve the delivery city id. Prefer the stored numeric id (picked from the
	// dropdown); otherwise match the free-typed city name against Ameex's list so
	// agents who typed the city instead of selecting it can still send.
	cityID := contact.AmeexCityID
	if cityID <= 0 && strings.TrimSpace(contact.City) != "" {
		if raw, status, cerr := a.ameexRequest(http.MethodGet, "/Delivery/Cities", apiID, apiKey, nil); cerr == nil && status >= 200 && status < 300 {
			want := strings.ToLower(strings.TrimSpace(contact.City))
			for _, c := range parseAmeexCities(raw) {
				if strings.ToLower(strings.TrimSpace(c.Name)) == want {
					cityID = c.ID
					break
				}
			}
		} else {
			a.Log.Error("ameex: city lookup failed", "status", status, "err", cerr)
		}
		if cityID > 0 {
			// Persist so the dropdown shows it next time and we skip the lookup.
			a.DB.Model(&models.Contact{}).Where("id = ?", contact.ID).Update("ameex_city_id", cityID)
			contact.AmeexCityID = cityID
		}
	}
	if cityID <= 0 {
		a.Log.Warn("ameex: no city id for contact", "contact_id", contact.ID, "city", contact.City)
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Couldn't match the delivery city \""+contact.City+"\" to an Ameex city — pick it from the city dropdown in Add contact.", nil, "")
	}
	if contact.ConversionValue <= 0 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Order value (COD) is required", nil, "")
	}

	product := req.Product
	if product == "" && contact.ConversionQuantity > 0 {
		product = fmt.Sprintf("x%d", contact.ConversionQuantity)
	}
	orderNum := firstNonEmpty(req.OrderNum, contact.ID.String()[:8])

	form := url.Values{}
	form.Set("type", "SIMPLE")
	form.Set("receiver", receiver)
	form.Set("phone", phone)
	form.Set("city", strconv.Itoa(cityID))
	form.Set("cod", strconv.FormatFloat(contact.ConversionValue, 'f', -1, 64))
	if contact.Address != "" {
		form.Set("address", contact.Address)
	}
	if product != "" {
		form.Set("product", product)
	}
	if req.Comment != "" {
		form.Set("comment", req.Comment)
	}
	form.Set("order_num", orderNum)

	a.Log.Info("ameex: creating parcel", "contact_id", contact.ID, "account", acc.Name,
		"city_id", cityID, "cod", contact.ConversionValue, "phone", phone,
		"sandbox", strings.HasPrefix(apiKey, "test_"))
	raw, status, err := a.ameexRequest(http.MethodPost, "/Delivery/Parcels/Action/Type/Add", apiID, apiKey, form)
	if err != nil || status >= 400 {
		a.Log.Error("ameex create parcel failed", "status", status, "err", err, "body", string(raw))
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway, "Ameex refused the parcel: "+ameexErr(raw), nil, "")
	}

	var decoded any
	_ = json.Unmarshal(raw, &decoded)
	code := asString(digAny(decoded, "code", "parcel_code", "colis", "tracking", "parcelcode"))
	if code == "" {
		a.Log.Error("ameex parcel created but no code in response", "body", string(raw))
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway, "Ameex accepted the parcel but returned no code", nil, "")
	}

	a.Log.Info("ameex: parcel created", "contact_id", contact.ID, "parcel_code", code,
		"sandbox", strings.HasPrefix(apiKey, "test_"))
	now := time.Now()
	updates := map[string]any{
		"ameex_parcel_code": code,
		"ameex_status":      "CREATED",
		"ameex_status_name": "Créé",
		"ameex_sent_at":     &now,
	}
	if err := a.DB.Model(&models.Contact{}).Where("id = ?", contact.ID).Updates(updates).Error; err != nil {
		a.Log.Error("ameex: failed to persist parcel code", "err", err)
	}
	return r.SendEnvelope(map[string]any{"parcel_code": code, "status": "CREATED"})
}

func ameexErr(raw []byte) string {
	var decoded any
	if json.Unmarshal(raw, &decoded) == nil {
		if m := asString(digAny(decoded, "message", "error", "msg", "detail")); m != "" {
			return m
		}
	}
	s := strings.TrimSpace(string(raw))
	if len(s) > 200 {
		s = s[:200]
	}
	if s == "" {
		return "unknown error"
	}
	return s
}

// ameexLocalPhone converts an E.164/stored number to the local 9+ digit form
// Ameex expects (Morocco: strip the 212 country code, keep 9 digits).
func ameexLocalPhone(p string) string {
	d := digitsOnly(p)
	d = strings.TrimPrefix(d, "00")
	if strings.HasPrefix(d, "212") {
		d = d[3:]
	}
	d = strings.TrimPrefix(d, "0")
	return d
}

// ---- Tracking ---------------------------------------------------------------

// TrackContactParcel returns live tracking for a contact's parcel.
// GET /api/contacts/{id}/ameex/tracking
func (a *App) TrackContactParcel(r *fastglue.Request) error {
	orgID, userID, err := a.getOrgAndUserID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	if !a.HasPermission(userID, models.ResourceContacts, models.ActionRead, orgID) {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "No permission", nil, "")
	}
	contactID, err := parsePathUUID(r, "id", "contact")
	if err != nil {
		return nil
	}
	contact, err := findByIDAndOrg[models.Contact](a.DB, r, contactID, orgID, "Contact")
	if err != nil {
		return nil
	}
	if contact.AmeexParcelCode == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "This contact has no Ameex parcel", nil, "")
	}
	var acc models.WhatsAppAccount
	if err := a.DB.Where("organization_id = ? AND name = ?", orgID, contact.WhatsAppAccount).First(&acc).Error; err != nil {
		_ = a.DB.Where("organization_id = ? AND ameex_enabled = ?", orgID, true).First(&acc).Error
	}
	apiID, apiKey, err := a.ameexCreds(&acc)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, err.Error(), nil, "")
	}
	raw, status, err := a.ameexRequest(http.MethodGet, "/Delivery/Parcels/Tracking/ParcelCode/"+url.PathEscape(contact.AmeexParcelCode), apiID, apiKey, nil)
	if err != nil || status >= 400 {
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway, "Ameex tracking failed", nil, "")
	}
	var decoded any
	_ = json.Unmarshal(raw, &decoded)
	return r.SendEnvelope(map[string]any{"tracking": decoded, "parcel_code": contact.AmeexParcelCode})
}

// ---- Webhook (public, HMAC-verified) ---------------------------------------

// AmeexWebhook receives status changes from Ameex (form-encoded). Verifies the
// X-Ameex-Signature header against the space's webhook secret when one is set.
// POST /api/ameex/webhook   (public route)
func (a *App) AmeexWebhook(r *fastglue.Request) error {
	rawBody := r.RequestCtx.PostBody()
	code := string(r.RequestCtx.FormValue("CODE"))
	statut := string(r.RequestCtx.FormValue("STATUT"))
	statutName := string(r.RequestCtx.FormValue("STATUT_NAME"))
	if code == "" {
		return r.SendEnvelope(map[string]any{"ok": true}) // nothing to do
	}

	// Find the contact (and its number) by parcel code.
	var contact models.Contact
	if err := a.DB.Where("ameex_parcel_code = ?", code).First(&contact).Error; err != nil {
		a.Log.Warn("ameex webhook: unknown parcel", "code", code)
		return r.SendEnvelope(map[string]any{"ok": true})
	}

	// Verify signature when the space configured a secret.
	var acc models.WhatsAppAccount
	if a.DB.Where("organization_id = ? AND name = ?", contact.OrganizationID, contact.WhatsAppAccount).First(&acc).Error == nil {
		if acc.AmeexWebhookSecret != "" {
			secret, derr := crypto.Decrypt(acc.AmeexWebhookSecret, a.Config.App.EncryptionKey)
			if derr == nil && !ameexVerifySignature(string(r.RequestCtx.Request.Header.Peek("X-Ameex-Signature")), rawBody, secret) {
				a.Log.Warn("ameex webhook: bad signature", "code", code)
				return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "bad signature", nil, "")
			}
		}
	}

	if err := a.DB.Model(&models.Contact{}).Where("id = ?", contact.ID).
		Updates(map[string]any{"ameex_status": statut, "ameex_status_name": statutName}).Error; err != nil {
		a.Log.Error("ameex webhook: update failed", "err", err)
	}
	return r.SendEnvelope(map[string]any{"ok": true})
}

// ameexVerifySignature checks "t=<ts>,v1=<hmac>" = HMAC-SHA256(ts + "." + rawBody).
func ameexVerifySignature(header string, rawBody []byte, secret string) bool {
	var ts, v1 string
	for _, part := range strings.Split(header, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			ts = kv[1]
		case "v1":
			v1 = kv[1]
		}
	}
	if ts == "" || v1 == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "." + string(rawBody)))
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(v1))
}

// ---- Dedicated Ameex settings save (doesn't touch other account settings) ----

// UpdateAmeexSettings updates ONLY the Ameex fields on a number, so saving Ameex
// can't clobber the Meta/webhook settings by mistake. Empty key/secret = keep current.
// PUT /api/accounts/{id}/ameex
func (a *App) UpdateAmeexSettings(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceAccounts, models.ActionWrite)
	if err != nil {
		return nil
	}
	id, err := parsePathUUID(r, "id", "account")
	if err != nil {
		return nil
	}
	account, err := a.resolveWhatsAppAccountByID(r, id, orgID)
	if err != nil {
		return nil
	}
	var req struct {
		AmeexEnabled       bool   `json:"ameex_enabled"`
		AmeexApiID         string `json:"ameex_api_id"`
		AmeexApiKey        string `json:"ameex_api_key"`
		AmeexWebhookSecret string `json:"ameex_webhook_secret"`
	}
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	account.AmeexEnabled = req.AmeexEnabled
	account.AmeexApiID = req.AmeexApiID
	if req.AmeexApiKey != "" {
		enc, e := crypto.Encrypt(req.AmeexApiKey, a.Config.App.EncryptionKey)
		if e != nil {
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save Ameex key", nil, "")
		}
		account.AmeexApiKey = enc
	}
	if req.AmeexWebhookSecret != "" {
		enc, e := crypto.Encrypt(req.AmeexWebhookSecret, a.Config.App.EncryptionKey)
		if e != nil {
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save Ameex webhook secret", nil, "")
		}
		account.AmeexWebhookSecret = enc
	}
	if err := a.DB.Save(account).Error; err != nil {
		a.Log.Error("ameex: failed to save settings", "err", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save Ameex settings", nil, "")
	}
	return r.SendEnvelope(accountToResponse(*account))
}
