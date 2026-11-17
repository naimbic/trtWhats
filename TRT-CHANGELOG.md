# trtWhats — TRT custom patches changelog

Running log of the custom features/fixes layered on the whatomate fork. Newest first.
All frontend is `go:embed`'d into the binary → every change needs a **Coolify Force
rebuild / no-cache** redeploy to go live.

## Multi-channel (WhatsApp + Instagram + Messenger)

| # | What | Status |
|---|------|--------|
| #87 | **Inbound IG/Messenger media renders** — download the Meta CDN attachment locally on receipt (was stored as an absolute CDN URL that `/api/media/{id}` can't serve → blank "empty"/no-image bubbles). | ✅ |
| #86 | **Filter inbox by platform** (All / WhatsApp / Instagram / Messenger) — chip row above the list; `?channel=` on `/api/contacts`. | ✅ |
| #85 | **Agent reply clears "not answered"** on IG/Messenger — `clearUnhandledOnAgentReply` clears `order_pending` + broadcasts `contact_read` (mirrors the WhatsApp send path). | ✅ |
| #84 / #84b | **Composer reply to an IG comment → DM.** Typing in "Type a message…" on a comment contact sends via the Private-Reply API (`recipient{comment_id}`); if that fails ("already has a reply" — one per comment) it **falls back to a normal DM send**. | ✅ (needs redeploy) |
| #82 | **IG comment tag + reply buttons render live** — added `metadata` to the WS broadcast + `MessageResponse` (was only on page load). | ✅ |
| #80/#81/#83 | **Live inbox** — (81) update the contact list on every new message, not just the open chat; (80) pull in brand-new conversations; (83) **reconnect the WebSocket when the active space changes** (it's per-org — was the "must refresh" root cause). | ✅ |
| #78 | **Fix IG/Messenger contact creation** — `idx_contacts_org_phone` made PARTIAL (`WHERE phone_number<>''`) so empty-phone (IG/Messenger) contacts don't collide. | ✅ (live DB patched) |
| #77 | **Instagram comments** — capture post comments into the inbox (💬 tag) + reply **publicly** (`/{comment_id}/replies`) or by **private DM**. | ✅ |
| #76 | **Image replies** for Instagram & Messenger (hosts file publicly, sends as attachment by URL). | ✅ |
| #75 | **Facebook Messenger channel** (Page DMs) — object `page`, keyed by PSID; Settings→Messenger. | ✅ |
| #68 | **Instagram DM channel** — object `instagram`, keyed by IGSID; Settings→Instagram. | ✅ |

### ⚠️ Known Meta-side gate (not a code issue)
- **Instagram DMs** only deliver webhooks from **app testers** while the app is in **Development** mode (comments deliver from anyone). For production: get `instagram_manage_messages` **Advanced Access** (App Review) + set app to **Live**.
- Each channel's webhook is configured separately: WhatsApp `/api/webhook` (object `whatsapp_business_account`), Messenger `/api/messenger/webhook` (object **Page**), Instagram `/api/instagram/webhook` (object **Instagram**, inside the Instagram product). Must **subscribe the field** (`messages`/`comments`) — verifying the callback alone subscribes nothing. Use the **Page** access token for Messenger; subscribe the IG account via `POST /{ig_user_id}/subscribed_apps`.

## Source / cost / moderation (earlier this cycle)

| # | What |
|---|------|
| #74 | Admin-only **hide / clear / delete** sessions (`admin_only` hidden from agents+managers; soft-delete). |
| #71–#73 | **72h CTWA free-window** dot + **$ billed** dot; organic FB/IG referral capture; tracked-link **source tags** (Arabic+Latin). |

## Pending / parked
- Instagram DMs blocked on Meta tester/Advanced-Access gate (above).
- Ameex webhook signature (status auto-updates) — flagged earlier, not yet fixed.
