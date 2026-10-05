param([Parameter(Mandatory=$true)][string]$Backup,[Parameter(Mandatory=$true)][string]$TargetDatabase)
. "$PSScriptRoot/common.ps1"
if ($TargetDatabase -notmatch '^[a-z][a-z0-9_]{1,62}$') { throw 'Target database name is invalid.' }
Set-TaskPostgres -SchemaOwner
if ($TargetDatabase -eq $env:PGDATABASE) { throw 'Refusing to restore over the active application database. Choose a new database.' }
$resolved = [IO.Path]::GetFullPath($Backup)
if (-not (Test-Path -LiteralPath $resolved)) { throw 'Backup does not exist.' }
$taskPsql = Get-TaskTool psql
$exists = & $taskPsql -d postgres -tAc "SELECT 1 FROM pg_database WHERE datname='$TargetDatabase'"
Assert-TaskExit
if ($exists -eq '1') {
    $tables = & $taskPsql -d $TargetDatabase -tAc "SELECT count(*) FROM pg_tables WHERE schemaname='public'"
    Assert-TaskExit
    if ([int]$tables -gt 0) { throw 'Target database is not empty; refusing to overwrite.' }
} else { & (Get-TaskTool createdb) $TargetDatabase; Assert-TaskExit }
& (Get-TaskTool pg_restore) --no-owner --exit-on-error --dbname=$TargetDatabase $resolved
Assert-TaskExit
Write-Host "Restored into $TargetDatabase. Active application configuration was not changed."
