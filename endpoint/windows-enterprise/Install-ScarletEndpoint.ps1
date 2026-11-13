#requires -Version 5.1
#requires -RunAsAdministrator
[CmdletBinding(SupportsShouldProcess=$true, ConfirmImpact='High')]
param([Parameter(Mandatory=$true)][string]$Configuration, [switch]$StartService)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ($PSVersionTable.PSEdition -ne 'Desktop') { throw 'Run with Windows PowerShell 5.1 (.NET Framework); PowerShell Core is not supported by this deployment script.' }
& (Join-Path $PSScriptRoot 'Test-ScarletDeployment.ps1') -Mode Static
$nativeArchitecture = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
$architecture = switch ($nativeArchitecture.ToUpperInvariant()) { 'AMD64' { 'x64' }; 'ARM64' { 'arm64' }; default { 'unsupported' } }
if ($architecture -notin @('x64', 'arm64')) { throw 'Only Windows x64 and ARM64 deployment binaries are included.' }
$fileName = if ($architecture -eq 'arm64') { 'scarlet-endpoint-agent-windows-arm64.exe' } else { 'scarlet-endpoint-agent-windows-amd64.exe' }
$agent = Join-Path $PSScriptRoot $fileName
$config = (Resolve-Path -LiteralPath $Configuration).Path
$configurationPreview = Get-Content -LiteralPath $config -Raw -Encoding UTF8 | ConvertFrom-Json
$platform = [Uri]$configurationPreview.platformOrigin
if (-not $platform.IsAbsoluteUri -or $platform.Scheme -ne 'https' -or $platform.UserInfo -or $platform.Query -or $platform.Fragment -or $platform.AbsolutePath -notin @('', '/')) { throw 'An explicit HTTPS platform origin without credentials/query is required.' }
# This independent TLS source binds the binary hash. A manifest inside the
# downloaded ZIP alone is only an integrity check, never its own authority.
$releaseUri = $platform.AbsoluteUri.TrimEnd('/') + '/downloads/windows-enterprise-manifest.json'
$release = (Invoke-WebRequest -Uri $releaseUri -UseBasicParsing -MaximumRedirection 0).Content | ConvertFrom-Json
$approvedEntry = @($release.files | Where-Object { $_.path -eq $fileName })
if ($approvedEntry.Count -ne 1 -or $approvedEntry[0].sha256 -notmatch '^[0-9a-f]{64}$') { throw 'Trusted platform binary hash unavailable.' }
$expectedHash = $approvedEntry[0].sha256
if (-not $PSCmdlet.ShouldProcess(('ScarletEndpointNative; platform ' + $platform.AbsoluteUri), 'Protect source configuration, copy hash-bound agent and register automatic SYSTEM service')) { return }

$stageRoot = Join-Path ([Environment]::GetFolderPath('CommonApplicationData')) 'ScarletPlumeDeployment'
$directorySecurity = New-Object Security.AccessControl.DirectorySecurity
$directorySecurity.SetSecurityDescriptorSddlForm('O:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)')
[IO.Directory]::CreateDirectory($stageRoot, $directorySecurity) | Out-Null
$stageItem = Get-Item -LiteralPath $stageRoot -Force
if (($stageItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'Protected staging path cannot be a reparse point.' }
$stageACL = Get-Acl -LiteralPath $stageRoot
$trustedSids = @('S-1-5-18', 'S-1-5-32-544')
if (-not $stageACL.AreAccessRulesProtected -or $stageACL.GetOwner([Security.Principal.SecurityIdentifier]).Value -notin $trustedSids) { throw 'Protected staging directory has an untrusted owner.' }
foreach ($rule in $stageACL.Access) {
  if ($rule.AccessControlType -ne 'Allow' -or $rule.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value -notin $trustedSids) { throw 'Protected staging directory permissions are not restricted to SYSTEM/Administrators.' }
}
$stagedAgent = Join-Path $stageRoot (([Guid]::NewGuid().ToString('N')) + '.exe')
$inputFile = [IO.File]::Open($agent, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
try {
  # Apply owner and protected ACL atomically at creation, before the first byte.
  # A current-user owner would let the unelevated user change its DACL later.
  $fileSecurity = New-Object Security.AccessControl.FileSecurity
  $fileSecurity.SetSecurityDescriptorSddlForm('O:BAD:P(A;;FA;;;SY)(A;;FA;;;BA)')
  $outputFile = [IO.FileStream]::new($stagedAgent, [IO.FileMode]::CreateNew, [Security.AccessControl.FileSystemRights]::Write, [IO.FileShare]::None, 4096, [IO.FileOptions]::None, $fileSecurity)
  try { $inputFile.CopyTo($outputFile); $outputFile.Flush($true) } finally { $outputFile.Dispose() }
} finally { $inputFile.Dispose() }
if ((Get-FileHash -LiteralPath $stagedAgent -Algorithm SHA256).Hash.ToLowerInvariant() -ne $expectedHash) { Remove-Item -LiteralPath $stagedAgent; throw 'Staged agent differs from the trusted release hash; no code was launched.' }
$agent = $stagedAgent
$oldToken = [Environment]::GetEnvironmentVariable('SCARLET_OPERATIONS_TOKEN', 'Process')
$secret = Read-Host 'Enter the one-device security-events source token (not logged)' -AsSecureString
$pointer = [IntPtr]::Zero
try {
  $pointer = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secret)
  $env:SCARLET_OPERATIONS_TOKEN = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($pointer)
  & $agent -mode provision -config $config
  if ($LASTEXITCODE -ne 0) { throw 'Provision failed; no native policy was applied.' }
  & $agent -mode native-plan
  if ($LASTEXITCODE -ne 0) { throw 'Native target inspection failed; protected configuration retained for review.' }
  & $agent -mode service-install -expected-sha256 $expectedHash
  if ($LASTEXITCODE -ne 0) { throw 'Service registration failed; protected configuration retained.' }
  if ($StartService) { Start-Service -Name ScarletEndpointNative }
} finally {
  [Environment]::SetEnvironmentVariable('SCARLET_OPERATIONS_TOKEN', $oldToken, 'Process')
  if ($pointer -ne [IntPtr]::Zero) { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($pointer) }
  if ($secret) { $secret.Dispose() }
  if (Test-Path -LiteralPath $stagedAgent -PathType Leaf) { Remove-Item -LiteralPath $stagedAgent }
}
Write-Output 'Service registered. No native change occurs until the matching device policy is independently approved. User clipboard observation is a separate interactive-session agent.'
