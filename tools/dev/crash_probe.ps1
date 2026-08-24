$p = Start-Process -FilePath "D:\pyx\AI_MIDI-go\bin\aimidi-engine.exe" -PassThru -NoNewWindow
$p.WaitForExit(5000)
Write-Output ("HasExited=" + $p.HasExited + " ExitCode=" + $p.ExitCode)
