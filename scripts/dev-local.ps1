# Select the local runtime profile without changing .env or env.prod.
$taskLocalRoot = Split-Path -Parent $PSScriptRoot
$taskPreviousEnvFile = $env:ENV_FILE
try {
    $env:ENV_FILE = Join-Path $taskLocalRoot '.env.local'
    if (-not (Test-Path -LiteralPath $env:ENV_FILE)) { throw 'Create .env.local from .env.example with your database configuration.' }
    & "$PSScriptRoot/dev.ps1"
} finally { $env:ENV_FILE = $taskPreviousEnvFile }
