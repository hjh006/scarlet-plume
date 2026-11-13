#requires -Version 5.1
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
function Assert-ScarletInteractiveUser {
  if ($PSVersionTable.PSEdition -ne 'Desktop') { throw 'Windows PowerShell 5.1 Desktop is required.' }
  if ([Security.Principal.WindowsIdentity]::GetCurrent().IsSystem -or [Diagnostics.Process]::GetCurrentProcess().SessionId -eq 0) { throw 'Run in the authorized interactive user session, never SYSTEM/session 0.' }
  $principal = [Security.Principal.WindowsPrincipal]::new([Security.Principal.WindowsIdentity]::GetCurrent())
  if ($principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { throw 'Run this per-user clipboard package without elevation. Machine native controls use their separate administrator installer.' }
}
function Get-ScarletClipboardRoot { return (Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'ScarletPlumeClipboard') }
function Get-ScarletClipboardSDDL([switch]$File) {
  $sid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
  if ($File) { return ('O:' + $sid + 'D:P(A;;FA;;;SY)(A;;FA;;;' + $sid + ')') }
  return ('O:' + $sid + 'D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;' + $sid + ')')
}
function Assert-ScarletClipboardPath([string]$Path) {
  $full = [IO.Path]::GetFullPath($ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($Path))
  if ($full -notmatch '^[A-Za-z]:\\' -or $full.Substring(3).Contains(':')) { throw 'Clipboard package paths must be local drive paths without alternate streams.' }
  $cursor = $full
  while ($cursor) {
    $item = Get-Item -LiteralPath $cursor -Force -ErrorAction SilentlyContinue
    if ($item -and ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'Clipboard package paths and their parents cannot be reparse points.' }
    $parent = [IO.Path]::GetDirectoryName($cursor)
    if ($parent -eq $cursor) { break }
    $cursor = $parent
  }
}
function Initialize-ScarletClipboardFileSafety {
  if ('ScarletClipboard.FileSafety' -as [type]) { return }
  # Inspect the leaf through a locked handle without following a reparse point.
  Add-Type -TypeDefinition @'
using System;
using System.IO;
using System.Runtime.InteropServices;
using Microsoft.Win32.SafeHandles;
namespace ScarletClipboard {
  public static class FileSafety {
    [StructLayout(LayoutKind.Sequential)]
    private struct FileInfo {
      public uint Attributes;
      public System.Runtime.InteropServices.ComTypes.FILETIME Created, Accessed, Written;
      public uint VolumeSerial, SizeHigh, SizeLow, LinkCount, IndexHigh, IndexLow;
    }
    [DllImport("kernel32.dll", CharSet=CharSet.Unicode, ExactSpelling=true, SetLastError=true)]
    private static extern SafeFileHandle CreateFileW(string path, uint access, uint share, IntPtr security, uint creation, uint flags, IntPtr template);
    [DllImport("kernel32.dll", SetLastError=true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool GetFileInformationByHandle(SafeFileHandle file, out FileInfo info);
    public static FileStream OpenRead(string path) {
      SafeFileHandle handle = CreateFileW(path, 0x80000000, 1, IntPtr.Zero, 3, 0x00200000, IntPtr.Zero);
      if (handle.IsInvalid) { handle.Dispose(); throw new IOException("Cannot lock clipboard package file for reading."); }
      FileInfo info;
      if (!GetFileInformationByHandle(handle, out info) || (info.Attributes & 0x410) != 0 || info.LinkCount != 1) {
        handle.Dispose(); throw new IOException("Clipboard package file must be a regular file with one link.");
      }
      try { return new FileStream(handle, FileAccess.Read); } catch { handle.Dispose(); throw; }
    }
  }
}
'@ | Out-Null
}
function Read-ScarletClipboardStream([IO.Stream]$Stream,[long]$Limit) {
  if ($Limit -lt 1 -or ($Stream.CanSeek -and $Stream.Length -gt $Limit)) { throw 'Clipboard package file exceeds its read budget.' }
  $memory = New-Object IO.MemoryStream
  $buffer = New-Object byte[] 8192
  try {
    while (($count = $Stream.Read($buffer,0,$buffer.Length)) -gt 0) {
      if ($memory.Length + $count -gt $Limit) { throw 'Clipboard package file exceeds its read budget.' }
      $memory.Write($buffer,0,$count)
    }
    return ,($memory.ToArray())
  } finally { [Array]::Clear($buffer,0,$buffer.Length); $memory.Dispose() }
}
function Read-ScarletClipboardFile([string]$Path,[long]$Limit=67108864) {
  $Path = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($Path)
  Assert-ScarletClipboardPath -Path $Path
  Initialize-ScarletClipboardFileSafety
  $stream = [ScarletClipboard.FileSafety]::OpenRead([IO.Path]::GetFullPath($Path))
  try { return ,(Read-ScarletClipboardStream -Stream $stream -Limit $Limit) } finally { $stream.Dispose() }
}
function Get-ScarletClipboardHash([byte[]]$Bytes) {
  $sha = [Security.Cryptography.SHA256]::Create()
  try { return [BitConverter]::ToString($sha.ComputeHash($Bytes)).Replace('-','').ToLowerInvariant() } finally { $sha.Dispose() }
}
function Assert-ScarletClipboardPrivate([string]$Path) {
  $Path = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($Path)
  Assert-ScarletClipboardPath -Path $Path
  $item = Get-Item -LiteralPath $Path -Force
  $sid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
  $acl = Get-Acl -LiteralPath $Path
  if ($acl.GetOwner([Security.Principal.SecurityIdentifier]).Value -ne $sid) { throw 'Clipboard path must be owned by this user.' }
  if ($item.PSIsContainer -and -not $acl.AreAccessRulesProtected) { throw 'Clipboard private directory must have protected permissions.' }
  $found = @{}
  foreach ($rule in $acl.Access) {
    $identity = $rule.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value
    if ($rule.AccessControlType -ne 'Allow' -or $identity -notin @($sid,'S-1-5-18') -or ($rule.FileSystemRights -band [Security.AccessControl.FileSystemRights]::FullControl) -ne [Security.AccessControl.FileSystemRights]::FullControl -or $rule.PropagationFlags -ne [Security.AccessControl.PropagationFlags]::None) { throw 'Clipboard path permissions must grant only this user and SYSTEM full control.' }
    if ($item.PSIsContainer -and ($rule.InheritanceFlags -band ([Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [Security.AccessControl.InheritanceFlags]::ObjectInherit)) -ne ([Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [Security.AccessControl.InheritanceFlags]::ObjectInherit)) { throw 'Clipboard private directory permissions must protect child files and directories.' }
    $found[$identity] = $true
  }
  if (-not $found.ContainsKey($sid) -or -not $found.ContainsKey('S-1-5-18')) { throw 'Clipboard private ACL must include this user and SYSTEM.' }
  if (-not $item.PSIsContainer) {
    Initialize-ScarletClipboardFileSafety
    $stream = [ScarletClipboard.FileSafety]::OpenRead([IO.Path]::GetFullPath($Path))
    $stream.Dispose()
  }
}
function New-ScarletClipboardDirectory([string]$Path) {
  $Path = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($Path)
  Assert-ScarletClipboardPath -Path $Path
  $parent = [IO.Path]::GetDirectoryName([IO.Path]::GetFullPath($Path))
  if (-not [IO.Directory]::Exists($parent)) { throw 'Create private clipboard directories one level at a time.' }
  $security = New-Object Security.AccessControl.DirectorySecurity
  $security.SetSecurityDescriptorSddlForm((Get-ScarletClipboardSDDL))
  [IO.Directory]::CreateDirectory($Path,$security) | Out-Null
  Assert-ScarletClipboardPrivate -Path $Path
}
function Write-ScarletPrivateBytes([string]$Path,[byte[]]$Bytes) {
  $Path = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($Path)
  Assert-ScarletClipboardPath -Path $Path
  if (-not [IO.Directory]::Exists([IO.Path]::GetDirectoryName([IO.Path]::GetFullPath($Path)))) { throw 'Clipboard output parent directory must already exist.' }
  $security = New-Object Security.AccessControl.FileSecurity
  $security.SetSecurityDescriptorSddlForm((Get-ScarletClipboardSDDL -File))
  $stream = [IO.FileStream]::new($Path,[IO.FileMode]::CreateNew,[Security.AccessControl.FileSystemRights]::Write,[IO.FileShare]::None,4096,[IO.FileOptions]::None,$security)
  try { $stream.Write($Bytes,0,$Bytes.Length); $stream.Flush($true) } finally { $stream.Dispose() }
  Assert-ScarletClipboardPrivate -Path $Path
}
function Get-ScarletClipboardProfile {
  Assert-ScarletInteractiveUser
  $root = Get-ScarletClipboardRoot
  Assert-ScarletClipboardPrivate -Path $root
  Assert-ScarletClipboardPrivate -Path (Join-Path $root 'bin')
  $path = Join-Path $root 'profile.dpapi'
  Assert-ScarletClipboardPrivate -Path $path
  $encrypted = $null; $bytes = $null; $profile = $null
  try {
    Add-Type -AssemblyName System.Security
    $encrypted = Read-ScarletClipboardFile -Path $path -Limit 65536
    $bytes = [Security.Cryptography.ProtectedData]::Unprotect($encrypted,$null,[Security.Cryptography.DataProtectionScope]::CurrentUser)
    $profile = [Text.Encoding]::UTF8.GetString($bytes) | ConvertFrom-Json
  } catch { throw 'Cannot read this user protected clipboard profile; no credentials were displayed.' } finally {
    if ($null -ne $bytes) { [Array]::Clear($bytes,0,$bytes.Length) }
    if ($null -ne $encrypted) { [Array]::Clear($encrypted,0,$encrypted.Length) }
  }
  $required = @('version','platformOrigin','scope','deviceId','sourceToken','keyHex','agent','files','windowsRuntimeAcceptance')
  if (@($profile.PSObject.Properties.Name).Count -ne $required.Count) { throw 'Protected clipboard profile is invalid.' }
  foreach ($key in $required) { if ($key -notin @($profile.PSObject.Properties.Name)) { throw 'Protected clipboard profile is invalid.' } }
  if ($profile.version -ne 1 -or $profile.sourceToken -isnot [string] -or $profile.sourceToken -notmatch '^[\x21-\x7e]{1,4096}$' -or $profile.keyHex -notmatch '^[0-9a-f]{64}$' -or $profile.scope -isnot [string] -or $profile.scope.Length -lt 1 -or $profile.scope.Length -gt 80 -or $profile.deviceId -isnot [string] -or $profile.deviceId.Length -lt 1 -or $profile.deviceId.Length -gt 256) { throw 'Protected clipboard profile is invalid.' }
  $platform = $null
  if (-not [Uri]::TryCreate($profile.platformOrigin,[UriKind]::Absolute,[ref]$platform) -or $platform.Scheme -ne 'https' -or $platform.UserInfo -or $platform.Query -or $platform.Fragment -or $platform.AbsolutePath -notin @('','/')) { throw 'Protected platform origin must be HTTPS.' }
  if ($profile.agent -notin @('scarlet-endpoint-agent-windows-amd64.exe','scarlet-endpoint-agent-windows-arm64.exe')) { throw 'Invalid pinned clipboard agent.' }
  $expected = @($profile.agent,'ScarletClipboard.Common.ps1','Start-ScarletClipboardSession.ps1','Remove-ScarletClipboardSession.ps1','Test-ScarletClipboardSession.ps1','Invoke-ScarletClipboardText.ps1')
  if (@($profile.files).Count -ne $expected.Count) { throw 'Protected package whitelist is invalid.' }
  foreach ($name in $expected) {
    $entry = @($profile.files | Where-Object { $_.path -ceq $name })
    if ($entry.Count -ne 1 -or $entry[0].sha256 -notmatch '^[0-9a-f]{64}$') { throw 'Invalid pinned clipboard file.' }
    $path = Join-Path (Join-Path $root 'bin') $name
    Assert-ScarletClipboardPrivate -Path $path
    $raw = Read-ScarletClipboardFile -Path $path
    if ((Get-ScarletClipboardHash -Bytes $raw) -ne $entry[0].sha256) { throw 'Installed clipboard file differs from the pinned approved package.' }
  }
  return $profile
}
function Invoke-ScarletClipboardAgent($Profile,[string[]]$AgentArguments) {
  $names = @('SCARLET_OPERATIONS_TOKEN','SCARLET_ENDPOINT_KEY_HEX','SCARLET_ENDPOINT_SCOPE','SCARLET_ENDPOINT_DEVICE_ID','SCARLET_ENDPOINT_SUBJECT_ID')
  $old = @{}; foreach ($name in $names) { $old[$name] = [Environment]::GetEnvironmentVariable($name,'Process') }
  $agentStream = $null
  try {
    $agent = Join-Path (Join-Path (Get-ScarletClipboardRoot) 'bin') $Profile.agent
    Assert-ScarletClipboardPrivate -Path $agent
    $entry = @($Profile.files | Where-Object { $_.path -ceq $Profile.agent })
    $agentStream = [ScarletClipboard.FileSafety]::OpenRead($agent)
    $raw = Read-ScarletClipboardStream -Stream $agentStream -Limit 67108864
    if ($entry.Count -ne 1 -or (Get-ScarletClipboardHash -Bytes $raw) -ne $entry[0].sha256) { throw 'Clipboard executable changed; nothing was launched.' }
    $env:SCARLET_OPERATIONS_TOKEN = $Profile.sourceToken; $env:SCARLET_ENDPOINT_KEY_HEX = $Profile.keyHex
    $env:SCARLET_ENDPOINT_SCOPE = $Profile.scope; $env:SCARLET_ENDPOINT_DEVICE_ID = $Profile.deviceId
    $env:SCARLET_ENDPOINT_SUBJECT_ID = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    & $agent -url $Profile.platformOrigin @AgentArguments
    if ($LASTEXITCODE -ne 0) { throw 'Clipboard agent did not complete successfully; inspect its anonymized evidence. Do not retry a native action with a new operation ID automatically.' }
  } finally {
    if ($null -ne $agentStream) { $agentStream.Dispose() }
    foreach ($name in $names) { [Environment]::SetEnvironmentVariable($name,$old[$name],'Process') }
  }
}
function Get-ScarletClipboardPowerShell { return (Join-Path $PSHOME 'powershell.exe') }
function Get-ScarletClipboardTaskName {
  $sha = [Security.Cryptography.SHA256]::Create()
  try { $hash = $sha.ComputeHash([Text.Encoding]::UTF8.GetBytes([Security.Principal.WindowsIdentity]::GetCurrent().User.Value)); return ('ScarletClipboardSession-' + ([BitConverter]::ToString($hash).Replace('-','').ToLowerInvariant().Substring(0,16))) } finally { $sha.Dispose() }
}
