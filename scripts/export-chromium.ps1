param([Parameter(Mandatory=$true)][string]$Destination)
$ErrorActionPreference='Stop'
$taskSource=[IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../chromium'))
$taskDestination=[IO.Path]::GetFullPath($Destination)
if ($taskDestination -eq $taskSource) { throw 'Choose a separate export directory.' }
$taskFiles=@('.dockerignore','.env.example','.gitignore','Dockerfile','compose.yaml','README.md','apparmor/hoanxu-chromium','scripts/start.sh','scripts/healthcheck.sh','ssh/authorized_keys.example','ssh/sshd_config.conf')
foreach ($taskFile in $taskFiles) {
    $taskInput=Join-Path $taskSource $taskFile
    $taskOutput=Join-Path $taskDestination $taskFile
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $taskOutput) | Out-Null
    Copy-Item -LiteralPath $taskInput -Destination $taskOutput -Force
    if ((Get-FileHash -LiteralPath $taskInput).Hash -ne (Get-FileHash -LiteralPath $taskOutput).Hash) { throw "Export mismatch: $taskFile" }
}
Write-Host "Exported $($taskFiles.Count) public Chromium files to $taskDestination."
