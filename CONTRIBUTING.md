# Contributing to Ongrid

Thanks for your interest in contributing! 🙌 This guide covers the few
conventions that keep the project tidy. By participating, you agree to abide
by our [Code of Conduct](CODE_OF_CONDUCT.md).

## Where things go

- **Code** lives in this repo (Go backend under `internal/` + `cmd/`,
  React frontend under `web/`).
- **Docs** live under `docs/` — **not** in the README. The README is kept
  intentionally minimal and is maintained in **9 languages**, so an
  English-only addition would drift out of sync. Operational / deployment
  guides go under `docs/install/`, design docs under `docs/design/`.
- **ADR / HLD** (substantial design decisions) go under `docs/design/`.

## Dev setup

```bash
# Full stack on Docker (manager + Prometheus + Loki + Tempo + Grafana)
cp deploy/.env.example deploy/.env   # set admin account + one model API key
make compose-up                      # make compose-down to stop

# Build binaries
make build              # ongrid + ongrid-edge
make build-ongrid-edge  # just the edge agent

# Frontend
cd web && npm install && npm run build
```

## Before you open a PR

- **Run the checks**: `go test ./...` and, if you touched the UI,
  `npm run build` in `web/`.
- **One logical change per PR.** Keep it focused.
- **Conventional commit messages**: `feat(scope): …`, `fix(scope): …`,
  `docs(scope): …`, `chore(scope): …`.
- **Confirm the contribution terms** by checking the required box in the PR
  template. PRs cannot be merged while that check is missing.
- **Link your commit email to your GitHub account** (Settings → Emails) so
  your commits are attributed to you in the contributors graph. If your
  author email isn't on your account, GitHub can't credit you.

## Pull requests

- `main` is protected: **all changes go through a PR** (no direct pushes,
  no force-pushes).
- PRs from a fork are welcome. Branch from `main`; a topic branch (e.g.
  `fix/tunnel-logs`) is preferred over committing to your fork's `main`.
- A maintainer will review. Be patient and responsive to feedback.

## License and contribution terms

The Ongrid community edition is licensed under the project's
[AGPLv3](LICENSE) license.

The Ongrid name, logo, domain names, and related brand assets are not licensed
under AGPLv3. See [TRADEMARK.md](TRADEMARK.md) for brand usage rules.

By submitting a pull request, patch, issue comment containing code, or any
other contribution to this repository, you agree that:

- your contribution may be distributed as part of the Ongrid community edition
  under AGPLv3;
- you retain the copyright to your contribution, but grant Ongrid a perpetual,
  worldwide, non-exclusive, irrevocable, royalty-free, sublicensable license to
  use, reproduce, modify, distribute, publicly perform, publicly display, and
  create derivative works from your contribution;
- Ongrid may use your contribution in open source, enterprise, hosted, SaaS,
  and other commercial offerings;
- you have the right to submit the contribution, and it does not include
  third-party code, confidential material, credentials, or company-owned code
  that you are not authorized to contribute.

Maintainers may request an additional written contributor agreement for large
changes, security-sensitive code, Agent execution logic, or company-sponsored
contributions.

## Reporting bugs / security issues

- **Bugs / features** → open a GitHub issue.
- **Security vulnerabilities** → do **not** open a public issue. See
  [SECURITY.md](SECURITY.md).
