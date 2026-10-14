package handlers

// Read-only Ameex parcel report for dashboards (the Ameex customer API has no list
// or statistics endpoint, so the parcels we created and their webhook-updated status
// are the only source). Returns one row per contact that has a parcel: no phone
// numbers or names, just what is needed to aggregate orders / COD / cities / status.
//
// GET /api/ameex/parcels?from=YYYY-MM-DD&to=YYYY-MM-DD[&account=<whatsapp account name>]
// Auth: normal JWT or X-API-Key; needs contacts:read.

import (
	"time"

	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"

	"github.com/shridarpatil/whatomate/internal/models"
)

type ameexParcelRow struct {
	ContactID  string  `json:"contact_id"`
	Account    string  `json:"account"`
	Code       string  `json:"code"`
	Status     string  `json:"status"`
	StatusName string  `json:"status_name"`
	City       string  `json:"city"`
	COD        float64 `json:"cod"`
	Quantity   int     `json:"quantity"`
	SentAt     string  `json:"sent_at"`
}

func parseReportDate(s string, endOfDay bool) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		if endOfDay {
			t = t.Add(24*time.Hour - time.Second)
		}
		return t, true
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	return time.Time{}, false
}

func (a *App) AmeexParcelsReport(r *fastglue.Request) error {
	orgID, userID, err := a.getOrgAndUserID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	if !a.HasPermission(userID, models.ResourceContacts, models.ActionRead, orgID) {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "Insufficient permissions", nil, "")
	}

	q := r.RequestCtx.QueryArgs()
	from, hasFrom := parseReportDate(string(q.Peek("from")), false)
	to, hasTo := parseReportDate(string(q.Peek("to")), true)
	account := string(q.Peek("account"))
	if !hasFrom {
		from = time.Now().AddDate(0, 0, -30)
	}
	if !hasTo {
		to = time.Now()
	}

	query := a.DB.Model(&models.Contact{}).
		Where("organization_id = ? AND ameex_parcel_code <> '' AND ameex_sent_at >= ? AND ameex_sent_at <= ?", orgID, from, to)
	if account != "" {
		query = query.Where("whats_app_account = ?", account)
	}

	var contacts []models.Contact
	if err := query.Order("ameex_sent_at DESC").Limit(20000).
		Select("id", "whats_app_account", "ameex_parcel_code", "ameex_status", "ameex_status_name", "city", "conversion_value", "conversion_quantity", "ameex_sent_at").
		Find(&contacts).Error; err != nil {
		a.Log.Error("ameex report: query failed", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load parcels", nil, "")
	}

	rows := make([]ameexParcelRow, 0, len(contacts))
	for _, c := range contacts {
		sent := ""
		if c.AmeexSentAt != nil {
			sent = c.AmeexSentAt.UTC().Format(time.RFC3339)
		}
		rows = append(rows, ameexParcelRow{
			ContactID: c.ID.String(), Account: c.WhatsAppAccount, Code: c.AmeexParcelCode,
			Status: c.AmeexStatus, StatusName: c.AmeexStatusName, City: c.City,
			COD: c.ConversionValue, Quantity: c.ConversionQuantity, SentAt: sent,
		})
	}
	return r.SendEnvelope(map[string]any{"parcels": rows, "count": len(rows), "from": from.UTC().Format(time.RFC3339), "to": to.UTC().Format(time.RFC3339)})
}
