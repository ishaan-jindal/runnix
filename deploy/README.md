# Runnix deployment runbook (single VM: prod + dev)

One Oracle e2-micro VM (1 GB RAM, 1 core, x86_64, Ubuntu) hosts everything:
three compose projects (`runnix-caddy`, `runnix-prod`, `runnix-dev`) on the
external network `runnix-public`. Caddy terminates TLS automatically for
`api.runnix.xyz` (prod) and `dev.runnix.xyz` (dev) and proxies to the two
app stacks. Images come from GHCR as
`ghcr.io/ishaan-jindal/runnix-{gateway,dispatcher,runner-python}`, tagged
with the version plus `latest` on releases and `edge` on main.

File map (hosted files are owned by sibling work; local file stays as is):

- `deploy/compose.caddy.yaml` — Caddy reverse proxy (project `runnix-caddy`)
- `deploy/compose.prod.yaml` — prod app stack (project `runnix-prod`)
- `deploy/compose.dev.yaml` — dev app stack (project `runnix-dev`)
- `deploy/Caddyfile` — `api.runnix.xyz` / `dev.runnix.xyz` routing + auto TLS
- `deploy/prod.env.example`, `deploy/dev.env.example` — env templates
- `deploy/compose.yaml` — laptop-dev path only (`just compose-up`)

## 1. Prerequisites

Ubuntu 22.04 or 24.04 on the VM.

2 GB swapfile (1 GB RAM box needs it before Docker does anything real):

```bash
sudo fallocate -l 2G /swapfile
sudo chmod 600 /swapfile
sudo mkswap /swapfile
sudo swapon /swapfile
echo '/swapfile none swap sw 0 0' | sudo tee -a /etc/fstab
free -h
```

Docker Engine + compose plugin (Ubuntu repo):

```bash
sudo apt-get update
sudo apt-get install -y ca-certificates curl gnupg
sudo install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo gpg --dearmor -o /etc/apt/keyrings/docker.gpg
sudo chmod a+r /etc/apt/keyrings/docker.gpg
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo $VERSION_CODENAME) stable" | sudo tee /etc/apt/sources.list.d/docker.list
sudo apt-get update
sudo apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
docker --version && docker compose version
sudo usermod -aG docker "$USER"   # log out/in after this
```

gVisor `runsc` (the dispatcher sandboxes every execution under `runsc`):

```bash
curl -fsSL https://gvisor.dev/archive.key | sudo gpg --dearmor -o /usr/share/keyrings/gvisor-archive-keyring.gpg
echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/gvisor-archive-keyring.gpg] https://storage.googleapis.com/gvisor/releases release main" | sudo tee /etc/apt/sources.list.d/gvisor.list
sudo apt-get update && sudo apt-get install -y runsc
runsc --version
sudo docker info --format '{{json .Runtimes}}' | python3 -m json.tool
# expect a "runsc" entry; if missing, register it in /etc/docker/daemon.json
# then `sudo systemctl restart docker`
```

Ports 80/443 must be open in TWO places. Oracle VCN first: console /
security list ingress rules — allow TCP 80 and 443 from `0.0.0.0/0`.
Oracle Ubuntu images also filter on-host by default, so open iptables too:

```bash
sudo iptables -I INPUT -p tcp --dport 80 -j ACCEPT
sudo iptables -I INPUT -p tcp --dport 443 -j ACCEPT
sudo apt-get install -y iptables-persistent
sudo netfilter-persistent save
```

DNS: one A-record per name, both pointing at the VM public IP:

- `api.runnix.xyz` → `<vm-public-ip>`
- `dev.runnix.xyz` → `<vm-public-ip>`

## 2. GHCR access

Assumption: the images are PUBLIC, so the VM needs no registry login —
`docker compose pull` just works.

Private-registry alternative (one line): `docker login ghcr.io`.

## 3. Secrets

Env files live OUTSIDE the repo (never commit them):

```bash
mkdir -p ~/runnix
cp deploy/dev.env.example ~/runnix/dev.env      # same for prod below
cp deploy/prod.env.example ~/runnix/prod.env
chmod 600 ~/runnix/dev.env ~/runnix/prod.env
```

