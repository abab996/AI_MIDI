$p = Start-Process -FilePath (Join-Path (Split-Path -Parent $PSScriptRoot) "bin\melodymate-engine.exe") -PassThru -NoNewWindow
$p.WaitForExit(5000)
Write-Output ("HasExited=" + $p.HasExited + " ExitCode=" + $p.ExitCode)
