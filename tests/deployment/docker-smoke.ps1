param([string]$Image = 'hoanxu-backend:remote', [string]$ChromeImage = 'hoanxu-chromium:ec2', [int]$Port = 18080, [string]$ViewerTest = '', [string]$AppArmorProfile = 'unconfined')
# PowerShell 7; uses only an isolated disposable PostgreSQL container.
$ErrorActionPreference = 'Stop'
$taskSuffix = [guid]::NewGuid().ToString('N').Substring(0, 8)
$taskNetwork = "hx-browser-$taskSuffix"
$taskDB = "$taskNetwork-db"
$taskAPI = "$taskNetwork-api"
$taskChrome = "$taskNetwork-chrome"
$taskSSH = "$taskNetwork-ssh"
$taskSSHImage = 'hoanxu-ssh-fixture:test'
$taskVolume = "$taskNetwork-data"
$taskProfile = "$taskNetwork-profile"
$taskKeys = "$taskNetwork-keys"
$taskBridge = 'smoke_bridge_secret_32_characters_only'
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
  $taskBackendRoot = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
  $taskChromeRoot = (Resolve-Path (Join-Path $taskBackendRoot 'chromium')).Path
  [void](Invoke-SmokeDocker build -t $Image $taskBackendRoot)
  [void](Invoke-SmokeDocker build -t $ChromeImage $taskChromeRoot)
  [void](Invoke-SmokeDocker build -t $taskSSHImage -f (Join-Path $PSScriptRoot 'ssh-fixture.Dockerfile') $PSScriptRoot)
  [void](Invoke-SmokeDocker network create $taskNetwork)
  [void](Invoke-SmokeDocker volume create $taskVolume)
  [void](Invoke-SmokeDocker volume create $taskProfile)
  [void](Invoke-SmokeDocker volume create $taskKeys)
  # Docker Desktop defaults to unconfined; Ubuntu CI loads the EC2 profile.
  [void](Invoke-SmokeDocker run -d --name $taskChrome --network $taskNetwork --security-opt seccomp=unconfined --security-opt "apparmor=$AppArmorProfile" --shm-size 512m -e "REMOTE_BROWSER_BRIDGE_PASSWORD=$taskBridge" -v "${taskProfile}:/var/lib/shopee-chrome" $ChromeImage)
  [void](Invoke-SmokeDocker run -d --name $taskSSH --network "container:$taskChrome" -e "SSH_FIXTURE_HOST=$taskChrome" -v "${taskKeys}:/fixture" $taskSSHImage)
  for ($attempt = 0; $attempt -lt 60; $attempt++) {
    & docker exec $taskChrome /opt/chromium/healthcheck.sh *> $null
    if ($LASTEXITCODE -eq 0) { break }
    Start-Sleep -Seconds 1
  }
  Assert-Smoke ($LASTEXITCODE -eq 0) 'Independent Chromium did not start with sandbox enabled.'
  $taskPrivateKey = (Invoke-SmokeDocker exec $taskSSH cat /fixture/identity) -join "`n"
  $taskKnownHosts = (Invoke-SmokeDocker exec $taskSSH cat /fixture/known_hosts) -join "`n"
  # Test both credential failures against a real SSH daemon. A timeout (124)
  # means auth unexpectedly succeeded; refusal must be SSH's status 255.
  foreach ($taskCase in @(@('wrongkey','known_hosts'), @('identity','wrong_hosts'))) {
    & docker run --rm --network $taskNetwork -v "${taskKeys}:/fixture:ro" --entrypoint timeout $taskSSHImage 5 ssh -F /dev/null -N -T -p 2222 -i "/fixture/$($taskCase[0])" -o BatchMode=yes -o StrictHostKeyChecking=yes -o "UserKnownHostsFile=/fixture/$($taskCase[1])" -o GlobalKnownHostsFile=/dev/null "chrome-tunnel@$taskChrome" *> $null
    Assert-Smoke ($LASTEXITCODE -eq 255) "SSH accepted invalid key/host key: $($taskCase -join ',')"
  }
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
  [void](Invoke-SmokeDocker run -d --name $taskAPI --cap-drop KILL --network $taskNetwork @taskEnv -e BROWSER_MODE=remote -e CHROME_SSH_TUNNEL_ENABLED=true -e "CHROME_SSH_HOST=$taskChrome" -e CHROME_SSH_PORT=2222 -e "CHROME_SSH_PRIVATE_KEY=$taskPrivateKey" -e "CHROME_SSH_KNOWN_HOSTS=$taskKnownHosts" -e "REMOTE_BROWSER_BRIDGE_PASSWORD=$taskBridge" -e "REMOTE_BROWSER_ORIGIN=$taskOrigin" -e APP_ORIGIN=http://localhost:3000 -e COOKIE_SECURE=false -p "127.0.0.1:${Port}:10000" -v "${taskVolume}:/var/data" $Image)
  $taskPrivateKey = $null
  $taskSession = [Microsoft.PowerShell.Commands.WebRequestSession]::new()
  for ($attempt = 0; $attempt -lt 60; $attempt++) {
    try { $ready = Invoke-WebRequest "$taskOrigin/readyz" -TimeoutSec 2 -SkipHttpErrorCheck; if ($ready.StatusCode -eq 200) { break } } catch {}
    Start-Sleep -Seconds 1
  }
  Assert-Smoke ($ready.StatusCode -eq 200) 'Smoke API did not become ready.'
  for ($attempt = 0; $attempt -lt 60; $attempt++) {
    & docker exec $taskAPI curl --fail --silent http://127.0.0.1:9222/json/version *> $null
    if ($LASTEXITCODE -eq 0) { break }
    Start-Sleep -Seconds 1
  }
  Assert-Smoke ($LASTEXITCODE -eq 0) 'Supervised SSH tunnel did not expose CDP.'
  foreach ($taskPath in @('/browser/screen','/browser/view/core/rfb.js','/browser/view/websockify')) {
    $response = Invoke-WebRequest "$taskOrigin$taskPath" -SkipHttpErrorCheck
    Assert-Smoke ($response.StatusCode -eq 401) "Public display access allowed: $taskPath"
  }
  $headers = @{ Origin = 'http://localhost:3000' }
  [void](Invoke-WebRequest "$taskOrigin/api/v1/auth/internal/login" -Method Post -Headers $headers -ContentType application/json -Body (@{username='smokeadmin';password=$taskPassword}|ConvertTo-Json) -WebSession $taskSession)
  $me = Invoke-RestMethod "$taskOrigin/api/v1/me" -WebSession $taskSession
  $headers['X-CSRF-Token'] = $me.data.csrfToken
  [void](Invoke-WebRequest "$taskOrigin/api/v1/auth/internal/reauth" -Method Post -Headers $headers -ContentType application/json -Body (@{password=$taskPassword}|ConvertTo-Json) -WebSession $taskSession)
  for ($attempt = 0; $attempt -lt 60; $attempt++) {
    $taskBrowserStatus = Invoke-RestMethod "$taskOrigin/api/v1/admin/browser" -WebSession $taskSession
    if ($taskBrowserStatus.data.browser) { break }
    Start-Sleep -Seconds 1
  }
  Assert-Smoke $taskBrowserStatus.data.browser 'Go did not attach to the independent browser.'
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
  $chrome = Invoke-SmokeDocker exec $taskChrome /bin/bash -c 'for file in /proc/[0-9]*/cmdline; do line=$(tr "\0" " " < "$file" 2>/dev/null || true); case "$line" in /usr/lib/chromium/chromium*) echo "$line";; esac; done'
  Assert-Smoke (($chrome -join ' ').Contains('--user-data-dir=/var/lib/shopee-chrome')) 'Independent persistent Chrome was not started.'
  Assert-Smoke (-not (($chrome -join ' ') -match '--headless')) 'Chrome unexpectedly ran headless.'
  Assert-Smoke (-not (($chrome -join ' ') -match '--no-sandbox')) 'Chrome sandbox was disabled.'
  & docker exec $taskAPI /bin/bash -c 'command -v chromium || command -v Xvfb' *> $null
  Assert-Smoke ($LASTEXITCODE -ne 0) 'Browser packages leaked into the Go image.'
  $owner = Invoke-SmokeDocker exec $taskChrome stat -c '%u' /var/lib/shopee-chrome
  Assert-Smoke (($owner -join '').Trim() -eq '10001') 'Chrome profile has the wrong owner.'
  [void](Invoke-SmokeDocker exec $taskChrome /bin/bash -c 'test -d /var/lib/shopee-chrome/Default && printf profile-smoke > /var/lib/shopee-chrome/persistence-smoke')
  $chromeStarted = (Invoke-SmokeDocker inspect --format '{{.State.StartedAt}}' $taskChrome) -join ''
  [void](Invoke-SmokeDocker restart --time 20 $taskAPI)
  $restartedReady = $false
  for ($attempt = 0; $attempt -lt 60; $attempt++) {
    try { $ready = Invoke-WebRequest "$taskOrigin/readyz" -TimeoutSec 2 -SkipHttpErrorCheck; if ($ready.StatusCode -eq 200) { $restartedReady = $true; break } } catch {}
    Start-Sleep -Seconds 1
  }
  Assert-Smoke $restartedReady 'API did not restart successfully.'
  $taskRestartLogs = (Invoke-SmokeDocker logs $taskAPI) -join "`n"
  Assert-Smoke (-not ($taskRestartLogs -match 'Unexpected error when forwarding signal')) 'Init could not forward signals without CAP_KILL.'
  Assert-Smoke (((Invoke-SmokeDocker inspect --format '{{.State.StartedAt}}' $taskChrome) -join '') -eq $chromeStarted) 'Restarting Go restarted Chromium.'
  [void](Invoke-SmokeDocker exec $taskChrome /bin/bash -c 'test -d /var/lib/shopee-chrome/Default && test "$(cat /var/lib/shopee-chrome/persistence-smoke)" = profile-smoke')
  for ($attempt = 0; $attempt -lt 60; $attempt++) {
    & docker exec $taskAPI curl --fail --silent http://127.0.0.1:9222/json/version *> $null
    if ($LASTEXITCODE -eq 0) { break }
    Start-Sleep -Seconds 1
  }
  Assert-Smoke ($LASTEXITCODE -eq 0) 'Tunnel did not recover after Go restart.'
  $response = Invoke-WebRequest "$taskOrigin/browser/screen" -WebSession $taskSession -SkipHttpErrorCheck
  Assert-Smoke ($response.StatusCode -eq 401) 'Browser session should be cleared after container restart.'
  $access = Invoke-RestMethod "$taskOrigin/api/v1/admin/browser/access" -Method Post -Headers $headers -WebSession $taskSession -TimeoutSec 45
  $ticket = ([uri]$access.data.url).Fragment.Substring('#ticket='.Length)
  [void](Invoke-WebRequest "$taskOrigin/browser/session" -Method Post -Headers $remoteHeaders -ContentType application/json -Body (@{ticket=$ticket}|ConvertTo-Json) -WebSession $taskSession)
  $response = Invoke-WebRequest "$taskOrigin/browser/screen" -WebSession $taskSession
  Assert-Smoke ($response.StatusCode -eq 200) 'Cannot reopen headed Chrome after restart.'
  [void](Invoke-SmokeDocker stop $taskSSH)
  $ready = Invoke-WebRequest "$taskOrigin/readyz" -SkipHttpErrorCheck
  Assert-Smoke ($ready.StatusCode -eq 200) 'Tunnel outage took down the API.'
  [void](Invoke-SmokeDocker start $taskSSH)
  for ($attempt = 0; $attempt -lt 90; $attempt++) {
    & docker exec $taskAPI curl --fail --silent http://127.0.0.1:9222/json/version *> $null
    if ($LASTEXITCODE -eq 0) { break }
    Start-Sleep -Seconds 1
  }
  Assert-Smoke ($LASTEXITCODE -eq 0) 'SSH tunnel did not reconnect automatically.'
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
  Write-Output 'Docker smoke passed: independent Chrome, real SSH tunnel, key/host pinning, API restart, tunnel recovery, noVNC and session revocation.'
} finally {
  $taskPrivateKey = $null
  & docker rm -f $taskAPI $taskDB $taskSSH $taskChrome *> $null
  & docker volume rm $taskVolume $taskProfile $taskKeys *> $null
  & docker network rm $taskNetwork *> $null
}
