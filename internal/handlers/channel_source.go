package handlers

// TRT patch #73: detect a lead's traffic source from the prefilled text of a
// wa.me / QR link. For organic channels that carry NO Meta referral (an
// Instagram bio link via biolinks.ma, a website button, a printed flyer QR),
// the only way to know where the customer came from is to bake the source into
// the link's prefilled message, e.g.:
//
//	https://wa.me/2126XXXXXXXX?text=السلام، جيت من انستغرام 🌷
//
// On the contact's FIRST inbound message we scan that text for a known channel
// keyword (Arabic or Latin), store it as contact.metadata.source and drop a
// coloured "Source: X" tag so agents can filter leads by channel. Paid ads
// (ctwa_clid) and organic FB/IG post referrals already carry their own source,
// so this never overrides them.

import (
	"strings"

	"github.com/shridarpatil/whatomate/internal/models"
)

// channelRule maps a traffic source to the tag it applies and the keywords that
// identify it. Needles are matched case-insensitively; Arabic needles match the
// raw (already lower-cased) text as-is.
type channelRule struct {
	source  string   // human label stored in metadata + used in the tag
	tag     string   // "Source: Instagram"
	color   string   // one of the frontend TAG_SOLID keys
	needles []string // any match wins
}

// channelRules order matters only for overlapping needles; the first match wins.
var channelRules = []channelRule{
	{source: "Instagram", tag: "Source: Instagram", color: "purple",
		needles: []string{"انستغرام", "انستقرام", "إنستغرام", "انستجرام", "انستا", "instagram", "insta"}},
	{source: "Facebook", tag: "Source: Facebook", color: "blue",
		needles: []string{"فيسبوك", "فيس بوك", "فايسبوك", "facebook"}},
	{source: "TikTok", tag: "Source: TikTok", color: "gray",
		needles: []string{"تيك توك", "تيكتوك", "tiktok", "tik tok"}},
	{source: "Site web", tag: "Source: Site web", color: "green",
		needles: []string{"الموقع الإلكتروني", "الموقع", "site web", "website", "siteweb"}},
	{source: "Flyer", tag: "Source: Flyer", color: "orange",
		needles: []string{"الإعلان", "الاعلان", "المنشور الورقي", "الملصق", "flyer", "فلاير"}},
}

// captureChannelSource inspects the first inbound message for a channel keyword
// baked into a tracked wa.me / QR link and tags the contact with its source.
// No-op when: not the first message, a source is already known, or the message
// carries a real Meta referral (ad / organic post) that is a better signal.
func (a *App) captureChannelSource(contact *models.Contact, text string, isNewContact bool) {
	if !isNewContact || strings.TrimSpace(text) == "" {
		return
	}
	md := contact.Metadata
	if md == nil {
		md = models.JSONB{}
	}
	// Don't override a source Meta already gave us (paid ad or organic FB/IG post).
	if s, _ := md["source"].(string); s != "" {
		return
	}
	if clid, _ := md["ctwa_clid"].(string); clid != "" {
		return
	}
	if ch, _ := md["referral_channel"].(string); ch != "" {
		return
	}

	lower := strings.ToLower(text)
	var matched *channelRule
	for i := range channelRules {
		for _, n := range channelRules[i].needles {
			if strings.Contains(lower, n) {
				matched = &channelRules[i]
				break
			}
		}
		if matched != nil {
			break
		}
	}
	if matched == nil {
		return
	}

	md["source"] = matched.source
	md["source_channel_link"] = true // came from a tracked link, not a Meta referral
	contact.Metadata = md
	if err := a.DB.Model(&models.Contact{}).Where("id = ?", contact.ID).
		Update("metadata", md).Error; err != nil {
		a.Log.Error("captureChannelSource: failed to persist source", "error", err, "contact", contact.ID)
		return
	}

	// Drop the coloured "Source: X" tag (idempotent).
	a.ensureTagExists(contact.OrganizationID, matched.tag, matched.color)
	if !contactHasTag(contact.Tags, matched.tag) {
		newTags := append(models.JSONBArray{}, contact.Tags...)
		newTags = append(newTags, matched.tag)
		contact.Tags = newTags
		if err := a.DB.Model(&models.Contact{}).Where("id = ?", contact.ID).
			Update("tags", newTags).Error; err != nil {
			a.Log.Error("captureChannelSource: failed to tag", "error", err, "contact", contact.ID)
		}
	}
	a.Log.Info("Captured channel source from link text",
		"contact", contact.ID, "source", matched.source)
}

// contactHasTag reports whether a tag name is already on the contact.
func contactHasTag(tags models.JSONBArray, name string) bool {
	for _, t := range tags {
		if s, ok := t.(string); ok && s == name {
			return true
		}
	}
	return false
}
