param([string]$Destination)
. "$PSScriptRoot/common.ps1"
Set-TaskPostgres
if (-not $Destination) { $Destination = Join-Path $TaskRoot ('private-data/backups/hoanxu-' + (Get-Date -Format 'yyyyMMdd-HHmmss') + '.dump') }
$resolved = [IO.Path]::GetFullPath($Destination)
if (Test-Path -LiteralPath $resolved) { throw 'Destination already exists; refusing to overwrite.' }
New-Item -ItemType Directory -Force ([IO.Path]::GetDirectoryName($resolved)) | Out-Null
& (Get-TaskTool pg_dump) --format=custom --no-owner --file=$resolved
Assert-TaskExit
Write-Host "Backup: $resolved"
Write-Host 'Back up private-data files and DATA_ENCRYPTION_KEY separately; the key is needed to read encrypted bank/voucher data.'
