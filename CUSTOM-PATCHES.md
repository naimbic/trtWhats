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
| 5 | Media / multi-org | `frontend/src/components/layout/OrganizationSwitcher.vue` | Super-admin org switch also re-issues the session cookie (`switchOrg`), not just the `X-Organization-ID` header. Element-based media (`<img>`/`<video>`) can't send that header, so header-only override left the cookie on the default org → `/api/media` 404 ("Message not found") → inbound images hidden. Header-only fallback preserved. | _this change_ |

## Notes for maintainers
- **No `upstream` remote is configured.** To pull upstream safely:
  `git remote add upstream https://github.com/shridarpatil/whatomate.git`
  then `git fetch upstream && git merge upstream/main` (resolve conflicts, re-check the table above).
- Custom edits live as normal commits on `main` (the branch Coolify builds from).
- The tag `TRT custom patch` in source comments marks intentional divergences — never delete one during a conflict resolution without re-implementing it.
- Deeper root cause for #5: `internal/handlers/app.go` resolves org from `X-Organization-ID` (header) with cookie/JWT fallback; media handlers (`ServeMedia`) run through element requests that carry only the cookie. A fuller fix would let media requests carry the active org (e.g. signed URL or org in path), but the cookie re-issue above is sufficient and low-risk.
