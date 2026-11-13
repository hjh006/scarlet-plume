#requires -Version 5.1
[CmdletBinding(SupportsShouldProcess=$true,ConfirmImpact='High')]
param([Parameter(Mandatory=$true)][ValidateRange(1,[long]::MaxValue)][long]$PolicyId,[Parameter(Mandatory=$true)][string]$AuthorizedRoot,[Parameter(Mandatory=$true)][string]$RelativeTextFile)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'ScarletClipboard.Common.ps1')
$profile = Get-ScarletClipboardProfile
# An application explicitly integrates this mediated write. Its own native Ctrl+C
# path remains outside this preflight; no input hooks or process injection occur.
if (-not $PSCmdlet.ShouldProcess('Explicit authorized text file and independently approved controlled_operations clipboard policy','Inspect and conditionally write Unicode text through the integrated application path')) { return }
$operation = [Guid]::NewGuid().ToString('N')
$state = Join-Path (Get-ScarletClipboardRoot) 'state\controlled'
Assert-ScarletClipboardPrivate -Path $state
Assert-ScarletClipboardPath -Path $AuthorizedRoot
Invoke-ScarletClipboardAgent -Profile $profile -AgentArguments @('-mode','session-copy','-policy',$PolicyId.ToString(),'-state-dir',$state,'-operation-id',$operation,'-from-root',(Resolve-Path -LiteralPath $AuthorizedRoot).Path,'-source',$RelativeTextFile)
