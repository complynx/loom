$ErrorActionPreference='Stop'
$dir=Join-Path (Get-Location) 'management.local/panel-login-qa'
$s=Get-Content "$dir/state.json" -Raw|ConvertFrom-Json
$pat=[IO.File]::ReadAllText("$dir/data/admin.pat").Trim()
$s.grant=Invoke-RestMethod "http://localhost:58390/management/v1/users/$($s.human.userId)/grants" -Method Post -Headers @{Authorization="Bearer $pat"} -ContentType application/json -Body (@{projectId=$s.project.id;roleKeys=@('panel-reader')}|ConvertTo-Json -Compress) -TimeoutSec 10
$s|ConvertTo-Json -Depth 12|Set-Content "$dir/state.json"
'Restored only isolated QA reader project grant.'
# Written by Codex (gpt-6/Codex desktop)
# on behalf of Daniel Drizhuk
