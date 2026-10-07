. "$PSScriptRoot/common.ps1"
foreach ($name in @('go','psql','pg_dump','pg_restore')) {
    try { Write-Host "$name : $(Get-TaskTool $name)" } catch { Write-Warning $_.Exception.Message }
}
$taskTestingRoot = Join-Path $env:LOCALAPPDATA 'ms-playwright'
$taskTestingBrowsers = @(Get-ChildItem -LiteralPath $taskTestingRoot -Directory -Filter 'chromium-*' -ErrorAction SilentlyContinue | ForEach-Object {
    foreach ($taskRelativePath in @('chrome-win64/chrome.exe','chrome-win/chrome.exe')) {
        $taskCandidate = Join-Path $_.FullName $taskRelativePath
        if (Test-Path -LiteralPath $taskCandidate) { Get-Item -LiteralPath $taskCandidate }
    }
})
$taskTestingBrowser = $taskTestingBrowsers | Sort-Object LastWriteTime -Descending | Select-Object -First 1
if ($taskTestingBrowser) { Write-Host "Chrome for Testing: $($taskTestingBrowser.FullName)" } else { Write-Warning 'Chrome for Testing was not found. Configure its executable in the Shopee admin form.' }
Write-Host 'Dependencies are checked only; no system settings were changed.'
Write-Host 'Next: configure .env.local, run scripts/migrate.ps1, then scripts/dev.ps1 from the backend repository. Select Chrome for Testing and the existing profile in the Shopee admin form.'
