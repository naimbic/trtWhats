package handlers

// TRT custom patch #52: copy configuration (tags, chatbot messages, keyword
// rules) from another space the user belongs to into the current space. This is
// ADDITIVE ONLY and cannot break the current setup: it never overwrites existing
// tags or keyword rules, and only fills chatbot message fields that are currently
// EMPTY. Flow-type keyword rules are skipped (their flow doesn't exist in the
// target space) and reported so the user can set the order flow up separately.

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// CopySpaceConfigRequest is the body for POST /api/spaces/copy-config.
type CopySpaceConfigRequest struct {
	FromOrgID    string `json:"from_org_id"`
	ToAccount    string `json:"to_account"` // target WhatsApp number for the keyword rules
	CopyTags     bool   `json:"copy_tags"`
	CopyChatbot  bool   `json:"copy_chatbot"`
	CopyKeywords bool   `json:"copy_keywords"`
}

// CopySpaceConfig performs the additive copy and returns a summary of what changed.
func (a *App) CopySpaceConfig(r *fastglue.Request) error {
	orgID, userID, err := a.getOrgAndUserID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	if !a.IsSuperAdmin(userID) && !a.HasPermission(userID, models.ResourceOrganizations, models.ActionWrite) {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "You don't have permission to copy space setup", nil, "")
	}

	var req CopySpaceConfigRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	fromOrg, perr := uuid.Parse(req.FromOrgID)
	if perr != nil || fromOrg == orgID {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Choose a different source space", nil, "")
	}
	// The user must belong to the source space (or be a super admin).
	if !a.IsSuperAdmin(userID) {
		var count int64
		a.DB.Table("user_organizations").
			Where("user_id = ? AND organization_id = ? AND deleted_at IS NULL", userID, fromOrg).
			Count(&count)
		if count == 0 {
			return r.SendErrorEnvelope(fasthttp.StatusForbidden, "You are not a member of the source space", nil, "")
		}
	}

	summary := map[string]any{}

	// 1) Tags — add any missing by name (never overwrite colours of existing tags).
	if req.CopyTags {
		var src []models.Tag
		a.DB.Where("organization_id = ?", fromOrg).Find(&src)
		added := 0
		for _, t := range src {
			var count int64
			a.DB.Model(&models.Tag{}).Where("organization_id = ? AND name = ?", orgID, t.Name).Count(&count)
			if count == 0 {
				if err := a.DB.Create(&models.Tag{OrganizationID: orgID, Name: t.Name, Color: t.Color}).Error; err == nil {
					added++
				}
			}
		}
		summary["tags_added"] = added
	}

	// 2) Chatbot messages — fill EMPTY fields on the target's org-level settings.
	if req.CopyChatbot {
		var src models.ChatbotSettings
		if err := a.DB.Where("organization_id = ? AND whatsapp_account = ?", fromOrg, "").First(&src).Error; err == nil {
			var tgt models.ChatbotSettings
			created := a.DB.Where("organization_id = ? AND whatsapp_account = ?", orgID, "").First(&tgt).Error != nil
			if created {
				tgt = models.ChatbotSettings{OrganizationID: orgID}
			}
			fillEmpty := func(dst *string, srcv string) {
				if *dst == "" && srcv != "" {
					*dst = srcv
				}
			}
			fillEmpty(&tgt.DefaultResponse, src.DefaultResponse)
			fillEmpty(&tgt.FallbackMessage, src.FallbackMessage)
			fillEmpty(&tgt.BusinessHours.OutOfHoursMessage, src.BusinessHours.OutOfHoursMessage)
			fillEmpty(&tgt.ClientInactivity.ReminderMessage, src.ClientInactivity.ReminderMessage)
			fillEmpty(&tgt.ClientInactivity.AutoCloseMessage, src.ClientInactivity.AutoCloseMessage)
			if len(tgt.GreetingButtons) == 0 {
				tgt.GreetingButtons = src.GreetingButtons
			}
			if len(tgt.FallbackButtons) == 0 {
				tgt.FallbackButtons = src.FallbackButtons
			}
			if created {
				a.DB.Create(&tgt)
			} else {
				a.DB.Save(&tgt)
			}
			a.InvalidateChatbotSettingsCache(orgID)
			summary["chatbot_updated"] = true
		} else {
			summary["chatbot_updated"] = false
		}
	}

	// 3) Keyword rules — copy to the chosen target number, skipping any with the
	// same name and any flow-type rule (its flow doesn't exist in this space).
	if req.CopyKeywords && req.ToAccount != "" {
		var src []models.KeywordRule
		a.DB.Where("organization_id = ?", fromOrg).Find(&src)
		added, flowSkipped := 0, 0
		for _, rule := range src {
			if rule.ResponseType == models.ResponseTypeFlow {
				flowSkipped++
				continue
			}
			var count int64
			a.DB.Model(&models.KeywordRule{}).
				Where("organization_id = ? AND whats_app_account = ? AND name = ?", orgID, req.ToAccount, rule.Name).
				Count(&count)
			if count > 0 {
				continue
			}
			nr := rule
			nr.BaseModel = models.BaseModel{ID: uuid.New()}
			nr.OrganizationID = orgID
			nr.WhatsAppAccount = req.ToAccount
			nr.CreatedByID = &userID
			nr.UpdatedByID = &userID
			if err := a.DB.Create(&nr).Error; err == nil {
				added++
			}
		}
		a.InvalidateKeywordRulesCache(orgID)
		summary["keyword_rules_added"] = added
		summary["keyword_rules_flow_skipped"] = flowSkipped
	}

	a.Log.Info("Copied space config", "from", fromOrg, "to", orgID, "summary", summary)
	return r.SendEnvelope(summary)
}
