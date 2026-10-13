package handlers

// TRT patch #71: auto-manage a pair of tags that reflect the Click-to-WhatsApp
// 72h free-messaging window. When an ad-referred message arrives the contact gets
// the "waiting" tag; once 72h pass (the free window closes) the SLA processor
// swaps it for the "past" tag — so agents can filter leads by whether a reply is
// still free.

import (
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
)

const (
	adWaitTag   = "Pub 72h ⏳"  // within the 72h free window
	adWaitColor = "green"
	adPastTag   = "Pub +72h ⌛" // past the free window — messages are billed
	adPastColor = "red"
)

// ensureTagExists creates an org tag (name+color) when it doesn't already exist,
// so the auto tags show up (coloured) in the picker/filter.
func (a *App) ensureTagExists(orgID uuid.UUID, name, color string) {
	var count int64
	a.DB.Table("tags").Where("organization_id = ? AND name = ?", orgID, name).Count(&count)
	if count > 0 {
		return
	}
	a.DB.Exec(
		"INSERT INTO tags (organization_id, name, color, created_at, updated_at) VALUES (?, ?, ?, now(), now())",
		orgID, name, color,
	)
}

// tagsAsStrings returns a contact's tags as a []string.
func tagsAsStrings(tags models.JSONBArray) []string {
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		if s, ok := t.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// setAdWindowTag rewrites a contact's tags to contain exactly one of the two
// ad-window tags (add=the one to keep). Returns the new tag array and whether it
// changed.
func setAdWindowTag(current models.JSONBArray, add string) (models.JSONBArray, bool) {
	out := models.JSONBArray{}
	hasAdd := false
	changed := false
	for _, t := range current {
		s, ok := t.(string)
		if !ok {
			out = append(out, t)
			continue
		}
		if s == adWaitTag || s == adPastTag {
			if s == add && !hasAdd {
				hasAdd = true
				out = append(out, t)
			} else {
				changed = true // dropping the other window tag (or a duplicate)
			}
			continue
		}
		out = append(out, t)
	}
	if !hasAdd {
		out = append(out, add)
		changed = true
	}
	return out, changed
}

// applyAdWaitingTag puts the "waiting" tag on a contact that just arrived via a
// Click-to-WhatsApp ad (called from captureCTWAReferral).
func (a *App) applyAdWaitingTag(contact *models.Contact) {
	a.ensureTagExists(contact.OrganizationID, adWaitTag, adWaitColor)
	newTags, changed := setAdWindowTag(contact.Tags, adWaitTag)
	if !changed {
		return
	}
	contact.Tags = newTags
	if err := a.DB.Model(&models.Contact{}).Where("id = ?", contact.ID).Update("tags", newTags).Error; err != nil {
		a.Log.Error("applyAdWaitingTag: failed", "error", err, "contact", contact.ID)
	}
}

// processAdWindowTags swaps the "waiting" tag for the "past" tag on ad contacts
// whose 72h free window has closed. Run from the SLA processor tick.
func (p *SLAProcessor) processAdWindowTags(now time.Time) {
	cutoff := now.Add(-72 * time.Hour)
	var contacts []models.Contact
	if err := p.app.DB.Where(
		"tags @> ?::jsonb AND (metadata->>'ctwa_last_seen') IS NOT NULL "+
			"AND (metadata->>'ctwa_last_seen')::timestamptz < ?",
		`["`+adWaitTag+`"]`, cutoff,
	).Limit(500).Find(&contacts).Error; err != nil {
		p.app.Log.Error("processAdWindowTags query failed", "error", err)
		return
	}
	swapped := 0
	for i := range contacts {
		c := &contacts[i]
		newTags, changed := setAdWindowTag(c.Tags, adPastTag)
		if !changed {
			continue
		}
		p.app.ensureTagExists(c.OrganizationID, adPastTag, adPastColor)
		if err := p.app.DB.Model(&models.Contact{}).Where("id = ?", c.ID).Update("tags", newTags).Error; err != nil {
			p.app.Log.Error("processAdWindowTags update failed", "error", err, "contact", c.ID)
			continue
		}
		swapped++
	}
	if swapped > 0 {
		p.app.Log.Info("Ad 72h window: tags moved to past", "count", swapped)
	}
}
