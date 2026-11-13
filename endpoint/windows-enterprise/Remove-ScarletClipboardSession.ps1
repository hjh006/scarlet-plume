#requires -Version 5.1
[CmdletBinding(SupportsShouldProcess=$true,ConfirmImpact='High')]
param()
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'ScarletClipboard.Common.ps1')
$profile = Get-ScarletClipboardProfile
$root = Get-ScarletClipboardRoot
if (-not $PSCmdlet.ShouldProcess('Current user clipboard guard','Remove its limited logon task and request observer stop, while retaining private source configuration and all evidence')) { return }
$name = Get-ScarletClipboardTaskName
$task = Get-ScheduledTask -TaskName $name -TaskPath '\' -ErrorAction SilentlyContinue
if ($task) {
  $sid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
  $expected = Join-Path (Join-Path $root 'bin') 'Start-ScarletClipboardSession.ps1'
  $principalSID = $null; $triggerSID = $null
  try {
    $principalSID = if ($task.Principal.UserId -match '^S-1-') { $task.Principal.UserId } else { ([Security.Principal.NTAccount]::new($task.Principal.UserId)).Translate([Security.Principal.SecurityIdentifier]).Value }
    if (@($task.Triggers).Count -eq 1 -and $task.Triggers[0].CimClass.CimClassName -eq 'MSFT_TaskLogonTrigger') {
      $triggerSID = if ($task.Triggers[0].UserId -match '^S-1-') { $task.Triggers[0].UserId } else { ([Security.Principal.NTAccount]::new($task.Triggers[0].UserId)).Translate([Security.Principal.SecurityIdentifier]).Value }
    }
  } catch { throw 'Existing task identity cannot be confirmed; no task was changed.' }
  if (@($task).Count -ne 1 -or $task.TaskPath -ne '\' -or $task.TaskName -cne $name -or $principalSID -ne $sid -or [string]$task.Principal.LogonType -notin @('Interactive','3') -or [string]$task.Principal.RunLevel -notin @('Limited','0') -or $triggerSID -ne $sid -or @($task.Actions).Count -ne 1 -or $task.Actions[0].CimClass.CimClassName -ne 'MSFT_TaskExecAction' -or $task.Actions[0].Execute -ne (Get-ScarletClipboardPowerShell) -or $task.Actions[0].WorkingDirectory -or $task.Actions[0].Arguments -cne ('-NoProfile -NonInteractive -File "' + $expected + '"')) { throw 'Existing task does not match this installed user guard; no task was changed.' }
  # Do not terminate the process: let the guard stop and seal its final receipt.
  Disable-ScheduledTask -TaskName $name -TaskPath '\' | Out-Null
  Unregister-ScheduledTask -TaskName $name -TaskPath '\' -Confirm:$false
}
$state = Join-Path $root 'state\guard'
Assert-ScarletClipboardPrivate -Path $state
$runningMarker = Join-Path $state 'running.marker'
$wasRunning = Test-Path -LiteralPath $runningMarker
Assert-ScarletClipboardPath -Path $runningMarker
Invoke-ScarletClipboardAgent -Profile $profile -AgentArguments @('-mode','session-stop','-state-dir',$state)
$deadline = [DateTime]::UtcNow.AddSeconds(15)
while ((Test-Path -LiteralPath $runningMarker) -and [DateTime]::UtcNow -lt $deadline) {
  Assert-ScarletClipboardPath -Path $runningMarker
  Start-Sleep -Milliseconds 250
}
if (Test-Path -LiteralPath $runningMarker) { throw 'Observer stop is not yet confirmed, possibly blocked in a native call. Configuration, executable and evidence were retained; do not claim a completed stop.' }
if ($wasRunning) { Write-Output 'Observer stop confirmed: its running marker disappeared.' } else { Write-Output 'Stop was requested; no running marker was observed. This does not prove that an observer was running or stopped.' }
Write-Output 'Configuration, installed files and evidence retained. Cleared content is never restored; this package does not delete clipboard history or cloud copies.'
