# make-upstream-patch.ps1
# Generate a clean patch for upstream PR that excludes custom additions.
#
# Usage: .\scripts\make-upstream-patch.ps1
# Output: upstream-pr.patch (in repo root)
#
# Excluded paths:
#   pacweb/        - custom CA/PAC service
#   launcher/      - custom process launcher
#   rules/         - personal routing rules
#   UPSTREAM_PR.md - this guide itself

$ErrorActionPreference = 'Stop'

# Resolve repo root (parent of scripts/)
$repoRoot = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
Set-Location $repoRoot

Write-Host "=== Upstream Patch Generator ===" -ForegroundColor Cyan
Write-Host "Repo: $repoRoot"

# Verify upstream remote exists
$upstream = git remote get-url upstream 2>$null
if (-not $upstream) {
    Write-Host "[ERROR] 'upstream' remote not found. Add it with:" -ForegroundColor Red
    Write-Host "  git remote add upstream https://github.com/nadoo/glider.git"
    exit 1
}
Write-Host "Upstream: $upstream"

# Fetch latest upstream
Write-Host "Fetching upstream/main ..." -ForegroundColor Yellow
git fetch upstream main --quiet 2>$null

# Paths to exclude from the patch
$excludePaths = @(
    'pacweb/',
    'launcher/',
    'rules/',
    'scripts/',
    'UPSTREAM_PR.md'
)

# Build git diff pathspec exclusions (pathspec magic: exclude)
# Format: ':(exclude)path'
$excludes = $excludePaths | ForEach-Object { ":(exclude)$_" }

# Compute diff stat for review
Write-Host ""
Write-Host "=== Changes included in patch (excluding custom dirs) ===" -ForegroundColor Cyan
git diff --stat upstream/main...HEAD -- . $excludes

Write-Host ""
Write-Host "=== Excluded paths ===" -ForegroundColor Cyan
$excludePaths | ForEach-Object { Write-Host "  - $_" }

# Generate the patch
$patchFile = Join-Path $repoRoot 'upstream-pr.patch'
Write-Host ""
Write-Host "Generating patch -> $patchFile" -ForegroundColor Yellow

git diff upstream/main...HEAD -- . $excludes | Out-File -FilePath $patchFile -Encoding utf8

$patchSize = (Get-Item $patchFile).Length
Write-Host "Patch size: $patchSize bytes" -ForegroundColor Green

if ($patchSize -eq 0) {
    Write-Host "[WARN] Patch is empty — no glider-source changes found." -ForegroundColor Red
    Remove-Item $patchFile
    exit 0
}

Write-Host ""
Write-Host "=== Next steps ===" -ForegroundColor Cyan
Write-Host "1. Review the patch:  Get-Content $patchFile"
Write-Host "2. Apply to clean branch:"
Write-Host "     git fetch upstream"
Write-Host "     git checkout -b upstream-pr upstream/main"
Write-Host "     git apply upstream-pr.patch"
Write-Host "     git add -A"
Write-Host "     git commit -m `"<English commit message>`""
Write-Host "     git push origin upstream-pr"
Write-Host ""
Write-Host "Done." -ForegroundColor Green
