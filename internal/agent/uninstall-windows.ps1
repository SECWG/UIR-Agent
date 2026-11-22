$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
# 使用本机 PowerShell 随附模块，避免继承其他版本的 PSModulePath。
if ($PSVersionTable.PSVersion.Major -ge 3) {
  Import-Module (Join-Path $PSHOME 'Modules/Microsoft.PowerShell.Utility') -ErrorAction Stop
  Import-Module (Join-Path $PSHOME 'Modules/Microsoft.PowerShell.Management') -ErrorAction Stop
}
# Windows 7 ships with PowerShell 2.0; keep its JSON fallback local to this helper.
$legacyJson = $null
if ($PSVersionTable.PSVersion.Major -lt 3) {
  Add-Type -AssemblyName System.Web.Extensions
  $legacyJson = New-Object System.Web.Script.Serialization.JavaScriptSerializer
}
function Read-Json([string]$text) {
  if ($legacyJson) { return $legacyJson.DeserializeObject($text) }
  return ConvertFrom-Json -InputObject $text
}
function Write-Json($value) {
  if ($legacyJson) { return $legacyJson.Serialize($value) }
  return ConvertTo-Json -InputObject $value -Compress
}
[Console]::InputEncoding = New-Object Text.UTF8Encoding($false)
$plan = Read-Json ([Console]::ReadLine())
$parentProcess = [Diagnostics.Process]::GetProcessById([int]$plan.pid)
# 提前取得句柄，后续不会因 PID 重用而等待或操作其他进程。
[void]$parentProcess.Handle
$scriptPath = $MyInvocation.MyCommand.Path
$cleanupDirectory = Split-Path -Parent $scriptPath
# Validate the receipt transport before the Agent removes its local identity.
$probe = New-Object -ComObject WinHttp.WinHttpRequest.5.1
[void][Runtime.InteropServices.Marshal]::FinalReleaseComObject($probe)
[Console]::Out.WriteLine('READY')
$resultLine = [Console]::ReadLine()
if (-not $resultLine) {
  $parentProcess.Dispose()
  Remove-Item -LiteralPath $scriptPath -Force
  Remove-Item -LiteralPath $cleanupDirectory
  exit 1
}
$result = Read-Json $resultLine
$remaining = New-Object 'System.Collections.Generic.List[string]'
foreach ($path in $result.residuals) { $remaining.Add([string]$path) }
$parentExited = $false
try {
  $parentExited = $parentProcess.WaitForExit(30000)
  if (-not $parentExited) { throw 'Parent process did not exit' }
  if (Test-Path -LiteralPath $plan.executable) {
    $stream = [IO.File]::OpenRead([string]$plan.executable)
    $hash = [Security.Cryptography.SHA256]::Create()
    try { $digest = [BitConverter]::ToString($hash.ComputeHash($stream)).Replace('-', '') }
    finally { $stream.Dispose(); $hash.Dispose() }
    if ($digest -ine $plan.sha256) { throw 'Executable changed' }
    for ($attempt = 0; $attempt -lt 20; $attempt++) {
      try {
        if (Test-Path -LiteralPath $plan.executable) { Remove-Item -LiteralPath $plan.executable -Force }
        break
      } catch { if ($attempt -eq 19) { throw }; Start-Sleep -Milliseconds 100 }
    }
  }
} catch {
  if (-not $parentExited -or (Test-Path -LiteralPath $plan.executable)) { $remaining.Add([string]$plan.executable) }
}
finally { $parentProcess.Dispose() }

# 只删除本次创建的脚本和空目录；不清理程序所在目录。
try { Remove-Item -LiteralPath $scriptPath -Force } catch { $remaining.Add($scriptPath) }
try { Remove-Item -LiteralPath $cleanupDirectory } catch { $remaining.Add($cleanupDirectory) }
$status = if ($remaining.Count -eq 0) { 'completed' } else { 'partial' }
$body = Write-Json @{ status = $status; residuals = @($remaining.ToArray()) }
$bytes = [Text.Encoding]::UTF8.GetBytes($body)
for ($attempt = 0; $attempt -lt 3; $attempt++) {
  $sender = $null
  try {
    $sender = New-Object -ComObject WinHttp.WinHttpRequest.5.1
    $sender.Open('POST', ($plan.server + '/v1/agent/uninstall/' + $plan.id + '/complete'), $false)
    $sender.Option(6) = $false
    $sender.SetAutoLogonPolicy(2)
    if (([Uri]$plan.server).Scheme -eq 'https') {
      $sender.Option(9) = 2048
      $sender.Option(4) = 13056
    }
    $sender.SetTimeouts(3000, 3000, 8000, 8000)
    $sender.SetProxy(1)
    if ($plan.proxy) {
      $proxy = [Uri]$plan.proxy
      $sender.SetProxy(2, $proxy.Authority)
      if ($proxy.UserInfo) {
        $credentials = $proxy.UserInfo.Split(@([char]':'), 2)
        $password = if ($credentials.Length -gt 1) { [Uri]::UnescapeDataString($credentials[1]) } else { '' }
        $sender.SetCredentials([Uri]::UnescapeDataString($credentials[0]), $password, 1)
      }
    }
    $sender.SetRequestHeader('User-Agent', [string]$plan.userAgent)
    $sender.SetRequestHeader('Authorization', ('Bearer ' + $plan.token))
    $sender.SetRequestHeader('Content-Type', 'application/json')
    $sender.Send($bytes)
    if ($sender.Status -ge 200 -and $sender.Status -lt 300) { exit 0 }
  } catch { }
  finally { if ($sender) { [void][Runtime.InteropServices.Marshal]::FinalReleaseComObject($sender) } }
  Start-Sleep -Seconds 1
}
exit 1
