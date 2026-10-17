# Upstream PR Guide (提交上游指南)

This document describes which files are **custom additions** and must be
**excluded** when submitting a PR to the upstream repository
(https://github.com/nadoo/glider).

## Files / Directories That Must NOT Be Submitted Upstream

### Custom add-on components (independent Go modules)

| Path | Description |
|------|-------------|
| `rules/` | Personal domain-routing rule files (i2p.rule, direct.rule) |
| `scripts/` | Fork helper scripts (e.g. upstream patch generator) |
| `UPSTREAM_PR.md` | This guide itself |
| `AGENTS.md` | Fork-local working rules (commit hygiene, repo conventions) |

The two custom components used to live here and have since been **moved out of the
repository** (they are standalone Go modules and now live in the workspace's `extra/`
folder):

| Former path | What it was |
|------|-------------|
| `pacweb/` | CA certificate export + PAC hosting + multi-platform install guide service |
| `launcher/` | Centralized launcher that manages glider & pacweb processes |

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
2. Exclude changes under `rules/`, `scripts/`, and this guide
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
2. Cherry-pick only the glider source commits (exclude `rules/` and `scripts/`):
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

## Custom Code Naming Convention

To keep exclusion easy and unambiguous:

- custom **components** (independent programs) do **not** live in this repository at all —
  `pacweb/` and `launcher/` were moved to the workspace's `extra/` folder
- only small fork-local support files stay here: `rules/` (personal routing rules) and
  `scripts/` (fork helper scripts)

Never mix custom code into upstream directories (e.g. do not add new
files under `proxy/` unless they are genuine upstream-worthy changes).

## Syncing with Upstream

`main` is a pure mirror of upstream; work happens on `nohafix`.

```bash
git fetch upstream
git switch main && git merge --ff-only upstream/main   # fast-forward the mirror
git push origin main
git switch nohafix && git rebase upstream/main          # replay our commits
git push --force-with-lease origin nohafix
```
