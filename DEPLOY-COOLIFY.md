# Deploying Whatomate (trtWhats) to Coolify

This app is a single Go binary that serves both the API and the embedded Vue
frontend on port **8080**, backed by **PostgreSQL** and **Redis**. The provided
[`docker-compose.coolify.yaml`](./docker-compose.coolify.yaml) deploys all three
together and builds the app image from [`docker/Dockerfile`](./docker/Dockerfile).

## Prerequisites

- A Coolify server with a Project + Environment.
- A domain (or use Coolify's auto-generated `*.sslip.io` domain). Example below
  uses `https://whats.trtdigital.ma`.
- This repo pushed to GitHub: `https://github.com/naimbic/trtWhats` (branch `main`).

## Step 1 — Create the resource

1. Coolify → your Project → **+ New** → **Docker Compose** (Git based).
2. Source: this repository, branch **main**.
3. **Compose file path:** `docker-compose.coolify.yaml`
4. Save. Coolify parses the stack (app + postgres + redis).

## Step 2 — Set environment variables

Open the resource → **Environment Variables** and add the keys from
[`.env.coolify.example`](./.env.coolify.example). At minimum:

| Variable | Value |
|---|---|
| `WHATOMATE_SERVER__ALLOWED_ORIGINS` | your public URL, e.g. `https://whats.trtdigital.ma` |
| `WHATOMATE_JWT__SECRET` | `openssl rand -hex 32` (≥ 32 chars) |
| `WHATOMATE_APP__ENCRYPTION_KEY` | `openssl rand -hex 32` (≥ 32 chars) |
| `POSTGRES_PASSWORD` | `openssl rand -hex 24` |
| `WHATOMATE_DEFAULT_ADMIN__EMAIL` | your admin email |
| `WHATOMATE_DEFAULT_ADMIN__PASSWORD` | a strong password |

> **Production gates enforced by the app on boot:** `environment=production`
> requires `JWT secret ≥ 32 chars` and a non-empty `allowed_origins`, or it exits.
> Both are already wired in the compose file.

## Step 3 — Set the domain

- In the **whatomate** service settings, set the **Domain** to your URL
  (`https://whats.trtdigital.ma`) and point that DNS record at the Coolify host.
- Coolify terminates TLS and proxies to container port **8080**. The
  `SERVICE_FQDN_WHATOMATE_8080` variable in the compose file tells Coolify which
  port to route to.
- Keep `WHATOMATE_SERVER__ALLOWED_ORIGINS` identical to this domain.

## Step 4 — Deploy

Click **Deploy**. First build takes a few minutes (frontend build + Go
cross-compile + Piper TTS assets). On boot the app:

1. connects to Postgres, runs migrations (`-migrate`),
2. seeds the initial admin (only if no users exist),
3. starts the HTTP server + background workers on 8080.

Health check: `GET /health` → `200`. Then open your domain and log in with the
admin credentials from Step 2.

## Step 5 — First-run hardening

- Log in and **change the admin password** (the seed value is only for bootstrap).
- Rotate `JWT_SECRET` / `ENCRYPTION_KEY` only via Coolify env (changing
  `ENCRYPTION_KEY` later makes previously-encrypted WhatsApp tokens unreadable —
  set it once, before connecting WhatsApp).

## Connecting WhatsApp (later)

Fill the `WHATOMATE_WHATSAPP__*` variables with your Meta app credentials, set the
webhook callback URL in Meta to `https://<your-domain>/api/whatsapp/webhook` (verify
token = `WHATOMATE_WHATSAPP__WEBHOOK_VERIFY_TOKEN`), and redeploy.

## Persistent data

Named volumes survive redeploys: `postgres-data`, `redis-data`,
`whatomate-uploads`, `whatomate-audio`. Back up `postgres-data` (or use Coolify's
scheduled backups on the Postgres service).

---

### Alternative: app image + Coolify-managed databases

If you prefer Coolify's one-click **PostgreSQL** and **Redis** resources instead
of running them in this compose file:

1. Create a **PostgreSQL** and a **Redis** resource in the same Project/Environment.
2. Deploy the app as a **Dockerfile** resource (Build Pack = Dockerfile,
   path `docker/Dockerfile`).
3. Point the app at them using the **single connection URL** each managed resource
   exposes (use the *internal* URL, not the public one):

   ```
   DATABASE_URL=postgres://user:pass@internal-host:5432/dbname?sslmode=require
   REDIS_URL=redis://:password@internal-host:6379/0
   ```

   `DATABASE_URL` / `REDIS_URL` override the discrete `WHATOMATE_DATABASE__*` /
   `WHATOMATE_REDIS__*` fields, so you don't have to split the URL by hand. (You can
   still use the split fields instead if you prefer.)

The compose approach above is simpler and self-contained; this variant gives you
independent DB lifecycle management and Coolify's built-in DB backups.
