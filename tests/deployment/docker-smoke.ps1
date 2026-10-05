param([string]$Image = 'hoanxu-backend:browser', [int]$Port = 18080, [string]$ViewerTest = '')
# PowerShell 7; uses only an isolated disposable PostgreSQL container.
$ErrorActionPreference = 'Stop'
$taskSuffix = [guid]::NewGuid().ToString('N').Substring(0, 8)
$taskNetwork = "hx-browser-$taskSuffix"
$taskDB = "$taskNetwork-db"
$taskAPI = "$taskNetwork-api"
$taskVolume = "$taskNetwork-data"
$taskPassword = 'docker-smoke-admin-password'
$taskOrigin = "http://localhost:$Port"
$taskDBURL = "postgres://hoanxu:smoke-only-password@${taskDB}:5432/hoanxu_browser_test?sslmode=disable"
$taskEnv = @('-e', "DATABASE_URL=$taskDBURL", '-e', 'DATA_ENCRYPTION_KEY=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=')
function Invoke-SmokeDocker {
  $taskOutput = & docker @args 2>&1
  if ($LASTEXITCODE -ne 0) { throw "Docker smoke command failed: $($args[0])" }
  return $taskOutput
}
function Assert-Smoke($Condition, [string]$Message) { if (-not $Condition) { throw $Message } }
try {
  [void](Invoke-SmokeDocker network create $taskNetwork)
  [void](Invoke-SmokeDocker volume create $taskVolume)
  [void](Invoke-SmokeDocker run -d --name $taskDB --network $taskNetwork -e POSTGRES_USER=hoanxu -e POSTGRES_PASSWORD=smoke-only-password -e POSTGRES_DB=hoanxu_browser_test postgres:17-alpine)
  for ($attempt = 0; $attempt -lt 60; $attempt++) {
    & docker exec $taskDB pg_isready -U hoanxu -d hoanxu_browser_test *> $null
    if ($LASTEXITCODE -eq 0) { break }
    Start-Sleep -Seconds 1
  }
  Assert-Smoke ($LASTEXITCODE -eq 0) 'Smoke PostgreSQL did not start.'
  [void](Invoke-SmokeDocker run --rm --network $taskNetwork @taskEnv --entrypoint /app/admin $Image migrate)
  [void](Invoke-SmokeDocker run --rm --network $taskNetwork @taskEnv -e "ADMIN_PASSWORD=$taskPassword" --entrypoint /app/admin $Image create --username smokeadmin --name SmokeAdmin --role admin)
  [void](Invoke-SmokeDocker exec $taskDB psql -U hoanxu -d hoanxu_browser_test -c 'UPDATE internal_credentials SET must_change=false;')
  [void](Invoke-SmokeDocker run -d --name $taskAPI --network $taskNetwork @taskEnv -e "REMOTE_BROWSER_ORIGIN=$taskOrigin" -e APP_ORIGIN=http://localhost:3000 -e COOKIE_SECURE=false -p "${Port}:10000" -v "${taskVolume}:/var/data" $Image)
  $taskSession = [Microsoft.PowerShell.Commands.WebRequestSession]::new()
  for ($attempt = 0; $attempt -lt 60; $attempt++) {
    try { $ready = Invoke-WebRequest "$taskOrigin/readyz" -TimeoutSec 2 -SkipHttpErrorCheck; if ($ready.StatusCode -eq 200) { break } } catch {}
    Start-Sleep -Seconds 1
  }
  Assert-Smoke ($ready.StatusCode -eq 200) 'Smoke API did not become ready.'
  foreach ($taskPath in @('/browser/screen','/browser/view/core/rfb.js','/browser/view/websockify')) {
    $response = Invoke-WebRequest "$taskOrigin$taskPath" -SkipHttpErrorCheck
    Assert-Smoke ($response.StatusCode -eq 401) "Public display access allowed: $taskPath"
  }
  $headers = @{ Origin = 'http://localhost:3000' }
  [void](Invoke-WebRequest "$taskOrigin/api/v1/auth/internal/login" -Method Post -Headers $headers -ContentType application/json -Body (@{username='smokeadmin';password=$taskPassword}|ConvertTo-Json) -WebSession $taskSession)
  $me = Invoke-RestMethod "$taskOrigin/api/v1/me" -WebSession $taskSession
  $headers['X-CSRF-Token'] = $me.data.csrfToken
  [void](Invoke-WebRequest "$taskOrigin/api/v1/auth/internal/reauth" -Method Post -Headers $headers -ContentType application/json -Body (@{password=$taskPassword}|ConvertTo-Json) -WebSession $taskSession)
  $access = Invoke-RestMethod "$taskOrigin/api/v1/admin/browser/access" -Method Post -Headers $headers -WebSession $taskSession -TimeoutSec 45
  $ticket = ([uri]$access.data.url).Fragment.Substring('#ticket='.Length)
  $remoteHeaders = @{ Origin = $taskOrigin }
  $response = Invoke-WebRequest "$taskOrigin/browser/session" -Method Post -Headers $remoteHeaders -ContentType application/json -Body (@{ticket=$ticket}|ConvertTo-Json) -WebSession $taskSession
  Assert-Smoke ($response.StatusCode -eq 204) 'Ticket exchange failed.'
  $response = Invoke-WebRequest "$taskOrigin/browser/view/core/rfb.js" -WebSession $taskSession
  Assert-Smoke ($response.StatusCode -eq 200 -and $response.Content.Contains('RFB')) 'noVNC assets unavailable.'
  $socket = [Net.WebSockets.ClientWebSocket]::new()
  try {
    $socket.Options.Cookies = $taskSession.Cookies
    $socket.Options.SetRequestHeader('Origin', $taskOrigin)
    $socketTimeout = [Threading.CancellationTokenSource]::new([TimeSpan]::FromSeconds(10))
    [void]$socket.ConnectAsync([uri]"ws://localhost:$Port/browser/view/websockify", $socketTimeout.Token).GetAwaiter().GetResult()
    $buffer = [byte[]]::new(1024)
    $received = $socket.ReceiveAsync([ArraySegment[byte]]::new($buffer), $socketTimeout.Token).GetAwaiter().GetResult()
    Assert-Smoke ([Text.Encoding]::ASCII.GetString($buffer,0,$received.Count).StartsWith('RFB ')) 'VNC handshake missing.'
  } finally { $socket.Dispose(); if ($socketTimeout) { $socketTimeout.Dispose() } }
  $chrome = Invoke-SmokeDocker exec $taskAPI /bin/bash -c 'for file in /proc/[0-9]*/cmdline; do line=$(tr "\0" " " < "$file" 2>/dev/null || true); case "$line" in /usr/lib/chromium/chromium*) echo "$line";; esac; done'
  Assert-Smoke (($chrome -join ' ').Contains('--user-data-dir=/var/data/chrome-profile')) 'Managed persistent Chrome was not started.'
  Assert-Smoke (-not (($chrome -join ' ') -match '--headless')) 'Chrome unexpectedly ran headless.'
  $owner = Invoke-SmokeDocker exec $taskAPI stat -c '%u' /var/data/chrome-profile
  Assert-Smoke (($owner -join '').Trim() -eq '10001') 'Chrome profile has the wrong owner.'
  [void](Invoke-SmokeDocker exec $taskAPI /bin/bash -c 'test -d /var/data/chrome-profile/Default && printf profile-smoke > /var/data/chrome-profile/persistence-smoke')
  [void](Invoke-SmokeDocker restart --time 20 $taskAPI)
  $restartedReady = $false
  for ($attempt = 0; $attempt -lt 60; $attempt++) {
    try { $ready = Invoke-WebRequest "$taskOrigin/readyz" -TimeoutSec 2 -SkipHttpErrorCheck; if ($ready.StatusCode -eq 200) { $restartedReady = $true; break } } catch {}
    Start-Sleep -Seconds 1
  }
  Assert-Smoke $restartedReady 'API did not restart successfully.'
  [void](Invoke-SmokeDocker exec $taskAPI /bin/bash -c 'test -d /var/data/chrome-profile/Default && test "$(cat /var/data/chrome-profile/persistence-smoke)" = profile-smoke')
  $response = Invoke-WebRequest "$taskOrigin/browser/screen" -WebSession $taskSession -SkipHttpErrorCheck
  Assert-Smoke ($response.StatusCode -eq 401) 'Browser session should be cleared after container restart.'
  $access = Invoke-RestMethod "$taskOrigin/api/v1/admin/browser/access" -Method Post -Headers $headers -WebSession $taskSession -TimeoutSec 45
  $ticket = ([uri]$access.data.url).Fragment.Substring('#ticket='.Length)
  [void](Invoke-WebRequest "$taskOrigin/browser/session" -Method Post -Headers $remoteHeaders -ContentType application/json -Body (@{ticket=$ticket}|ConvertTo-Json) -WebSession $taskSession)
  $response = Invoke-WebRequest "$taskOrigin/browser/screen" -WebSession $taskSession
  Assert-Smoke ($response.StatusCode -eq 200) 'Cannot reopen headed Chrome after restart.'
  if ($ViewerTest) {
    $taskPreviousOrigin = $env:HOANXU_BROWSER_SMOKE_ORIGIN
    try {
      $env:HOANXU_BROWSER_SMOKE_ORIGIN = $taskOrigin
      & node $ViewerTest
      Assert-Smoke ($LASTEXITCODE -eq 0) 'Remote browser viewer smoke failed.'
    } finally { $env:HOANXU_BROWSER_SMOKE_ORIGIN = $taskPreviousOrigin }
  }
  [void](Invoke-WebRequest "$taskOrigin/api/v1/auth/logout" -Method Post -Headers $headers -WebSession $taskSession)
  $response = Invoke-WebRequest "$taskOrigin/browser/screen" -WebSession $taskSession -SkipHttpErrorCheck
  Assert-Smoke ($response.StatusCode -eq 401) 'Original session logout did not revoke the display.'
  Write-Output 'Docker smoke passed: headed Chrome, noVNC WebSocket, persisted profile/restart, private display and session revocation.'
} finally {
  & docker rm -f $taskAPI $taskDB *> $null
  & docker volume rm $taskVolume *> $null
  & docker network rm $taskNetwork *> $null
}
