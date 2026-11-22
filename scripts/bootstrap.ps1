param(
  [Parameter(Mandatory=$true)][string]$Server,
  [Alias('Token')][string]$JoinToken = '', [switch]$ReadJoinToken,
  [string]$Proxy = '', [string]$DataDir = '', [string]$DownloadDir = './uir-agent',
  [switch]$Foreground
)
$ErrorActionPreference = 'Stop'
# Use this PowerShell host's modules even when PSModulePath came from another version.
Import-Module (Join-Path $PSHOME 'Modules/Microsoft.PowerShell.Utility') -ErrorAction Stop
if ($ReadJoinToken) { $JoinToken = [Console]::ReadLine() }
if ($JoinToken -cnotmatch '^(?:[A-Za-z0-9]{16}|[A-Za-z0-9_-]{43})$') { throw 'Invalid join code. Generate a new connection command.' }
if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) { throw 'Use this installer on Windows' }
$Server = $Server.TrimEnd('/')
$uri = [Uri]$Server
if (-not $uri.IsAbsoluteUri -or $uri.Scheme -notin @('http','https') -or $uri.UserInfo -or $uri.Query -or $uri.Fragment -or $uri.AbsolutePath -ne '/') { throw 'Provide -Server with an HTTP or HTTPS origin' }
$architecture = $env:PROCESSOR_ARCHITEW6432
if (-not $architecture) { $architecture = [Environment]::GetEnvironmentVariable('PROCESSOR_ARCHITECTURE', 'Machine') }
if (-not $architecture) { $architecture = $env:PROCESSOR_ARCHITECTURE }
$arch = switch ($architecture) { 'AMD64' {'amd64'} 'X64' {'amd64'} 'ARM64' {'arm64'} 'X86' {'386'} default { throw 'Unsupported architecture. Use Download enrollment to select a matching Agent.' } }
$parent = [IO.Directory]::CreateDirectory($DownloadDir).FullName
$temp = Join-Path $parent ('.download-' + [Guid]::NewGuid().ToString('N'))
$url = "$Server/agent/download/$JoinToken/uir-agent-win-$arch.exe"
try {
  if ($Proxy) {
    $status = & curl.exe -qkfsS --connect-timeout 20 --max-time 300 --proxy $Proxy -o $temp -w '%{http_code}' $url
    if ($status -eq '404') { throw "No Agent program published for windows/$arch. Upload it in Agent management, then retry." }
    if ($LASTEXITCODE -ne 0 -or $status -ne '200') { throw "Download failed: HTTP $status. Generate a new connection command if the code is no longer valid." }
  } else {
    # WinHTTP is available on supported Windows versions without curl or Internet Explorer.
    $request = New-Object -ComObject WinHttp.WinHttpRequest.5.1
    try {
      $request.Open('GET', $url, $false)
      $request.Option(6) = $false
      $request.SetAutoLogonPolicy(2)
      if ($uri.Scheme -eq 'https') { $request.Option(9) = 2048; $request.Option(4) = 13056 }
      $request.SetTimeouts(20000, 20000, 300000, 300000)
      $request.Send()
      if ($request.Status -eq 404) { throw "No Agent program published for windows/$arch. Upload it in Agent management, then retry." }
      if ($request.Status -ne 200) { throw "Download failed: HTTP $($request.Status). Generate a new connection command if the code is no longer valid." }
      $bytes = [byte[]]$request.ResponseBody
      if ($request.GetAllResponseHeaders() -match '(?im)^Content-Length:[ \t]*(\d+)[ \t]*\r?$' -and $bytes.Length -ne [long]$Matches[1]) { throw 'Incomplete Agent download' }
      [IO.File]::WriteAllBytes($temp, $bytes)
    } finally { [void][Runtime.InteropServices.Marshal]::FinalReleaseComObject($request) }
  }
  if (([IO.FileInfo]$temp).Length -eq 0) { throw 'Empty Agent download' }
  $binary = Join-Path $parent 'uir-agent.exe'
  Move-Item -LiteralPath $temp -Destination $binary -Force
  $arguments = @('--server', $Server, '--join-token', '-')
  if ($Foreground) { $arguments += '--foreground' }
  if ($Proxy) { $arguments += @('--proxy', $Proxy) }
  if ($DataDir) { $arguments += @('--data-dir', $DataDir) }
  $JoinToken | & $binary @arguments
  if ($LASTEXITCODE -ne 0) { throw "Agent exited with code $LASTEXITCODE" }
} finally { if ([IO.File]::Exists($temp)) { [IO.File]::Delete($temp) } }
