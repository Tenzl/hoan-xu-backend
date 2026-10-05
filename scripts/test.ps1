. "$PSScriptRoot/common.ps1"
Import-TaskEnv
$taskGo = Get-TaskTool go
if (-not $env:TEST_DATABASE_URL) { throw 'Set TEST_DATABASE_URL in .env to a separate database ending in _test.' }
Push-Location $TaskRoot
try { & $taskGo test ./... -count=1; Assert-TaskExit; & $taskGo vet ./...; Assert-TaskExit } finally { Pop-Location }
