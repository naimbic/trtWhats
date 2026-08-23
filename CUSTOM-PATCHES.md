# Custom patches (TRT Digital fork of whatomate)

This fork (`naimbic/trtWhats`) diverges from upstream `shridarpatil/whatomate`.
**Before merging/rebasing upstream, re-apply or verify every item below.** Each
code patch is marked in-source with a greppable tag so conflicts are easy to spot:

```
grep -rn "TRT custom patch" internal/ frontend/src/
```

| # | Area | File(s) | What / Why | Commit |
|---|------|---------|------------|--------|
| 1 | Config | `internal/config/config.go` | Parse `DATABASE_URL` / `REDIS_URL` connection strings (managed Postgres/Redis on Coolify). Adds `applyDatabaseURL`/`applyRedisURL`. | a7df019 |
| 2 | Redis TLS | `internal/config/config.go`, `internal/database/redis.go` | `RedisConfig.TLSSkipVerify` + `redis://` disables TLS, `?tls_skip_verify=true` for self-signed certs. | c28f2b0 |
| 3 | Deploy | root `Dockerfile`, `docker-compose.coolify.yaml`, `.env.coolify.example`, `DEPLOY-COOLIFY.md`, `.dockerignore` | Root Dockerfile (multi-stage + ffmpeg/Piper TTS) so Coolify's default build pack finds it; Coolify deploy assets. | 61299c6, 91dc551, 1519318 |
| 4 | Calling | `internal/handlers/outgoing_calls.go` | Decrypt account access token (`decryptAccountSecrets`) before `ToWAAccount()` for calling APIs — else encrypted token → Meta error 190. | d647c25 |
| 5 | Media / multi-org | `internal/handlers/media.go` (`ServeMedia`) **+** `frontend/src/components/layout/OrganizationSwitcher.vue` | **Backend (robust fix):** for super admins, `ServeMedia` resolves the message by its UUID across any org (element `<img>`/`<video>` requests can't carry `X-Organization-ID`, so the cookie's org may not match the message's org → org-scoped lookup 404s → inbound images silently hidden). Non-super users stay org-scoped. **Frontend (defense in depth):** super-admin org switch also re-issues the session cookie (`switchOrg`), not just the header, with header-only fallback. | _this change_ |
| 6 | Notification sound | `frontend/src/services/websocket.ts` (`playNotificationSound`) | Synthesize a WhatsApp-style two-note "ding" via Web Audio API instead of loading `/notification.mp3` (no copyrighted asset). Autoplay policy still requires one user interaction. | _this change_ |
| 7 | Unread badge | `frontend/src/views/chat/ChatView.vue` (conversation list) | WhatsApp-style solid-green (`#25D366`) circular unread-count bubble, white text, `99+` cap; hidden via `v-if` once the chat is opened (`unread_count` resets to 0). Restyle only — the count logic already existed. | _this change_ |
| 8 | Unread badge (server) | `internal/handlers/chatbot_processor.go` (`saveIncomingMessage`) | Stop pre-marking bot-handled inbound messages as `read` (upstream did this for issue #280 to avoid a badge flash). Leaving them `Received` makes the WhatsApp-style unread count (contacts.go: `status != read`) reflect real inbound messages, so the green bubble shows per contact and clears only when an agent opens the chat. Pairs with patch #7 (badge styling). | _this change_ |
| 9 | Media re-download resilience | `internal/models/models.go` (`Message.MediaID`) + `internal/handlers/chatbot_processor.go` (`MediaInfo`, extraction, `saveIncomingMessage`) | Persist the Meta `media_id` on each inbound media message so a lost/wiped local file can be re-fetched from Meta (within its ~30d retention). New nullable column `media_id` (added by GORM auto-migrate on `-migrate`). Does not restore media saved before this field. **Also requires** a persistent volume on the media dir (`/app/uploads`) — this is the belt to that suspenders. | _this change_ |
| 10 | Incoming-call ring | `frontend/src/stores/calling.ts` (`handleCallEvent`) | Synthesize a repeating ring tone (Web Audio) while a `call_transfer_waiting` is pending; stop on connected/completed/abandoned/no_answer. whatomate had no incoming-call ring sound. Autoplay policy applies (one prior click). | _this change_ |
| 11 | Call-permission Darija | `pkg/whatsapp/call.go` (`SendCallPermissionRequest`) | Default call-permission-request body text changed from English to Moroccan Darija. | _this change_ |

| 12 | Team member multi-org | `internal/handlers/teams.go` (`AddTeamMember`) | Allow adding a user to a team if they're a **member** of the team's org (home org OR a `user_organizations` row), not only if it's their *home* org. Upstream rejected multi-org members (who can switch into the org) with a misleading "User not found". | _this change_ |

| 13 | Reply to media-only messages | `internal/handlers/chatbot_processor.go` (`processIncomingMessage`) | Upstream drops messages with no text, so a client who sends only a photo/screenshot/voice note got no reply (conversation stalls, bot re-asks on later texts). Now media messages (image/video/document/sticker/audio) send `FallbackMessage` (acknowledge + ask size/colour/city / staff will contact), debounced 90s via `chatbot_last_message_at` to avoid burst spam. | _this change_ |

| 14 | Auto-tag Lost (5d) | `internal/handlers/sla_processor.go` (`processOrganizationSLA` + `tagLostContacts`) | Periodic job tags contacts "Lost" (`ضائع - Perdu`) after 5 days with no inbound reply, unless already Converted/Lost. Runs for SLA-enabled orgs. Tag names hardcoded — update constants if tags are renamed. | _this change_ |

## Notes for maintainers
- **No `upstream` remote is configured.** To pull upstream safely:
  `git remote add upstream https://github.com/shridarpatil/whatomate.git`
  then `git fetch upstream && git merge upstream/main` (resolve conflicts, re-check the table above).
- Custom edits live as normal commits on `main` (the branch Coolify builds from).
- The tag `TRT custom patch` in source comments marks intentional divergences — never delete one during a conflict resolution without re-implementing it.
- Deeper root cause for #5: `internal/handlers/app.go` resolves org from `X-Organization-ID` (header) with cookie/JWT fallback; media handlers (`ServeMedia`) run through element requests that carry only the cookie. A fuller fix would let media requests carry the active org (e.g. signed URL or org in path), but the cookie re-issue above is sufficient and low-risk.
