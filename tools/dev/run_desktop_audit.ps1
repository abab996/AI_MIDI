$ErrorActionPreference = 'Stop'
$workspace = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
$desktopDir = Join-Path $workspace 'output/audit-2026-10-01/desktop'
$desktopFile = Join-Path $desktopDir 'MelodyMate-audit-smoke.exe'
if (Get-Process -Name 'MelodyMate*','melodymate-engine' -ErrorAction SilentlyContinue) {
    throw 'Desktop smoke requires no existing MelodyMate session; existing processes are preserved.'
}
New-Item -ItemType Directory -Path (Join-Path $desktopDir 'bin') -Force | Out-Null
Copy-Item -LiteralPath (Join-Path $workspace 'build/bin/MelodyMate-audit.exe') -Destination (Join-Path $desktopDir 'MelodyMate-audit.exe')
Copy-Item -LiteralPath (Join-Path $workspace 'engine/build/melodymate-engine_artefacts/Release/melodymate-engine.exe') -Destination (Join-Path $desktopDir 'bin/melodymate-engine.exe')
Copy-Item -LiteralPath (Join-Path $workspace 'output/audit-2026-10-01/MelodyMate-audit-smoke.exe') -Destination $desktopFile
# A pre-existing isolated settings file suppresses automatic import of user data.
$settingsFile = Join-Path $desktopDir 'settings.json'
if (-not (Test-Path -LiteralPath $settingsFile)) {
    '{"providers":[],"audio":{"engine_enabled":true},"material_dirs":[]}' | Set-Content -LiteralPath $settingsFile -Encoding utf8NoBOM
}
foreach ($kind in @('gain','synth')) {
    $pluginDir = Join-Path $desktopDir "Library/plugins/com.melodymate.example.$kind@1.0.0"
    New-Item -ItemType Directory -Path (Join-Path $pluginDir 'bin/win-x64') -Force | Out-Null
    Copy-Item -LiteralPath (Join-Path $workspace "engine/mm-sdk/examples/$kind/manifest.json") -Destination $pluginDir
    Copy-Item -LiteralPath (Join-Path $workspace "engine/mm-sdk/examples/$kind/ui") -Destination $pluginDir -Recurse -Force
    Copy-Item -LiteralPath (Join-Path $workspace "engine/build/bin/Release/mm_example_$kind.dll") -Destination (Join-Path $pluginDir 'bin/win-x64')
}
New-Item -ItemType Directory -Path (Join-Path $desktopDir 'Library/soundfonts') -Force | Out-Null
Copy-Item -LiteralPath (Join-Path $workspace 'Library/soundfonts/GeneralUser GS v1.471.sf2') -Destination (Join-Path $desktopDir 'Library/soundfonts')
$vstDir = Join-Path $desktopDir 'Library/vst3'
New-Item -ItemType Directory -Path $vstDir -Force | Out-Null
foreach ($vst in @('AuditGain','AuditSynth')) {
    Copy-Item -LiteralPath (Join-Path $workspace "engine/build/${vst}_artefacts/Release/VST3/$vst.vst3") -Destination $vstDir -Recurse -Force
}
$previousArgs = $env:WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS
$env:WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS = '--remote-debugging-port=9237'
$desktop = Start-Process -FilePath $desktopFile -WorkingDirectory $desktopDir -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $desktopDir 'desktop.stdout.log') -RedirectStandardError (Join-Path $desktopDir 'desktop.stderr.log')
try {
    Push-Location $workspace
    node tools/dev/desktop_audit_test.mjs
    if ($LASTEXITCODE -ne 0) { throw "Desktop audit failed ($LASTEXITCODE)." }
    if (-not $desktop.WaitForExit(10000)) { throw 'Desktop did not shut down gracefully.' }
    $desktop = Start-Process -FilePath $desktopFile -WorkingDirectory $desktopDir -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $desktopDir 'reopen.stdout.log') -RedirectStandardError (Join-Path $desktopDir 'reopen.stderr.log')
    node tools/dev/desktop_audit_test.mjs --reopen
    if ($LASTEXITCODE -ne 0) { throw "Desktop reopen audit failed ($LASTEXITCODE)." }
} finally {
    Pop-Location
    $env:WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS = $previousArgs
    if (-not $desktop.HasExited) {
        $desktop.CloseMainWindow() | Out-Null
        if (-not $desktop.WaitForExit(5000)) { Stop-Process -Id $desktop.Id; $desktop.WaitForExit(5000) | Out-Null }
    }
}