Fill in at least:

```bash
openssl rand -hex 32   # JWT_SECRET (use one value per stack)
openssl rand -hex 32   # WEBHOOK_SIGNING_SECRET (same value on gateway + dispatcher)
openssl rand -hex 24   # POSTGRES_PASSWORD (per stack)
```

Set `IMAGE_TAG=vX.Y.Z` in `~/runnix/prod.env` and `IMAGE_TAG=edge` in
`~/runnix/dev.env`. Keep the two stacks' passwords and JWT secrets distinct.

## 4. First boot order

Publish images before the first `up -d`: the GHCR tags (`:v0.1.0` /
`:latest` / `:edge`) do not exist until the `docker` workflow has run at
least once. Push a tag (or `workflow_dispatch` the docker workflow on
`main`), then verify the tag exists before proceeding:

```bash
docker manifest inspect ghcr.io/ishaan-jindal/runnix-gateway:<tag>
```

`latest` appears only after the first tag build; `edge` appears only
after the first post-merge `main` build.

Shared network first, then prod, then dev, Caddy last so first requests
don't 502; cert issuance is independent of backend state:

```bash
docker network create runnix-public || true
docker compose -p runnix-prod -f deploy/compose.prod.yaml --env-file ~/runnix/prod.env up -d
docker compose -p runnix-dev -f deploy/compose.dev.yaml --env-file ~/runnix/dev.env up -d
docker compose -p runnix-caddy -f deploy/compose.caddy.yaml --env-file ~/runnix/prod.env --env-file ~/runnix/dev.env up -d
docker ps --format 'table {{.Names}}\t{{.Status}}\t{{.Ports}}'
```

The Caddy up command passes both env files because `DEV_DOMAIN` lives in
`dev.env`; the overlap of other keys is harmless (the Caddy compose file
only interpolates the two domains).

Migrations auto-apply at gateway startup (embedded, idempotent) — no manual
migrate step on the VM. If a gateway restarts against an already-migrated
DB it is a no-op.

## 5. Smoke test

Liveness straight through Caddy:

```bash
curl https://api.runnix.xyz/healthz
curl https://dev.runnix.xyz/healthz
```

Register → submit → poll against the live domain (adapted from the laptop
quickstart, with `Authorization` plus `X-Tenant-ID` on tenant routes):

```bash
BASE=https://api.runnix.xyz
REG=$(curl -s -X POST $BASE/auth/register -H 'Content-Type: application/json' -d '{"username":"smoke","email":"smoke@example.org","password":"SecurePass123"}')
TOKEN=$(echo "$REG" | python3 -c "import json,sys; print(json.load(sys.stdin)['accessToken'])")
TENANT=$(echo "$REG" | python3 -c "import json,sys; print(json.load(sys.stdin)['tenants'][0]['id'])")
SUB=$(curl -s -X POST $BASE/executions -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: $TENANT" -H 'Content-Type: application/json' -d '{"language":"python","source":"print(40+2)","tenant_id":"'$TENANT'"}')
ID=$(echo "$SUB" | python3 -c "import json,sys; print(json.load(sys.stdin)['id'])")
curl -s -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: $TENANT" $BASE/executions/$ID | python3 -m json.tool
# status walks queued -> running -> succeeded; stdout holds "42\n"
```

Same checks against `BASE=https://dev.runnix.xyz` (register a separate user
there; prod and dev databases are independent).

## 6. Updates

Prod (pinned tags, explicit rollback):

```bash
# in ~/runnix/prod.env: IMAGE_TAG=vX.Y.Z
docker compose -p runnix-prod -f deploy/compose.prod.yaml --env-file ~/runnix/prod.env pull
docker compose -p runnix-prod -f deploy/compose.prod.yaml --env-file ~/runnix/prod.env up -d
# re-run the section 5 smoke test against https://api.runnix.xyz
# rollback = set IMAGE_TAG to the previous tag, then pull + up -d again
```

Dev (floating `edge`, auto-updated by Watchtower):

