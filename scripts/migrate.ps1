. "$PSScriptRoot/common.ps1"
Import-TaskEnv
$taskGo = Get-TaskTool go
Push-Location $TaskRoot
try { & $taskGo run ./cmd/admin migrate; Assert-TaskExit } finally { Pop-Location }
