# Pitwall production deploy (VPS) — operator runbook

Realises **ADR-0007** on the smallest slice (Story 1.12). The pipeline is **pull-based**
(Q29.1): a per-service tag builds in CI and pushes to **public GHCR**; a **VPS-side poller**
pulls the new image and recreates **only that container**. There is **no inbound SSH from CI**
and **no SSH/deploy-key secret** anywhere.

```
git tag ‹svc›-vX.Y.Z ──► GitHub Actions (release.yml) ──► ghcr.io/<owner>/pitwall-‹svc›:X.Y.Z (public)
                                                                   │
        VPS: pitwall-deploy.timer ─► pitwall-deploy-poller.sh ─────┘  pull + up -d --no-build ‹svc›
```

> ## ⚠️ The VPS is shared with another live project — do not break it
> Another project (`/root/AI-bot`, containers `rules_bot_*` + `dozzle`) runs on this host.
> **Never** stop/rm/restart/prune anything outside the `pitwall` project; **never** run a global
> `docker system/volume/network prune` or any `-a` cleanup; **never** edit `/root/deploy-poller.sh`,
> `rules-deploy.{service,timer}` or `/root/.deployed_tag`. Pitwall is fully namespaced: project
> `pitwall`, dir `/root/pitwall`, units `pitwall-deploy.{service,timer}`, marker
> `/root/pitwall.deployed_tags`, and a **non-colliding** loopback board port (8080/8081 are taken).
>
> ## 🔒 Never commit a secret — especially the VPS host/IP
> This repo is **public**. Real RabbitMQ creds and the VPS host live **only** in the VPS `/root/pitwall/.env`
> (gitignored) and your local tooling. No tracked file contains the host/IP. Below, `‹vps-host›` is a
> placeholder — substitute your own host locally; do not paste it into any file you commit.

---

## 1. One-time VPS setup (manual; not part of the per-release flow)

Prerequisite: a VPS with Docker + Compose (already present) and your workstation's IP allowed to SSH.
Images are **public**, so the VPS needs **no `docker login`**.

```sh
# on the VPS (root)
cd /root
git clone https://github.com/Jeremy-Luyckfasseel/Pitwall.git pitwall
cd /root/pitwall

# create the prod .env (NEVER committed): real creds + a non-colliding loopback board port
cp .env.example .env
#   edit /root/pitwall/.env:
#     RABBITMQ_USER=<real>          RABBITMQ_PASSWORD=<real-strong>
#     LEADERBOARD_HTTP_PORT=8090    # a port not used by the other project (8080/8081 are taken)
#     TIMING_VERSION / LEADERBOARD_VERSION  # the first tags you will release (e.g. 0.1.0)
#     SIMULATOR_ENABLED=true        # the walking-skeleton board shows simulated laps

# install the poller + its systemd timer (Pitwall-namespaced — does not touch the other project)
install -m 0755 deploy/pitwall-deploy-poller.sh /root/pitwall-deploy-poller.sh
cp deploy/systemd/pitwall-deploy.service deploy/systemd/pitwall-deploy.timer /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now pitwall-deploy.timer
```

Validate the merged prod compose before the first bring-up (client-side; no daemon needed):

```sh
make prod-config        # or: sh scripts/check-prod-compose.sh
```

> ### ⚠️ The server NEVER builds — always `pull` + `up -d --no-build`
> The base `docker-compose.yml` still carries a `build:` section for each service (it's the dev
> file). On the VPS that section must be **ignored**: images come from GHCR (ADR-0007). So **never**
> run a bare `docker compose ... up -d` on the VPS — it could build on the server. The poller already
> does the right thing (`pull` then `up -d --no-build`); any manual bring-up/deploy must too.

**First bring-up** (once the first images exist in GHCR — see §2): the poller does this automatically
on its next tick, but to do it by hand use the pull-only path (never a bare `up`):

