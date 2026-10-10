$ErrorActionPreference = 'Stop'
$workspace = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
$out = Join-Path $workspace 'output/audit-2026-10-01'
$native = Join-Path $out 'native'
New-Item -ItemType Directory -Path $native -Force | Out-Null
$files = @{
    'melodymate-engine.exe' = 'engine/build/melodymate-engine_artefacts/Release/melodymate-engine.exe'
    'audio-regression.exe' = 'engine/build/audio-regression_artefacts/Release/audio-regression.exe'
    'engine-regression.exe' = 'engine/build/engine-regression_artefacts/Release/engine-regression.exe'
    'mm_example_gain.dll' = 'engine/build/bin/Release/mm_example_gain.dll'
    'mm_example_synth.dll' = 'engine/build/bin/Release/mm_example_synth.dll'
    'mm_services_fixture.dll' = 'engine/build/Release/mm_services_fixture.dll'
}
foreach ($name in $files.Keys) {
    $source = Join-Path $workspace $files[$name]
    $destination = Join-Path $native $name
    if ((Test-Path -LiteralPath $destination) -and ((Get-FileHash -LiteralPath $source).Hash -eq (Get-FileHash -LiteralPath $destination).Hash)) { continue }
    Copy-Item -LiteralPath $source -Destination $destination
}
New-Item -ItemType Directory -Path (Join-Path $native 'VST3') -Force | Out-Null
foreach ($fixture in @('AuditGain', 'AuditSynth')) {
    Copy-Item -LiteralPath (Join-Path $workspace "engine/build/${fixture}_artefacts/Release/VST3/${fixture}.vst3") -Destination (Join-Path $native 'VST3') -Recurse -Force
}
New-Item -ItemType Directory -Path (Join-Path $out 'audio') -Force | Out-Null
foreach ($name in @('mm.wav','vst.wav')) { Copy-Item -LiteralPath (Join-Path $workspace "engine/build/test-audio/$name") -Destination (Join-Path $out "audio/$name") }
$desktopEngine = Join-Path $out 'desktop/bin/melodymate-engine.exe'
if ((Get-FileHash -LiteralPath $desktopEngine).Hash -ne (Get-FileHash -LiteralPath (Join-Path $native 'melodymate-engine.exe')).Hash) { throw 'Desktop engine is not synchronized with the tested native engine.' }
$artifacts = @(Get-ChildItem -LiteralPath $native -Recurse -File)
$artifacts += @(Get-ChildItem -LiteralPath (Join-Path $out 'audio') -File)
$artifacts += @(Get-Item -LiteralPath (Join-Path $out 'desktop/MelodyMate-audit.exe'), (Join-Path $out 'desktop/MelodyMate-audit-smoke.exe'), $desktopEngine)
$rows = foreach ($file in $artifacts) { [ordered]@{path=[IO.Path]::GetRelativePath($out,$file.FullName).Replace('\','/');bytes=$file.Length;sha256=(Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash.ToLowerInvariant()} }
$manifest = [ordered]@{builtAt=(Get-Date).ToString('o');platform='windows/amd64';branch='codex/fix-audit-2026-10-01';mainBaseline='78e4088';engineBaseline='1388692';artifacts=@($rows | Sort-Object path)}
$manifest | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath (Join-Path $out 'artifacts.json') -Encoding utf8NoBOM
Write-Output "Packaged $($artifacts.Count) synchronized artifacts."
