#requires -Version 5.1
[CmdletBinding()]
param()
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'ScarletClipboard.Common.ps1')
$profile = Get-ScarletClipboardProfile
$state = Join-Path (Get-ScarletClipboardRoot) 'state\guard'
Assert-ScarletClipboardPrivate -Path $state
Invoke-ScarletClipboardAgent -Profile $profile -AgentArguments @('-mode','session-watch','-state-dir',$state)
