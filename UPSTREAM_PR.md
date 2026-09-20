# Upstream PR Guide (提交上游指南)

This document describes which files are **custom additions** and must be
**excluded** when submitting a PR to the upstream repository
(https://github.com/nadoo/glider).

## Files / Directories That Must NOT Be Submitted Upstream

### Custom add-on components (independent Go modules)

| Path | Description |
|------|-------------|
| `pacweb/` | CA certificate export + PAC hosting + multi-platform install guide service |
| `launcher/` | Centralized launcher that manages glider & pacweb processes |
| `rules/` | Personal domain-routing rule files (i2p.rule, direct.rule) |
| `scripts/` | Fork helper scripts (e.g. upstream patch generator) |
| `UPSTREAM_PR.md` | This guide itself |

### Local environment artifacts (already in .gitignore)

| Pattern | Description |
|---------|-------------|
| `*.exe` | Compiled binaries (glider.exe, pacweb.exe, launcher.exe) |
| `*.conf` | Local config files (glider.conf, pacweb.conf, launcher.conf) |
| `ca.crt`, `ca.pem` | Exported CA certificates (machine-specific) |
| `*.log` | Runtime logs |

## How to Prepare an Upstream PR

### Option A: Generate a clean patch (recommended)

Run the helper script to generate a patch that only contains glider
source changes (excluding custom directories):

```powershell
# Windows PowerShell
.\scripts\make-upstream-patch.ps1
```

The script will:
1. Compute the diff between `upstream/main` and current `HEAD`
2. Exclude changes under `pacweb/`, `launcher/`, `rules/`, and this guide
3. Output `upstream-pr.patch` in the repository root

Review the patch, then apply it to a clean branch cut from upstream:

```bash
git fetch upstream
git checkout -b upstream-pr upstream/main
git apply upstream-pr.patch
git add -A
git commit -m "<English commit message>"
git push origin upstream-pr
```

Then open the PR from `luminset/glider:upstream-pr` to `nadoo/glider:main`.

### Option B: Manual cherry-pick

1. Create a clean branch from upstream:
   ```bash
   git fetch upstream
   git checkout -b upstream-pr upstream/main
   ```
2. Cherry-pick only the glider source commits (exclude pacweb/launcher/rules):
   ```bash
   git checkout <commit-hash> -- proxy/ config/ main.go config.go ...
   ```
3. Commit with an **English** message and push.

## Commit Message Requirements

- **Language: English** (the upstream repository is English-only)
- Follow conventional commit format:
  - `fix: ...` for bug fixes
  - `feat: ...` for new features
  - `refactor: ...` for refactoring
  - `docs: ...` for documentation
- Include a clear description of what and why

## Custom Directory Naming Convention

To make exclusion easy and unambiguous, all custom additions are kept in
dedicated top-level directories:

- `pacweb/` — all PAC/cert service code
- `launcher/` — all process-launcher code
- `rules/` — all personal routing rules

Never mix custom code into upstream directories (e.g. do not add new
files under `proxy/` unless they are genuine upstream-worthy changes).

## Syncing with Upstream

```bash
git fetch upstream
git checkout main
git merge upstream/main
# Resolve conflicts if any, then:
git push origin main
```
