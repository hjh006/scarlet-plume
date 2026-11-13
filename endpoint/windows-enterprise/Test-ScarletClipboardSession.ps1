#requires -Version 5.1
[CmdletBinding()]
param([ValidateSet('Plan','Replay')][string]$Mode='Plan',[string]$OutputPath)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'ScarletClipboard.Common.ps1')
$profile = Get-ScarletClipboardProfile
if ($Mode -eq 'Replay') {
  $state = Join-Path (Get-ScarletClipboardRoot) 'state\guard'
  Assert-ScarletClipboardPrivate -Path $state
  Invoke-ScarletClipboardAgent -Profile $profile -AgentArguments @('-mode','session-replay','-state-dir',$state)
  Write-Output 'Only existing anonymous metadata was replayed; no clipboard capture or clear action was requested.'
  return
}
$plan = Invoke-ScarletClipboardAgent -Profile $profile -AgentArguments @('-mode','session-plan')
$result = [ordered]@{version=1;sourcePlan=($plan | ConvertFrom-Json);office='not_run';edge='not_run';chrome='not_run';notepad='not_run';reactiveClear='not_run';prePasteBlocking='not_guaranteed_for_unintegrated_apps';historyAndCloud='not_covered';windowsRuntimeAcceptance='pending'}
$json = $result | ConvertTo-Json -Depth 12
if ($OutputPath) { Write-ScarletPrivateBytes -Path $OutputPath -Bytes ([Text.Encoding]::UTF8.GetBytes($json)) }
$json
