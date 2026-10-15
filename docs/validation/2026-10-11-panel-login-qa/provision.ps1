$ErrorActionPreference='Stop'
$dir=Join-Path (Get-Location) 'management.local/panel-login-qa'
$base='http://localhost:58390'
$pat=[IO.File]::ReadAllText("$dir/data/admin.pat").Trim()
$h=@{Authorization="Bearer $pat"}
function API($path,$body,$method='Post') {
 try { Invoke-RestMethod "$base/$path" -Method $method -Headers $h -ContentType application/json -Body ($body|ConvertTo-Json -Depth 8 -Compress) -TimeoutSec 10 } catch { throw "Fixture API failed: $method $path status=$([int]$_.Exception.Response.StatusCode)" }
}
API 'v2/features/instance' @{loginV2=@{required=$false}} 'Put'|Out-Null
$p=API 'management/v1/projects' @{name='Panel Login QA';projectRoleAssertion=$true}
$app=API "management/v1/projects/$($p.id)/apps/api" @{name='Panel Introspection';authMethod='API_AUTH_METHOD_BASIC'}
$oidc=API "management/v1/projects/$($p.id)/apps/oidc" @{name='Panel Browser PKCE';redirectUris=@('http://localhost:58380/admin');responseTypes=@('OIDC_RESPONSE_TYPE_CODE');grantTypes=@('OIDC_GRANT_TYPE_AUTHORIZATION_CODE');appType='OIDC_APP_TYPE_USER_AGENT';authMethodType='OIDC_AUTH_METHOD_TYPE_NONE';version='OIDC_VERSION_1_0';accessTokenType='OIDC_TOKEN_TYPE_JWT';devMode=$true}
API "management/v1/projects/$($p.id)/roles" @{roleKey='panel-reader';displayName='Panel Reader'}|Out-Null
$password='Fixture-'+[Guid]::NewGuid().ToString('N')+'!'
$human=API 'management/v1/users/human' @{userName='panel-reader';profile=@{firstName='Panel';lastName='Reader'};email=@{email='panel-reader@example.invalid';isEmailVerified=$true};initialPassword=$password}
$human2=API 'management/v1/users/human' @{userName='panel-denied';profile=@{firstName='Panel';lastName='Denied'};email=@{email='panel-denied@example.invalid';isEmailVerified=$true};initialPassword=$password}
# Human v1 initial password requires explicit clearing for unattended browser fixtures.
foreach($humanId in @($human.userId,$human2.userId)){API "management/v1/users/$humanId/password" @{password=$password;noChangeRequired=$true} 'Post'|Out-Null}
$grant=API "management/v1/users/$($human.userId)/grants" @{projectId=$p.id;roleKeys=@('panel-reader')}
$org=Invoke-RestMethod "$base/management/v1/orgs/me" -Headers $h -TimeoutSec 10
@{human=$human;denied=$human2;project=$p;app=$app;oidc=$oidc;grant=$grant;organization=$org;password=$password}|ConvertTo-Json -Depth 12|Set-Content "$dir/state.json"
'Provisioned isolated public PKCE browser client, reader and denied users; credentials remain in ignored scratch.'

# Written by Codex (gpt-6/Codex desktop)
# on behalf of Daniel Drizhuk
