# Dump the Windows UI Automation tree (what Narrator reads) of one process's window:
#   powershell -File scripts/uia-dump.ps1 -ProcId <pid>
# One line per element: control type, focusable flag, accessible name.
param([int]$ProcId)
Add-Type -AssemblyName UIAutomationClient, UIAutomationTypes
$AE = [System.Windows.Automation.AutomationElement]
$cond = New-Object System.Windows.Automation.PropertyCondition($AE::ProcessIdProperty, $ProcId)
$win = $null
for ($i = 0; $i -lt 20 -and -not $win; $i++) {
  $win = $AE::RootElement.FindFirst([System.Windows.Automation.TreeScope]::Children, $cond)
  if (-not $win) { Start-Sleep -Milliseconds 250 }
}
if (-not $win) { Write-Output "NO-WINDOW"; exit 1 }
$all = $win.FindAll([System.Windows.Automation.TreeScope]::Descendants, [System.Windows.Automation.Condition]::TrueCondition)
foreach ($e in $all) {
  $c = $e.Current
  "{0}`t{1}`t{2}" -f $c.ControlType.ProgrammaticName.Replace('ControlType.', ''), $c.IsKeyboardFocusable, $c.Name
}
