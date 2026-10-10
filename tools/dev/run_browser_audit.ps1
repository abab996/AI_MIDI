$ErrorActionPreference = 'Stop'
$workspace = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
$artifactDir = Join-Path $workspace 'output/audit-2026-10-01'
$serverFile = Join-Path $artifactDir 'serve-audit-next.exe'
foreach ($connection in (Get-NetTCPConnection -LocalPort 7912 -State Listen -ErrorAction SilentlyContinue)) {
    $previous = Get-Process -Id $connection.OwningProcess -ErrorAction SilentlyContinue
    if ($previous -and ((Split-Path $previous.Path) -eq $artifactDir) -and ($previous.ProcessName -like 'serve-audit*')) {
        Stop-Process -Id $previous.Id
        $previous.WaitForExit(5000) | Out-Null
    } elseif ($previous) { throw 'Port 7912 belongs to a process outside this audit.' }
}
$server = Start-Process -FilePath $serverFile -WorkingDirectory $workspace -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $artifactDir 'serve.stdout.log') -RedirectStandardError (Join-Path $artifactDir 'serve.stderr.log')
try {
    Push-Location $workspace
    node tools/dev/browser_audit_test.mjs
    if ($LASTEXITCODE -ne 0) { throw "Browser audit failed ($LASTEXITCODE)." }
} finally {
    Pop-Location
    if (-not $server.HasExited) { Stop-Process -Id $server.Id; $server.WaitForExit(5000) | Out-Null }
}
