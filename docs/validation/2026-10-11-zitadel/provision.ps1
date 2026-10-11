$ErrorActionPreference='Stop'
$dir=$PSScriptRoot
$pat=[IO.File]::ReadAllText("$dir/data/admin.pat").Trim()
$h=@{Authorization="Bearer $pat"}
$p=Get-Content "$dir/project.json" -Raw|ConvertFrom-Json
function API($path,$body,$method='Post'){Invoke-RestMethod "http://localhost:18080/management/v1/$path" -Method $method -Headers $h -ContentType application/json -Body ($body|ConvertTo-Json -Depth 8 -Compress)}
API "projects/$($p.id)/roles" @{roleKey='qa-user';displayName='QA User'} | Out-Null
API "projects/$($p.id)/roles" @{roleKey='qa-service';displayName='QA Service'} | Out-Null
$human=API 'users/human' @{userName='qa-human';profile=@{firstName='QA';lastName='User'};email=@{email='qa-user@example.invalid';isEmailVerified=$true};initialPassword=$env:QA_HUMAN_PASSWORD}
$machine=API 'users/machine' @{userName='qa-service';name='QA Service';accessTokenType='ACCESS_TOKEN_TYPE_JWT'}
$secret=API "users/$($machine.userId)/secret" @{} 'Put'
API "users/$($human.userId)/grants" @{projectId=$p.id;roleKeys=@('qa-user')}|Out-Null
API "users/$($machine.userId)/grants" @{projectId=$p.id;roleKeys=@('qa-service')}|Out-Null
$state=@{human=$human;machine=$machine;secret=$secret;project=$p}
$state|ConvertTo-Json -Depth 10|Set-Content "$dir/provisioned.json"
'Test human, machine, roles and grants created'