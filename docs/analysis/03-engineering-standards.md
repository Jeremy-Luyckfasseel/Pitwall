# Pitwall — Engineering Standards & Code-Quality Rules

> The rules that make a **polyglot, solo-built** system feel like one well-engineered
> platform. These apply to **every** service. Traces to
> [`00-questions-and-answers.md`](./00-questions-and-answers.md) Rounds 2, 3, 11.
>
> Golden rule for working in this repo: **never assume.** If a requirement is not
> recorded in the Q&A log, ask before building. See [`/CLAUDE.md`](../../CLAUDE.md).

## 1. Quality bar

This is a portfolio project whose purpose is to *demonstrate* production-grade
craftsmanship. "Done" means: tested, linted, documented, observable, containerised,
contract-conformant, and with every sad path handled. Code should read like the rest
of its service — match local idioms, naming, and comment density.

## 2. Repository layout (monorepo)

```
/
├── CLAUDE.md                      # agent + contributor operating rules (no assumptions)
├── docker-compose.yml             # local dev/staging: full stack
├── docker-compose.prod.yml        # production overlay (VPS)
├── .env.example                   # documents every required variable (no secrets)
├── contract/                      # the ONLY shared coupling point (JSON Schemas)
├── docs/
│   ├── pitwall_brief.md
│   ├── analysis/                  # this analysis phase
│   │   ├── 00-questions-and-answers.md
│   │   ├── 01-architecture-overview.md
│   │   ├── 02-message-bus-and-contracts.md
│   │   ├── 03-engineering-standards.md
│   │   ├── 04-service-blueprint.md
│   │   └── services/<service>.md
│   └── adr/                       # architecture decision records
└── services/
    ├── timing/        ├── identity/   ├── driver/      ├── crm/
    ├── booking/       ├── frontend/   ├── billing/     ├── mailing/
    ├── leaderboard/   ├── bar-pos/    └── control-room/
```

Each `services/<name>/` is self-contained: its own code, `Dockerfile`, tests,
`README.md`, and dependency manifest. A service may pick **any language/framework/
database** (per-service freedom) provided it honours the **service blueprint**
([04](./04-service-blueprint.md)) and the **contract**.

## 3. Testing strategy (CI-gated)

Every service must ship:

1. **Unit tests** — pure logic (lap calculation, capacity checks, invoice numbering,
   conflict resolution), no I/O.
2. **Integration tests** — against a **real RabbitMQ + real database** via
   testcontainers (or equivalent). Prove publish/consume, outbox flush, inbox dedupe,
   and DLQ routing actually work.
3. **Contract tests** — validate **every** message the service publishes *and*
   consumes against the JSON Schemas in `/contract`. The build fails on any drift.
4. **End-to-end smoke test** — at least the happy path across the bus
   (check-in → lap → leaderboard update → session end → invoice → email) runs green in
   CI using the simulators.

**Test-first (TDD) is the working method (Round 24).** Implement every story
**red → green → refactor**: write the failing test(s) **first** — derived directly from the
story's **Given/When/Then** acceptance criteria — watch them fail, write the *minimum* code to
pass, then refactor under the green. The four layers above are the **what**; TDD is the **how**
(the order tests are written). The `/contract` valid + known-bad fixtures are themselves
test-first artifacts. No production code lands without a failing test that motivated it. *(Story
1.1 — scaffold/infra — predates this; from Story 1.2 onward it applies.)*

CI **gates** on all four. No merge to `main` with a red pipeline (GitHub Flow, Round 21).

## 4. Code style & enforcement