```sh
cd /root/pitwall
docker compose -p pitwall -f docker-compose.yml -f docker-compose.prod.yml pull
docker compose -p pitwall -f docker-compose.yml -f docker-compose.prod.yml up -d --no-build
```

## 2. Cut a release (the only thing you do per deploy — deploy ≠ merge)

Merging to `main` **does not** deploy. Only a **per-service tag** does:

```sh
git tag timing-v0.1.0 && git push origin timing-v0.1.0
# release.yml builds ONLY pitwall-timing (context = repo root) → pushes to public GHCR.
# Within one timer tick (~3 min) the VPS poller pulls it and recreates ONLY pitwall-timing.
```

First publish only: if the GHCR package starts **private**, flip it to **public** once
(GitHub → your packages → `pitwall-‹svc›` → Package settings → Change visibility → Public),
so the VPS can pull with no login. Subsequent versions stay public.

Watch a deploy on the VPS:

```sh
tail -f /root/pitwall-deploy.log
docker compose -p pitwall ps           # only pitwall-* containers
cat /root/pitwall.deployed_tags        # what is deployed per service
```

## 3. View the live board (loopback-only — SSH tunnel, no public ingress)

The board binds `127.0.0.1:<LEADERBOARD_HTTP_PORT>` on the VPS (Q29.3). From your workstation:

```sh
ssh -L 8090:127.0.0.1:8090 root@‹vps-host›       # forward the VPS loopback port locally
# then open http://127.0.0.1:8090  (live SSE standings; this is NOT a health endpoint)
```

Bus-only health (ADR-0004/NFR18) is unchanged in prod: liveness is the per-container Docker
**touch-file healthcheck** (fresh only after a successful heartbeat publish) — there is **no** HTTP
`/health`. Check it with `docker compose -p pitwall ps` (STATUS shows `(healthy)`).

## 4. Rollback (redeploy the previous immutable image — no server rebuild)

Images are immutable per version in GHCR, so rollback just re-pins the old tag and pulls it.
Because the poller deploys the **newest** tag, pause the timer first (or it will re-advance):

```sh
systemctl stop pitwall-deploy.timer
cd /root/pitwall
TIMING_VERSION=0.1.0 LEADERBOARD_VERSION=$(grep '^leaderboard=' /root/pitwall.deployed_tags | cut -d= -f2) \
  docker compose -p pitwall -f docker-compose.yml -f docker-compose.prod.yml pull timing
TIMING_VERSION=0.1.0 LEADERBOARD_VERSION=$(grep '^leaderboard=' /root/pitwall.deployed_tags | cut -d= -f2) \
  docker compose -p pitwall -f docker-compose.yml -f docker-compose.prod.yml up -d --no-build timing
# update the marker to the rolled-back version, then resume forward deploys when ready:
sed -i 's/^timing=.*/timing=0.1.0/' /root/pitwall.deployed_tags
# to go forward again, push a NEW higher tag (e.g. timing-v0.1.2) and:
systemctl start pitwall-deploy.timer
```

No image is ever rebuilt on the server — the old immutable image is pulled straight from GHCR.

## Files

| File | Role |
|---|---|
| `../docker-compose.prod.yml` | prod overlay — GHCR `image:` + `pull_policy: always` + `restart: always` (no server build) |
| `pitwall-deploy-poller.sh` | the pull poller (flock, fetch tags, select changed, `pull` + `up -d --no-build`, scoped `-p pitwall`) |
| `select-deploys.sh` | pure per-service newest-tag-vs-deployed selection (unit-tested) |
| `systemd/pitwall-deploy.service` · `.timer` | oneshot + 3-min timer that runs the poller |
| `../.github/workflows/release.yml` | tag-only CI build → public GHCR push (one service per tag) |
| `../scripts/parse-service-tag.sh` | shared `‹svc›-vX.Y.Z` parser (release + poller) |
| `../scripts/check-prod-compose.sh` · `check-release-workflow.sh` | the validity gates (`make prod-config`) |