```bash
# in ~/runnix/dev.env: IMAGE_TAG=edge
# The dev stack runs Watchtower (scope runnix-dev, 5-min poll): new edge
# images restart gateway/dispatcher automatically. Watch: docker logs runnix-dev-watchtower-1
# Manual nudge if needed:
docker compose -p runnix-dev -f deploy/compose.dev.yaml --env-file ~/runnix/dev.env pull
docker compose -p runnix-dev -f deploy/compose.dev.yaml --env-file ~/runnix/dev.env up -d
```

## 7. Pressure valve

This is a 1 GB box — treat RAM as the budget:

- When dev sits idle, stop it: `docker compose -p runnix-dev -f deploy/compose.dev.yaml down`
  (plain `down` keeps volumes; the dev DB survives the stop).
- Never run load on both stacks at once.
- Never `down -v` on prod (deletes the database volume) nor on caddy
  (wipes cert storage, forcing re-issuance against rate limits); plain
  `down` is safe for both.

> Known risk: the dispatcher runs as root with the host docker socket
> (inherited dev posture until Job-per-execution) — a dispatcher
> compromise means host root. Mitigations in place: no published
> DB/NATS/gateway ports, `ENV=production` fail-closed, stop idle dev.
> Do not add mounts or published ports to the dispatcher.

## 8. Backups

Nightly `pg_dump` cron for BOTH databases (they live in separate compose
projects). Container names follow the `"<project>-<service>-1"` convention —
confirm with `docker ps` and adjust if the service name differs:

```bash
set -a; . ~/runnix/prod.env; set +a
docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" runnix-prod-postgres-1 pg_dump -U "$POSTGRES_USER" "$POSTGRES_DB" | gzip > ~/runnix/prod-$(date +%F).sql.gz && chmod 600 ~/runnix/prod-$(date +%F).sql.gz
set -a; . ~/runnix/dev.env; set +a
docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" runnix-dev-postgres-1 pg_dump -U "$POSTGRES_USER" "$POSTGRES_DB" | gzip > ~/runnix/dev-$(date +%F).sql.gz && chmod 600 ~/runnix/dev-$(date +%F).sql.gz
```

Cron (every night at 02:00, keep 7 days):

```cron
0 2 * * * set -a; . ~/runnix/prod.env; set +a; docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" runnix-prod-postgres-1 pg_dump -U "$POSTGRES_USER" "$POSTGRES_DB" | gzip > ~/runnix/prod-$(date +\%F).sql.gz && chmod 600 ~/runnix/prod-$(date +\%F).sql.gz && find ~/runnix -name 'prod-*.sql.gz' -mtime +7 -delete
0 2 * * * set -a; . ~/runnix/dev.env; set +a; docker exec -e PGPASSWORD="$POSTGRES_PASSWORD" runnix-dev-postgres-1 pg_dump -U "$POSTGRES_USER" "$POSTGRES_DB" | gzip > ~/runnix/dev-$(date +\%F).sql.gz && chmod 600 ~/runnix/dev-$(date +\%F).sql.gz && find ~/runnix -name 'dev-*.sql.gz' -mtime +7 -delete
```

NOT covered: NATS stream files. Acceptable loss — in-flight executions
re-queue on redelivery or are failed by the claim guard + reaper, so a lost
stream costs retries, not silent wrong results.

## 9. Ops

```bash
docker compose -p runnix-prod -f deploy/compose.prod.yaml logs --tail 100 gateway        # tail one service
docker compose -p runnix-prod -f deploy/compose.prod.yaml logs --tail 100 dispatcher
docker compose -p runnix-prod -f deploy/compose.prod.yaml ps                             # project status
docker compose -p runnix-prod -f deploy/compose.prod.yaml up -d --no-deps gateway        # restart one service
```

Caddy / cert check (automatic TLS; a healthy domain answers 200 with a
valid chain):

```bash
curl -vI https://api.runnix.xyz/healthz
curl -vI https://dev.runnix.xyz/healthz
```

When `/readyz` 503s, the body names the dependency: check gateway logs for
`postgres` / `nats` errors, then `docker compose -p runnix-prod -f deploy/compose.prod.yaml ps` for a
restarting `postgres` or `nats` container, then `docker volume ls` only to
confirm the data volume still exists (do not delete it).
