#requires -Version 5.1
[CmdletBinding(SupportsShouldProcess=$true,ConfirmImpact='High')]
param([Parameter(Mandatory=$true)][string]$Configuration,[switch]$RegisterLogonTask)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
# This installer is the explicitly trusted bootstrap. Do not execute a neighboring
# helper until its bytes have been checked against the independent HTTPS release.
if ($PSVersionTable.PSEdition -ne 'Desktop') { throw 'Windows PowerShell 5.1 Desktop is required.' }
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = [Security.Principal.WindowsPrincipal]::new($identity)
if ($identity.IsSystem -or [Diagnostics.Process]::GetCurrentProcess().SessionId -eq 0 -or $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { throw 'Run without elevation in the authorized interactive user session.' }
function Assert-ScarletInstallerPath([string]$Path) {
  $full = [IO.Path]::GetFullPath($Path)
  if ($full -notmatch '^[A-Za-z]:\\' -or $full.Substring(3).Contains(':')) { throw 'Installer inputs must be local drive paths without alternate streams.' }
  $cursor = $full
  while ($cursor) {
    $item = Get-Item -LiteralPath $cursor -Force -ErrorAction SilentlyContinue
    if ($item -and ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'Installer inputs and their parents cannot be reparse points.' }
    $parent = [IO.Path]::GetDirectoryName($cursor)
    if ($parent -eq $cursor) { break }
    $cursor = $parent
  }
}
function Read-ScarletInstallerStream([IO.Stream]$Stream,[long]$Limit) {
  if ($Stream.CanSeek -and $Stream.Length -gt $Limit) { throw 'Installer input exceeds its read budget.' }
  $memory = New-Object IO.MemoryStream; $buffer = New-Object byte[] 8192
  try {
    while (($count = $Stream.Read($buffer,0,$buffer.Length)) -gt 0) {
      if ($memory.Length + $count -gt $Limit) { throw 'Installer input exceeds its read budget.' }
      $memory.Write($buffer,0,$count)
    }
    return ,($memory.ToArray())
  } finally { [Array]::Clear($buffer,0,$buffer.Length); $memory.Dispose() }
}
$Configuration = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($Configuration)
Assert-ScarletInstallerPath -Path $Configuration
$configStream = [IO.File]::Open([IO.Path]::GetFullPath($Configuration),[IO.FileMode]::Open,[IO.FileAccess]::Read,[IO.FileShare]::Read)
try { $configBytes = Read-ScarletInstallerStream -Stream $configStream -Limit 8192 } finally { $configStream.Dispose() }
try { $config = [Text.Encoding]::UTF8.GetString($configBytes).TrimStart([char]0xfeff) | ConvertFrom-Json } catch { throw 'Invalid public clipboard configuration; never place credentials in it.' }
$keys = @('version','platformOrigin','scope','deviceId','allowUnicodeInspection')
if (@($config.PSObject.Properties.Name).Count -ne $keys.Count) { throw 'Use the complete explicit public clipboard template; do not place credentials in it.' }
foreach ($property in $keys) { if ($property -notin @($config.PSObject.Properties.Name)) { throw 'Use the explicit public clipboard template; do not place credentials in it.' } }
if ($config.version -ne 1 -or $config.allowUnicodeInspection -isnot [bool] -or -not $config.allowUnicodeInspection -or $config.scope -isnot [string] -or $config.scope.Length -lt 1 -or $config.scope.Length -gt 80 -or $config.deviceId -isnot [string] -or $config.deviceId.Length -gt 256) { throw 'An explicit authorized Unicode inspection scope is required.' }
$platform = $null
if (-not [Uri]::TryCreate($config.platformOrigin,[UriKind]::Absolute,[ref]$platform) -or $platform.Scheme -ne 'https' -or $platform.UserInfo -or $platform.Query -or $platform.Fragment -or $platform.AbsolutePath -notin @('','/')) { throw 'An explicit HTTPS platform origin is required.' }
$architecture = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
$fileName = switch ($architecture.ToUpperInvariant()) { 'AMD64' { 'scarlet-endpoint-agent-windows-amd64.exe' }; 'ARM64' { 'scarlet-endpoint-agent-windows-arm64.exe' }; default { throw 'Only Windows x64/ARM64 packages are included.' } }
# The public HTTPS platform is the independent release authority, not the ZIP's manifest.
# Retain certificate validation and refuse redirects; do not weaken execution policy.
$previousTLS = [Net.ServicePointManager]::SecurityProtocol
try {
  [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
  $request = [Net.HttpWebRequest]::Create($platform.AbsoluteUri.TrimEnd('/') + '/downloads/windows-enterprise-manifest.json')
  $request.AllowAutoRedirect = $false; $request.Timeout = 15000; $request.ReadWriteTimeout = 15000
  $response = $null; $responseStream = $null
  try {
    $response = $request.GetResponse()
    if ([int]$response.StatusCode -ne 200) { throw 'Independent platform release manifest was not returned directly.' }
    $responseStream = $response.GetResponseStream()
    $releaseBytes = Read-ScarletInstallerStream -Stream $responseStream -Limit 262144
    $release = [Text.Encoding]::UTF8.GetString($releaseBytes) | ConvertFrom-Json
  } finally {
    if ($null -ne $responseStream) { $responseStream.Dispose() }
    if ($null -ne $response) { $response.Dispose() }
  }
} finally { [Net.ServicePointManager]::SecurityProtocol = $previousTLS }
if ($release.version -ne 'windows-enterprise-v2') { throw 'Unsupported independent platform release manifest.' }
$commonPath = Join-Path $PSScriptRoot 'ScarletClipboard.Common.ps1'
$commonEntry = @($release.files | Where-Object { $_.path -ceq 'ScarletClipboard.Common.ps1' })
if ($commonEntry.Count -ne 1 -or $commonEntry[0].sha256 -notmatch '^[0-9a-f]{64}$') { throw 'Independent clipboard helper hash unavailable.' }
Assert-ScarletInstallerPath -Path $commonPath
$commonStream = [IO.File]::Open($commonPath,[IO.FileMode]::Open,[IO.FileAccess]::Read,[IO.FileShare]::Read)
try {
  $commonBytes = Read-ScarletInstallerStream -Stream $commonStream -Limit 131072
  $sha = [Security.Cryptography.SHA256]::Create()
  try { $commonHash = [BitConverter]::ToString($sha.ComputeHash($commonBytes)).Replace('-','').ToLowerInvariant() } finally { $sha.Dispose() }
  if ($commonBytes.Length -ne $commonEntry[0].bytes -or $commonHash -ne $commonEntry[0].sha256) { throw 'Clipboard helper differs from the independently fetched platform manifest.' }
  # Holding the read-only/no-delete handle prevents replacing the verified helper.
  # Normal dot-sourcing preserves Windows execution policy and Zone.Identifier checks.
  . $commonPath
} finally { $commonStream.Dispose() }
Assert-ScarletInteractiveUser
$files = @($fileName,'ScarletClipboard.Common.ps1','Start-ScarletClipboardSession.ps1','Remove-ScarletClipboardSession.ps1','Test-ScarletClipboardSession.ps1','Invoke-ScarletClipboardText.ps1')
$approved = @(); $payloads = @{}
foreach ($name in $files) {
  $entry = @($release.files | Where-Object { $_.path -ceq $name })
  if ($entry.Count -ne 1 -or $entry[0].sha256 -notmatch '^[0-9a-f]{64}$') { throw 'Independent platform release hash unavailable.' }
  $source = Join-Path $PSScriptRoot $name
  Assert-ScarletClipboardPath -Path $source
  Initialize-ScarletClipboardFileSafety
  $inputFile = [ScarletClipboard.FileSafety]::OpenRead([IO.Path]::GetFullPath($source))
  $zone = $null
  try {
    $limit = if ($name -eq $fileName) { 67108864 } else { 131072 }
    $raw = Read-ScarletClipboardStream -Stream $inputFile -Limit $limit
    if ($raw.Length -ne $entry[0].bytes -or (Get-ScarletClipboardHash -Bytes $raw) -ne $entry[0].sha256) { throw 'Clipboard package differs from the independently fetched platform manifest.' }
    if (Get-Item -LiteralPath $source -Stream Zone.Identifier -ErrorAction SilentlyContinue) {
      $zoneStream = [IO.File]::Open(($source + ':Zone.Identifier'),[IO.FileMode]::Open,[IO.FileAccess]::Read,[IO.FileShare]::Read)
      try { $zone = Read-ScarletClipboardStream -Stream $zoneStream -Limit 65536 } finally { $zoneStream.Dispose() }
    }
  } finally { $inputFile.Dispose() }
  # Copy exactly these verified bytes, never reopen the source after hashing it.
  $payloads[$name] = @{bytes=$raw;zone=$zone}
  $approved += [ordered]@{path=$name;sha256=$entry[0].sha256}
}
if (-not $PSCmdlet.ShouldProcess(('Current user; ' + $platform.AbsoluteUri),'Install a per-user clipboard observer, protect its source profile and optionally register a limited interactive logon task')) { return }
$root = Get-ScarletClipboardRoot
New-ScarletClipboardDirectory -Path $root
if (Test-Path -LiteralPath (Join-Path $root 'profile.dpapi')) { throw 'Existing profile is retained; stop and archive the old source before a deliberate re-enrollment.' }
foreach ($relative in @('bin','state','state\guard','state\controlled')) { New-ScarletClipboardDirectory -Path (Join-Path $root $relative) }
foreach ($entry in $approved) {
  $target = Join-Path (Join-Path $root 'bin') $entry.path
  if (Test-Path -LiteralPath $target) { throw 'Existing installed file retained; this installer does not overwrite an earlier installation.' }
  Write-ScarletPrivateBytes -Path $target -Bytes $payloads[$entry.path].bytes
  if ($null -ne $payloads[$entry.path].zone) {
    $zoneStream = [IO.File]::Open(($target + ':Zone.Identifier'),[IO.FileMode]::CreateNew,[IO.FileAccess]::Write,[IO.FileShare]::None)
    try { $zoneStream.Write($payloads[$entry.path].zone,0,$payloads[$entry.path].zone.Length); $zoneStream.Flush($true) } finally { $zoneStream.Dispose() }
  }
  if ((Get-FileHash -LiteralPath $target -Algorithm SHA256).Hash.ToLowerInvariant() -ne $entry.sha256) { throw 'Installed clipboard file hash mismatch; nothing was launched.' }
}
$secret = $null; $pointer = [IntPtr]::Zero; $plain = $null; $key = $null; $protected = $null; $profile = $null
try {
  $secret = Read-Host 'Enter a dedicated clipboard user-session source token (never share the SYSTEM service source)' -AsSecureString
  $pointer = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secret)
  $rng = [Security.Cryptography.RandomNumberGenerator]::Create(); $key = New-Object byte[] 32
  try { $rng.GetBytes($key) } finally { $rng.Dispose() }
  $device = if ($config.deviceId) { $config.deviceId } else { [Guid]::NewGuid().ToString('N') }
  $profile = [ordered]@{version=1;platformOrigin=$platform.AbsoluteUri.TrimEnd('/');scope=$config.scope;deviceId=$device;sourceToken=[Runtime.InteropServices.Marshal]::PtrToStringBSTR($pointer);keyHex=[BitConverter]::ToString($key).Replace('-','').ToLowerInvariant();agent=$fileName;files=$approved;windowsRuntimeAcceptance='pending'}
  if ($profile.sourceToken -notmatch '^[\x21-\x7e]{1,4096}$') { throw 'A nonempty dedicated printable source token is required.' }
  $plain = [Text.Encoding]::UTF8.GetBytes(($profile | ConvertTo-Json -Depth 8))
  Add-Type -AssemblyName System.Security
  $protected = [Security.Cryptography.ProtectedData]::Protect($plain,$null,[Security.Cryptography.DataProtectionScope]::CurrentUser)
  Write-ScarletPrivateBytes -Path (Join-Path $root 'profile.dpapi') -Bytes $protected
} finally {
  if ($pointer -ne [IntPtr]::Zero) { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($pointer) }
  if ($plain) { [Array]::Clear($plain,0,$plain.Length) }; if ($key) { [Array]::Clear($key,0,$key.Length) }
  if ($protected) { [Array]::Clear($protected,0,$protected.Length) }
  if ($secret) { $secret.Dispose() }; $profile = $null
}
if ($RegisterLogonTask) {
  $name = Get-ScarletClipboardTaskName
  if (Get-ScheduledTask -TaskName $name -TaskPath '\' -ErrorAction SilentlyContinue) { throw 'Existing logon task retained; inspect it before replacement.' }
  $sid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
  $powershell = Get-ScarletClipboardPowerShell
  $start = Join-Path (Join-Path $root 'bin') 'Start-ScarletClipboardSession.ps1'
  $action = New-ScheduledTaskAction -Execute $powershell -Argument ('-NoProfile -NonInteractive -File "' + $start + '"')
  $trigger = New-ScheduledTaskTrigger -AtLogOn -User $sid
  $principal = New-ScheduledTaskPrincipal -UserId $sid -LogonType Interactive -RunLevel Limited
  Register-ScheduledTask -TaskName $name -TaskPath '\' -Action $action -Trigger $trigger -Principal $principal | Out-Null
}
Write-Output 'Per-user package installed, not started. Run the installed Start script explicitly. A new logon session needs a matching independently approved platform policy; no native observer has been claimed active by installation.'
