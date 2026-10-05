. "$PSScriptRoot/common.ps1"
foreach ($name in @('go','psql','pg_dump','pg_restore')) {
    try { Write-Host "$name : $(Get-TaskTool $name)" } catch { Write-Warning $_.Exception.Message }
}
$chrome = @('C:/Program Files/Google/Chrome/Application/chrome.exe','C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe') | Where-Object { Test-Path -LiteralPath $_ } | Select-Object -First 1
if ($chrome) { Write-Host "Chromium-compatible browser: $chrome" } else { Write-Warning 'Set CHROME_PATH to your Chromium executable.' }
Write-Host 'Dependencies are checked only; no system settings were changed.'
Write-Host 'Next: configure .env, run scripts/migrate.ps1, then scripts/dev.ps1 from the backend repository.'
