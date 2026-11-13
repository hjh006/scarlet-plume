#requires -Version 5.1
#requires -RunAsAdministrator
[CmdletBinding(SupportsShouldProcess=$true, ConfirmImpact='High')]
param()
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
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
if (-not (Test-Path -LiteralPath $agent -PathType Leaf)) { throw 'Installed agent not found. Preserve protected recovery data and ask the administrator to inspect it.' }
if (-not $PSCmdlet.ShouldProcess('ScarletEndpointNative', 'Stop service, restore owned native state and remove service registration while retaining evidence')) { return }
$service = Get-Service -Name ScarletEndpointNative -ErrorAction SilentlyContinue
if ($service -and $service.Status -ne 'Stopped') { Stop-Service -Name ScarletEndpointNative -ErrorAction Stop }
& $agent -mode native-rollback
if ($LASTEXITCODE -ne 0) { throw 'Rollback is incomplete or conflicted. Service registration, originals and evidence have been retained.' }
& $agent -mode service-remove
if ($LASTEXITCODE -ne 0) { throw 'Service removal failed. Protected data retained.' }
Write-Output 'Service registration removed. The executable, DPAPI configuration and receipt/recovery files are retained for audit and replay. No evidence directory was deleted.'
