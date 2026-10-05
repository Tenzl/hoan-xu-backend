$ErrorActionPreference = 'Stop'
$TaskRoot = Split-Path -Parent $PSScriptRoot
function Get-TaskTool([string]$Name) {
    $found = Get-Command $Name -ErrorAction SilentlyContinue
    if ($found) { return $found.Source }
    if ($Name -eq 'go') {
        $candidate = Join-Path $TaskRoot '.tools/go/bin/go.exe'
        if (Test-Path -LiteralPath $candidate) { return $candidate }
    }
    if ($Name -in @('psql','pg_dump','pg_restore','createdb')) {
        $candidate = Get-ChildItem 'C:/Program Files/PostgreSQL/*/bin' -Directory -ErrorAction SilentlyContinue | Sort-Object FullName -Descending | ForEach-Object { Join-Path $_.FullName ($Name + '.exe') } | Where-Object { Test-Path -LiteralPath $_ } | Select-Object -First 1
        if ($candidate) { return $candidate }
    }
    throw "Missing $Name. Read README.md for installation instructions."
}
function Import-TaskEnv {
    $path = if ($env:ENV_FILE) { if ([IO.Path]::IsPathRooted($env:ENV_FILE)) { $env:ENV_FILE } else { Join-Path $TaskRoot $env:ENV_FILE } } else { Join-Path $TaskRoot '.env' }
    if (-not (Test-Path -LiteralPath $path)) { throw 'Create .env from .env.example in the backend repository first.' }
    foreach ($line in [IO.File]::ReadAllLines($path)) {
        if ($line -match '^([A-Z_]+)=(.*)$') {
            [Environment]::SetEnvironmentVariable($Matches[1],$Matches[2].Trim(),'Process')
        }
    }
}
function Set-TaskPostgres {
    param([string]$Database = '',[switch]$SchemaOwner)
    Import-TaskEnv
    $connection = [Uri]$(if ($SchemaOwner -and $env:MIGRATION_DATABASE_URL) { $env:MIGRATION_DATABASE_URL } else { $env:DATABASE_URL })
    $parts = $connection.UserInfo.Split(':',2)
    $env:PGUSER = [Uri]::UnescapeDataString($parts[0])
    $env:PGPASSWORD = if ($parts.Count -eq 2) { [Uri]::UnescapeDataString($parts[1]) } else { '' }
    $env:PGHOST = $connection.Host
    $env:PGPORT = if ($connection.Port -gt 0) { [string]$connection.Port } else { '5432' }
    $env:PGDATABASE = if ($Database) { $Database } else { $connection.AbsolutePath.TrimStart('/') }
}
function Assert-TaskExit { if ($LASTEXITCODE -ne 0) { throw "Command failed (exit $LASTEXITCODE)." } }
