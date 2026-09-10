param([Parameter(Mandatory=$true)][int]$RootProcessId)
$ErrorActionPreference = 'Stop'
# Do not collect command lines, environment, owners, or executable paths.
$properties = @('ProcessId', 'ParentProcessId', 'Name', 'CreationDate', 'KernelModeTime', 'UserModeTime', 'WorkingSetSize')
$root = Get-CimInstance Win32_Process -Filter "ProcessId=$RootProcessId" -Property $properties
if ($null -eq $root) {
    @{status='root_missing'; processes=@()} | ConvertTo-Json -Compress
    exit 0
}
$queue = [System.Collections.Generic.Queue[object]]::new()
$queue.Enqueue($root)
$seen = [System.Collections.Generic.HashSet[int]]::new()
$rows = [System.Collections.Generic.List[object]]::new()
while ($queue.Count -gt 0 -and $rows.Count -lt 64) {
    $item = $queue.Dequeue()
    if (-not $seen.Add([int]$item.ProcessId)) { continue }
    $rows.Add(@{
        pid=[int]$item.ProcessId; parentPid=[int]$item.ParentProcessId; name=$item.Name
        createdAtUtc=$item.CreationDate.ToUniversalTime().ToString('o')
        cpuSeconds=([double]$item.KernelModeTime + [double]$item.UserModeTime) / 10000000
        workingSetBytes=[long]$item.WorkingSetSize
    })
    $children = @(Get-CimInstance Win32_Process -Filter "ParentProcessId=$($item.ProcessId)" -Property $properties)
    foreach ($child in $children) {
        if ($child.CreationDate -ge $root.CreationDate) { $queue.Enqueue($child) }
    }
}
@{status='captured'; truncated=($queue.Count -gt 0); processes=@($rows.ToArray())} | ConvertTo-Json -Depth 4 -Compress
