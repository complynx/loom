$ErrorActionPreference='Stop'
$dir=Join-Path (Get-Location) 'management.local/panel-login-qa'
$s=Get-Content "$dir/state.json" -Raw|ConvertFrom-Json
$pat=[IO.File]::ReadAllText("$dir/data/admin.pat").Trim()
Invoke-RestMethod "http://localhost:58390/management/v1/users/$($s.human.userId)/grants/$($s.grant.userGrantId)" -Method Delete -Headers @{Authorization="Bearer $pat"} -TimeoutSec 10|Out-Null
'Removed only isolated QA reader project grant; issued browser token remains unchanged.'
# Written by Codex (gpt-6/Codex desktop)
# on behalf of Daniel Drizhuk
