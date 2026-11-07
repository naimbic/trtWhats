package handlers

// TRT patch #74: admin-only moderation of conversations.
//   - Hide a session so only org admins see it (admin_only flag) — hidden from
//     agents AND managers everywhere contacts are listed/opened/searched.
//   - Admin-only soft-delete of a single message, a conversation (messages only),
//     or a whole session (contact + its messages). Soft delete = recoverable
//     (rows keep deleted_at; GORM's default scope hides them from the app).
//
// "Admin" here means the org's `admin` system role or a super admin — NOT manager
// and NOT agent, per the requirement that these are admin-exclusive.

import (
	"strings"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// isOrgAdmin reports whether the user is an org admin (role "admin") or a super
// admin. Managers and agents are NOT admins.
func (a *App) isOrgAdmin(userID, orgID uuid.UUID) bool {
	perms, err := a.getUserPermissionsCached(userID, orgID)
	if err != nil {
		a.Log.Error("isOrgAdmin: failed to load permissions", "error", err, "user_id", userID)
		return false
	}
	return perms.IsSuperAdmin || strings.EqualFold(perms.RoleName, "admin")
}

// SetContactVisibility toggles a contact's admin-only (hidden) flag.
// PUT /api/contacts/{id}/visibility   body: { "admin_only": true|false }
func (a *App) SetContactVisibility(r *fastglue.Request) error {
	orgID, userID, err := a.getOrgAndUserID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	if !a.isOrgAdmin(userID, orgID) {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "Only admins can hide or show sessions", nil, "")
	}
	contactID, err := parsePathUUID(r, "id", "contact")
	if err != nil {
		return nil
	}
	contact, err := findByIDAndOrg[models.Contact](a.DB, r, contactID, orgID, "Contact")
	if err != nil {
		return nil
	}
	var req struct {
		AdminOnly bool `json:"admin_only"`
	}
	_ = a.decodeRequest(r, &req)

	old := *contact
	if err := a.DB.Model(&models.Contact{}).Where("id = ? AND organization_id = ?", contactID, orgID).
		Update("admin_only", req.AdminOnly).Error; err != nil {
		a.Log.Error("SetContactVisibility: update failed", "error", err, "contact", contactID)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update session visibility", nil, "")
	}
	contact.AdminOnly = req.AdminOnly
	a.logAudit(orgID, userID, "contact", contactID, models.AuditActionUpdated, &old, contact)

	msg := "Session is now visible to all agents"
	if req.AdminOnly {
		msg = "Session is now hidden — admins only"
	}
	return r.SendEnvelope(map[string]any{"admin_only": req.AdminOnly, "message": msg})
}

// ClearConversation soft-deletes all messages for a contact but keeps the contact
// in the list. DELETE /api/contacts/{id}/conversation
func (a *App) ClearConversation(r *fastglue.Request) error {
	orgID, userID, err := a.getOrgAndUserID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	if !a.isOrgAdmin(userID, orgID) {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "Only admins can delete messages", nil, "")
	}
	contactID, err := parsePathUUID(r, "id", "contact")
	if err != nil {
		return nil
	}
	contact, err := findByIDAndOrg[models.Contact](a.DB, r, contactID, orgID, "Contact")
	if err != nil {
		return nil
	}
	res := a.DB.Where("contact_id = ? AND organization_id = ?", contactID, orgID).Delete(&models.Message{})
	if res.Error != nil {
		a.Log.Error("ClearConversation: delete failed", "error", res.Error, "contact", contactID)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to clear conversation", nil, "")
	}
	// Clear stale list badges now that the history is gone.
	a.DB.Model(&models.Contact{}).Where("id = ?", contactID).Updates(map[string]any{
		"is_read":              true,
		"order_pending":        false,
		"last_message_preview": "",
	})
	a.logAudit(orgID, userID, "contact", contactID, models.AuditActionDeleted, contact, nil)
	a.Log.Info("Admin cleared conversation", "contact", contactID, "messages_deleted", res.RowsAffected, "by", userID)
	return r.SendEnvelope(map[string]any{
		"message":          "Conversation cleared",
		"messages_deleted": res.RowsAffected,
	})
}

// DeleteSession soft-deletes a whole session: the contact AND all its messages.
// DELETE /api/contacts/{id}/session
func (a *App) DeleteSession(r *fastglue.Request) error {
	orgID, userID, err := a.getOrgAndUserID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	if !a.isOrgAdmin(userID, orgID) {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "Only admins can delete sessions", nil, "")
	}
	contactID, err := parsePathUUID(r, "id", "contact")
	if err != nil {
		return nil
	}
	contact, err := findByIDAndOrg[models.Contact](a.DB, r, contactID, orgID, "Contact")
	if err != nil {
		return nil
	}
	res := a.DB.Where("contact_id = ? AND organization_id = ?", contactID, orgID).Delete(&models.Message{})
	if res.Error != nil {
		a.Log.Error("DeleteSession: message delete failed", "error", res.Error, "contact", contactID)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to delete session", nil, "")
	}
	if err := a.DB.Delete(contact).Error; err != nil {
		a.Log.Error("DeleteSession: contact delete failed", "error", err, "contact", contactID)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to delete session", nil, "")
	}
	a.logAudit(orgID, userID, "contact", contactID, models.AuditActionDeleted, contact, nil)
	a.Log.Info("Admin deleted session", "contact", contactID, "messages_deleted", res.RowsAffected, "by", userID)
	return r.SendEnvelope(map[string]any{
		"message":          "Session deleted",
		"messages_deleted": res.RowsAffected,
	})
}

// DeleteMessage soft-deletes a single message. DELETE /api/messages/{id}
func (a *App) DeleteMessage(r *fastglue.Request) error {
	orgID, userID, err := a.getOrgAndUserID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	if !a.isOrgAdmin(userID, orgID) {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "Only admins can delete messages", nil, "")
	}
	msgID, err := parsePathUUID(r, "id", "message")
	if err != nil {
		return nil
	}
	var msg models.Message
	if err := a.DB.Where("id = ? AND organization_id = ?", msgID, orgID).First(&msg).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Message not found", nil, "")
	}
	if err := a.DB.Delete(&msg).Error; err != nil {
		a.Log.Error("DeleteMessage: delete failed", "error", err, "message", msgID)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to delete message", nil, "")
	}
	a.logAudit(orgID, userID, "message", msgID, models.AuditActionDeleted, &msg, nil)
	return r.SendEnvelope(map[string]any{"message": "Message deleted"})
}
