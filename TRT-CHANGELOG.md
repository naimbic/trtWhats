# trtWhats — TRT custom patches changelog

Running log of the custom features/fixes layered on the whatomate fork. Newest first.
All frontend is `go:embed`'d into the binary → every change needs a **Coolify Force
rebuild / no-cache** redeploy to go live.

## Multi-channel (WhatsApp + Instagram + Messenger)

| # | What | Status |
|---|------|--------|
| #96 | **Telegram order alerts match the real workflow: fire on the Converti tag, then again when the amount is added.** Agents mark orders by tagging *Converti* (often with no value), so the earlier value/form-only triggers missed almost everything (live check: today's orders all tagged, `conversion_value=0`, 0 alerts). Now: (1) an **"order marked"** alert (`🛒 بيعة جديدة`) fires on the Converti tag OR order-form submission — amount shows `غير محدد (à confirmer)` when not yet entered, never a bogus 0 Dh; (2) an **"amount confirmed"** update (`💰 تحديث المبلغ`) fires when the agent later saves the value. Two independent one-time pings via `telegram_notified_at` + new `telegram_value_notified_at` (no dupes; a form/tag that already carries the amount stamps both so no redundant update). Works for WhatsApp **and** Instagram contacts. | ✅ |
| #95 | **Template publish shows Meta's real reason, not "502".** `SubmitTemplate` returned 502 for *every* failure, incl. a Meta content rejection — so the UI only said "Request failed with status code 502" and hid the cause. Now a Meta validation rejection (`API error …`, e.g. "Buttons can't have … emojis …") returns **400 with Meta's message**; genuine gateway/network failures stay 502. (Root cause of the trtDigital `first_contact_meeting_fr` failure: an emoji 📅 in the URL button text — Meta forbids emojis/newlines/variables in buttons; body emojis are fine.) | ✅ |
| #94 | **WS token honours the active space (real "must-refresh" root cause).** `GetWSToken` read the JWT's *default* org and ignored the `X-Organization-ID` space-switch header that REST uses — so an agent whose default org is A but who works in space B got a socket bound to **A**, receiving **none** of B's broadcasts (inbox frozen until manual refresh). Confirmed live: the only connected socket was on *trtDigital* while 100+ msgs/30min flowed into *Belle Tulipe Inezgane*. Now `GetWSToken` resolves org via `getOrgID` (same header path as REST). | ✅ |
| #92b | **No more "0 Dh" Telegram card.** The order-form path fired the sale ping even when the form carried no amount (then the dedup blocked the later value-save). Now the form-submit only notifies when value > 0; otherwise the agent's value-save sends the card with the real price. | ✅ |
| #93 | **Live inbox pong watchdog** — the WS client sent 30s pings but never checked for a reply, so a **half-open** socket (TCP dropped with no FIN — e.g. a container restart/redeploy or a proxy idle-timeout) stayed `readyState=OPEN`, `onclose` never fired, and the inbox silently froze until a manual refresh. Now every inbound frame refreshes a liveness clock; if nothing arrives for 75s despite pings, the client force-drops the dead socket and reconnects (then force-refreshes to backfill). Fixes "my page shows 08:48 PM but messages kept coming". | ✅ |
| #92 | **Telegram sale notifications** — new Settings → Telegram page (on/off + bot token + chat id, token encrypted at rest, "Send a test" button). When enabled, every sale fires a Darija/French order card to the chat: customer, formatted phone + wa.me link, city, address, quantity, amount (whole dirhams, thousands-separated), source tags, channel, Casablanca time. Fires **once** per sale (dedup via `telegram_notified_at`) on **either** trigger: the customer submitting the **order form** (`chatbot_processor` nfm_reply) **or** the agent **saving the conversion value** (`SetContactConversion`) — whichever comes first. Fire-and-forget (never blocks/fails), 8s timeout, token never logged. Pure `buildTelegramSaleMessage` builder + unit tests. Manual **Converti**-tagging alone does **not** fire (by request). | ✅ |
| #91 | **WhatsApp platform tab default** + pagination keeps the filter — the inbox defaults to WhatsApp, and `loadMoreContacts` now carries `channel` so scrolling past page 1 no longer leaks other platforms into a filtered view. | ✅ |
| #90 | **Per-channel on/off switch** (Settings → Instagram/Messenger) — a Switch on each account row; **off = no sync** (webhook ignores that account's DMs/comments). Dedicated `PUT …/accounts/{id}/active` endpoints. | ✅ |
| #89 | **Fallback contact name** for Messenger/Instagram — user-profile API needs Advanced Access (Standard 400s), so inbound contacts had blank names; now "Messenger 1234"/"Instagram 1234". Backfilled existing. | ✅ |
| #88 | **"Not answered" dot** for IG/Messenger — keys off `is_read` (not just unread count); shows until an agent replies, **persists when you open/read** the chat, clears only on reply (same as WhatsApp). `is_read` now exposed to the UI; agent reply marks incoming read + is_read=true. | ✅ |
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
