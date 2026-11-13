#requires -Version 5.1
[CmdletBinding()]
param([ValidateSet('Static','Plan')][string]$Mode='Static', [string]$OutputPath)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$manifestPath = Join-Path $PSScriptRoot 'manifest.json'
$manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
foreach ($file in $manifest.files) {
  if ($file.path -notmatch '^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$') { throw 'Invalid package manifest path.' }
  $path = Join-Path $PSScriptRoot $file.path
  if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw ('Package file missing: ' + $file.path) }
  $actual = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
  if ($actual -ne $file.sha256) { throw ('Package hash mismatch: ' + $file.path) }
}
$nativeArchitecture = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
$architecture = switch ($nativeArchitecture.ToUpperInvariant()) { 'AMD64' { 'x64' }; 'ARM64' { 'arm64' }; default { 'unsupported' } }
$result = [ordered]@{ packageVersion=$manifest.version; packageHashes='passed'; architecture=$architecture; nativeFunctionality='not_run'; usb='not_run'; print='not_run'; rdpClipboard='not_run'; localApplicationClipboard='not_run'; sessionResponse='not_run'; unintegratedPrePasteBlocking='not_guaranteed'; windowsRuntimeAcceptance='pending' }
if ($Mode -eq 'Plan') {
  $programData = [Environment]::GetFolderPath('CommonApplicationData')
  $trustedSids = @('S-1-5-18', 'S-1-5-32-544')
  # Inspect the installed path before launching code with an elevated token.
  foreach ($relative in @('ScarletPlume', 'ScarletPlume\EndpointNative', 'ScarletPlume\EndpointNative\scarlet-endpoint-agent.exe')) {
    $protectedPath = Join-Path $programData $relative
    $item = Get-Item -LiteralPath $protectedPath -Force
    if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'Installed agent path cannot contain reparse points.' }
    $acl = Get-Acl -LiteralPath $protectedPath
    if (-not $acl.AreAccessRulesProtected -or $acl.GetOwner([Security.Principal.SecurityIdentifier]).Value -notin $trustedSids) { throw 'Installed agent path must have a protected SYSTEM/Administrators owner and ACL.' }
    foreach ($rule in $acl.Access) {
      if ($rule.AccessControlType -ne 'Allow' -or $rule.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value -notin $trustedSids) { throw 'Installed agent permissions are not restricted to SYSTEM/Administrators.' }
    }
  }
  $agent = Join-Path ([Environment]::GetFolderPath('CommonApplicationData')) 'ScarletPlume\EndpointNative\scarlet-endpoint-agent.exe'
  if (-not (Test-Path -LiteralPath $agent -PathType Leaf)) { throw 'Provision and install before reading the protected native plan.' }
  $plan = & $agent -mode native-plan
  if ($LASTEXITCODE -ne 0) { throw 'Protected plan inspection failed. No functional verification is claimed.' }
  $result['sourcePlan'] = ($plan | ConvertFrom-Json)
}
$json = $result | ConvertTo-Json -Depth 12
if ($OutputPath) { $json | Set-Content -LiteralPath $OutputPath -Encoding UTF8 -NoNewline }
$json