- **Per-language standard toolchain**, e.g. ESLint+Prettier (JS/TS), Ruff+Black
  (Python), gofmt+golangci-lint (Go), dotnet format/analyzers (C#). Pick the canonical
  one for whatever language a service uses.
- **Pre-commit hooks** run formatter + linter + the contract validator locally.
- **CI gate** re-runs lint/format/type-check/tests — local hooks are a convenience,
  CI is the authority.
- **Conventional Commits** (`feat:`, `fix:`, `chore(timing):` …) drive readable
  history and **automated per-service versioning + changelogs**.

## 5. Observability

- **Structured JSON logs** on every service: `timestamp`, `level`, `service`,
  `correlationId`, `eventId?`, `message`, plus structured fields. No bare `print`.
- **Correlation id** is created at the start of a flow and propagated on every
  downstream event (envelope `correlationId`) and every log line, so one driver's
  journey is followable across all services.
- **Errors on bad data are logged**, the offending message dead-lettered — never
  silently dropped.
- No heavy central log/metrics stack required; the Control Room surfaces aggregate
  stats from the event stream.

## 6. CI/CD & deployment

| Stage | Behaviour |
|---|---|
| **On PR to `main`** (from a `story/*` branch) | lint + type-check + unit + integration + contract + smoke + **security scans** (below); build images. **No merge on red.** GitHub Flow — no long-lived `dev` branch (Round 21); integration runs locally via Compose. |
| **On push to `main`** (squash-merge) | same gates; `main` is the always-green release line. Merging does **not** deploy. |
| **On per-service tag** `‹svc›-vX.Y.Z` | CI builds that service's image, pushes to **GHCR**, then the **VPS pulls** and `docker compose up -d` recreates **only that container**. Independent per-team releases in one repo. |
| **Security scanning** (Round 23) | Secret scanning (GitHub native + push-protection; gitleaks fallback) — **blocking**. SAST (CodeQL) — **blocking on high/critical**. Dependency/SCA (Dependabot + govulncheck/pip-audit/npm audit) and container image scan (Trivy) — **advisory** (Security tab). Phased in per language (Go @ 1.3, Python @ 3.1, TS @ 4.x/5.x); secret scanning applies from the scaffold. |
| **Secrets** | `.env` on the VPS (and locally); CI holds only the GHCR push (automatic `GITHUB_TOKEN`). |
| **Rollback** | redeploy the previous tag's image (immutable images in GHCR). |

> **Realized deploy mechanism (Story 1.12 / [Q29.1](00-questions-and-answers.md#round-29)):** the
> "VPS pulls" step is a **pull-based poller** (a systemd timer on the VPS pulls the new public-GHCR
> image and recreates only the changed container) — **not** CI-SSHes-in. So **CI holds no SSH deploy
> creds**; the only CI credential is the GHCR push via the automatic `GITHUB_TOKEN` (this **supersedes**
> the older "CI holds … SSH deploy creds" wording above). GHCR images are **public**, so the VPS pulls
> with no login. Board reachability in prod is **loopback + SSH tunnel** ([Q29.3](00-questions-and-answers.md#round-29)).

Branch→environment and tag→release rationale:
[ADR-0007](../adr/0007-monorepo-per-service-deploy.md).

## 7. Security & config

- `.env.example` committed (placeholders only); real `.env` files **gitignored**.
- No secrets in code, logs, or images.
- Credentials/passwords live **only in Frontend** (auth owner); Identity and all other
  services never see them.
- Inter-service trust is via the bus; there are **no inter-service APIs** to
  authenticate (the single Control Room→Mailing bus-down exception aside).

### Automated security scanning (Round 23)

Beyond the secure-by-design rules above, CI **scans for what slips through anyway** — four
categories, GitHub-native-first + Trivy, **phased in as the code to scan appears** (walking-skeleton-first):

- **Secret scanning** — GitHub secret scanning + **push protection** (native); **gitleaks** is the
  portable CI gate where native GHAS isn't available (e.g. a private repo). **Blocking.** Applies from
  the scaffold (a repo can leak a secret before any service exists). *(Open sub-detail, not assumed:
  repo visibility / GHAS availability — confirm at enablement.)*
- **Dependency / SCA** — **Dependabot** alerts + update PRs, backed by a per-language vuln check in CI:
  **govulncheck** (Go), **pip-audit** (Python), **npm audit** (TS). **Advisory** (reports, does not block).
- **SAST** — **CodeQL** (Go / Python / JS-TS). **Blocking on high/critical** findings; lower severities
  advisory.
- **Container images** — **Trivy** scans the built service images for OS/package CVEs. **Advisory.**

Phasing: secret scanning now; CodeQL + govulncheck + Trivy at **Story 1.3** (first Go service), CodeQL
Python + pip-audit at **3.1**, CodeQL JS/TS + npm audit at the **TS tier (4.x/5.x)**. Each scan is
path-filtered like the rest of CI (AR17). Rationale + alternatives weighed:
[Q&A Round 23](00-questions-and-answers.md#round-23--security-ci-scanning-scope-tooling-phasing--enforcement-2026-06-04).
This is an engineering-standards addition (no service/bus/`/contract` change) → **no ADR**.

## 8. Definition of Done (per change)

- [ ] Conforms to the **service blueprint** ([04](./04-service-blueprint.md)).
- [ ] **Built test-first** (TDD, Round 24): tests written before the code, red→green→refactor,
      derived from the story's Given/When/Then ACs.
- [ ] All four test layers pass in CI; coverage of new logic.
- [ ] Messages validate against `/contract`; new/changed events have schemas +
      examples committed.
- [ ] Lint/format/type-check clean; Conventional Commit message.
- [ ] Sad paths for the change are handled and reflected in the service's sad-path
      table.
- [ ] Structured logs with correlation id; no secrets leaked.
- [ ] Security scans green for the change: no detected secret, no new high/critical SAST
      finding (dependency/image findings triaged, not necessarily blocking) — Round 23.
- [ ] Service `README.md` and the relevant analysis doc updated if behaviour changed.
- [ ] A decision that changes architecture gets an **ADR**.
