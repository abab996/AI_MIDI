$ErrorActionPreference = 'Stop'
$workspace = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
Push-Location $workspace
try {
    npm run build
    if ($LASTEXITCODE -ne 0) { throw 'Frontend build failed.' }
    wails build -m -nosyncgomod -o MelodyMate-audit.exe
    if ($LASTEXITCODE -ne 0) { throw 'Production desktop build failed.' }
    # Wails' loader disables environment CDP flags. Use an isolated dependency
    # copy for the smoke binary; never alter go.mod, the module cache or production.
    $module = go list -m -json github.com/wailsapp/go-webview2 | ConvertFrom-Json
    $instrumentation = Join-Path $workspace 'output/audit-2026-10-01/desktop-instrumentation'
    New-Item -ItemType Directory -Path $instrumentation -Force | Out-Null
    $copy = Join-Path $instrumentation 'go-webview2'
    if (-not (Test-Path -LiteralPath $copy)) { Copy-Item -LiteralPath $module.Dir -Destination $copy -Recurse }
    $loader = Join-Path $copy 'webviewloader/env_create.go'
    (Get-Item -LiteralPath $loader).IsReadOnly = $false
    $content = Get-Content -LiteralPath (Join-Path $module.Dir 'webviewloader/env_create.go') -Raw
    $content = $content.Replace('os.Setenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS", "")', '// Audit binary only: retain the loopback CDP argument.')
    Set-Content -LiteralPath $loader -Value $content -Encoding utf8NoBOM -NoNewline
    Copy-Item -LiteralPath go.mod -Destination (Join-Path $instrumentation 'audit.mod')
    Copy-Item -LiteralPath go.sum -Destination (Join-Path $instrumentation 'audit.sum')
    go mod edit '-modfile=output/audit-2026-10-01/desktop-instrumentation/audit.mod' "-replace=github.com/wailsapp/go-webview2=$copy"
    if ($LASTEXITCODE -ne 0) { throw 'Smoke modfile preparation failed.' }
    go build '-modfile=output/audit-2026-10-01/desktop-instrumentation/audit.mod' '-tags=desktop,production' '-ldflags=-H windowsgui' -o output/audit-2026-10-01/MelodyMate-audit-smoke.exe .
    if ($LASTEXITCODE -ne 0) { throw 'Smoke desktop build failed.' }
} finally { Pop-Location }
