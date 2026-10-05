param([string]$Username = 'admin',[string]$Name = 'Quản trị')
. "$PSScriptRoot/common.ps1"
Import-TaskEnv
$taskSecret = Read-Host 'Mật khẩu tạm (12–128 ký tự)' -AsSecureString
$taskPointer = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($taskSecret)
$taskPreviousPassword = $env:ADMIN_PASSWORD
try {
    $env:ADMIN_PASSWORD = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($taskPointer)
    Push-Location $TaskRoot
    try { & (Get-TaskTool go) run ./cmd/admin create --username $Username --name $Name --role admin; Assert-TaskExit } finally { Pop-Location }
} finally {
    [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($taskPointer)
    $taskSecret.Dispose()
    $env:ADMIN_PASSWORD = $taskPreviousPassword
}
Write-Host 'Admin đã tạo. Đăng nhập ở /login trên frontend và đổi mật khẩu tạm.'
