# ADR-0002: Origin owns the repository root; the existing site moves to `site/`

**Status:** accepted · **Date:** 2026-08-28 · **Decided by:** implementer (user delegated)

## Context
The repository was expected to be empty but already contained a static website
(`index.html`, `script.js`, `style.css`, `styles.css`, and CNAME history). The user
declined to choose a layout.

## Options considered
- **Move the site to `site/`, Origin at root.** Preserves the site's git history via
  `git mv`; the repository root then describes the project; a GitHub Pages deployment
  needs its source directory repointed at `site/`.
- **Origin under `origin/`.** Leaves the site untouched, but every path in `CLAUDE.md`,
  `./check`, `go.mod` and every future tool config carries a prefix, and the repository
  root stops describing what the repository is.
- **Delete the site.** Irreversible from `main`'s perspective; nothing established the
  site was dead.

## Decision
`git mv` the four web files into `site/`. Origin owns the root: `cmd/`, `internal/`,
`docs/`, `tests/`, `bootstrap/`, `check`.

## Consequences
- If the site was published from the repository root, its Pages source must be changed
  to `/site` — a settings change, no content change.
- Reversing this is one `git mv` away; nothing was deleted.
