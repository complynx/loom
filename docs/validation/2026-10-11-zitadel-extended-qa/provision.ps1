$ErrorActionPreference='Stop'
$dir=Join-Path (Get-Location) 'management.local/zitadel-extended-qa'
$base='http://localhost:18090'
$pat=[IO.File]::ReadAllText("$dir/data/admin.pat").Trim()
$h=@{Authorization="Bearer $pat"}
function API($path,$body,$method='Post',$org='') {
 $headers=$h.Clone(); if($org){$headers['x-zitadel-orgid']=$org}
 Invoke-RestMethod "$base/$path" -Method $method -Headers $headers -ContentType application/json -Body ($body|ConvertTo-Json -Depth 8 -Compress)
}
$p=API 'management/v1/projects' @{name='Extended QA';projectRoleAssertion=$true}
$app=API "management/v1/projects/$($p.id)/apps/api" @{name='QA Introspection';authMethod='API_AUTH_METHOD_BASIC'}
$oidc=API "management/v1/projects/$($p.id)/apps/oidc" @{name='QA Exchange';redirectUris=@('http://localhost:18091/callback');responseTypes=@('OIDC_RESPONSE_TYPE_CODE');grantTypes=@('OIDC_GRANT_TYPE_AUTHORIZATION_CODE','OIDC_GRANT_TYPE_REFRESH_TOKEN','OIDC_GRANT_TYPE_TOKEN_EXCHANGE');appType='OIDC_APP_TYPE_WEB';authMethodType='OIDC_AUTH_METHOD_TYPE_BASIC';version='OIDC_VERSION_1_0';accessTokenType='OIDC_TOKEN_TYPE_JWT'}
foreach($role in @('qa-user','qa-service')) {API "management/v1/projects/$($p.id)/roles" @{roleKey=$role;displayName=$role}|Out-Null}
$human=API 'management/v1/users/human' @{userName='qa-human';profile=@{firstName='QA';lastName='User'};email=@{email='qa-user@example.invalid';isEmailVerified=$true};initialPassword='QaFixture-Password123!'}
$machine=API 'management/v1/users/machine' @{userName='qa-service';name='QA Service';accessTokenType='ACCESS_TOKEN_TYPE_JWT'}
$secret=API "management/v1/users/$($machine.userId)/secret" @{} 'Put'
$grant=API "management/v1/users/$($human.userId)/grants" @{projectId=$p.id;roleKeys=@('qa-user')}
API "management/v1/users/$($machine.userId)/grants" @{projectId=$p.id;roleKeys=@('qa-service')}|Out-Null
API 'admin/v1/policies/security' @{enableImpersonation=$true} 'Put'|Out-Null
API 'management/v1/orgs/me/members' @{userId=$machine.userId;roles=@('ORG_OWNER','ORG_END_USER_IMPERSONATOR')}|Out-Null
$org=API 'management/v1/orgs' @{name='QA Other Organization'}
$human2=API 'management/v1/users/human' @{userName='qa-human-other';profile=@{firstName='QA';lastName='Other'};email=@{email='qa-other@example.invalid';isEmailVerified=$true};initialPassword='QaFixture-Password123!'} 'Post' $org.id
API 'management/v1/orgs/me/members' @{userId=$machine.userId;roles=@('ORG_OWNER','ORG_END_USER_IMPERSONATOR')} 'Post' $org.id|Out-Null
$scope="openid urn:zitadel:iam:org:project:id:$($p.id):aud urn:zitadel:iam:org:project:roles urn:zitadel:iam:user:resourceowner"
$service=Invoke-RestMethod "$base/oauth/v2/token" -Method Post -Body @{grant_type='client_credentials';client_id=$secret.clientId;client_secret=$secret.clientSecret;scope=$scope}
$basic=[Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes("$($oidc.clientId):$($oidc.clientSecret)"))
function Exchange($id){ Invoke-RestMethod "$base/oauth/v2/token" -Method Post -Headers @{Authorization="Basic $basic"} -Body @{grant_type='urn:ietf:params:oauth:grant-type:token-exchange';subject_token=$id;subject_token_type='urn:zitadel:params:oauth:token-type:user_id';actor_token=$service.access_token;actor_token_type='urn:ietf:params:oauth:token-type:access_token';requested_token_type='urn:ietf:params:oauth:token-type:jwt';scope=$scope} }
$user=Exchange $human.userId; $user2=Exchange $human2.userId
@{human=$human;human2=$human2;machine=$machine;project=$p;app=$app;oidc=$oidc;secret=$secret;grant=$grant;org2=$org;scope=$scope;service=$service;user=$user;user2=$user2}|ConvertTo-Json -Depth 12|Set-Content "$dir/state.json"
'Provisioned two actual organizations and live JWTs; credentials saved only in ignored scratch.'

# Written by Codex (gpt-6/Codex desktop)
# on behalf of Daniel Drizhuk
