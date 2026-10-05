. "$PSScriptRoot/common.ps1"
Import-TaskEnv
$taskPort = if ($env:PORT) { [int]$env:PORT } else { 8080 }
if (Get-NetTCPConnection -LocalPort $taskPort -State Listen -ErrorAction SilentlyContinue) { throw "Port $taskPort is occupied. Stop the existing backend first." }
$taskGo = Get-TaskTool go
$taskBin = Join-Path $TaskRoot '.tools/bin'
$taskLogs = Join-Path $TaskRoot 'private-data/logs'
New-Item -ItemType Directory -Force $taskBin,$taskLogs | Out-Null
Push-Location $TaskRoot
try { & $taskGo build -o (Join-Path $taskBin 'hoanxu-api.exe') ./cmd/api; Assert-TaskExit } finally { Pop-Location }
$taskProcesses = @()
try {
    $taskProcesses += Start-Process -FilePath (Join-Path $taskBin 'hoanxu-api.exe') -WorkingDirectory $TaskRoot -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $taskLogs 'api.log') -RedirectStandardError (Join-Path $taskLogs 'api-error.log')
    $taskReady = $false
    for ($taskAttempt=0; $taskAttempt -lt 30; $taskAttempt++) {
        foreach ($process in $taskProcesses) { if ($process.HasExited) { throw "Application process exited. Read logs in $taskLogs." } }
        try {
            $null = Invoke-RestMethod -Uri "http://127.0.0.1:$taskPort/readyz" -TimeoutSec 2
            $taskReady = $true
            break
        } catch { Start-Sleep -Milliseconds 300 }
    }
    if (-not $taskReady) { throw "Startup timed out. Read logs in $taskLogs." }
    Write-Host "API: http://127.0.0.1:$taskPort/healthz. Start the frontend separately from its repository."
    Write-Host "Logs: $taskLogs"
    Read-Host 'Press Enter to stop the backend process' | Out-Null
} finally {
    foreach ($process in $taskProcesses) {
        if (-not $process.HasExited) { & taskkill.exe /PID $process.Id /T /F | Out-Null }
    }
    Write-Host 'Application stopped. PostgreSQL service was not stopped.'
}
