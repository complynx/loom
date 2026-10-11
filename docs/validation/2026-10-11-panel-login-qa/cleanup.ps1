$ErrorActionPreference='Stop'
$repo=[IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../../..'))
$scratch=[IO.Path]::GetFullPath((Join-Path $repo 'management.local/panel-login-qa'))
$expected=[IO.Path]::GetFullPath((Join-Path $repo 'management.local/panel-login-qa'))
if($scratch -ne $expected -or -not $scratch.StartsWith($repo+[IO.Path]::DirectorySeparatorChar)){throw 'Invalid owned scratch path'}
$ownedExecutables=@((Join-Path $scratch 'harness.exe'),(Join-Path $scratch 'harness-next.exe'))
Get-Process | Where-Object {$_.Path -in $ownedExecutables} | Stop-Process
docker compose -p clx-panel-login-qa-20261011 -f "$scratch/compose.yaml" --env-file "$scratch/.env" down --volumes
if($LASTEXITCODE -ne 0){throw 'Owned Compose cleanup failed; scratch preserved'}
Remove-Item -LiteralPath $scratch -Recurse -Force
'Removed only owned panel-login QA processes, containers, network and private scratch.'
# Written by Codex (gpt-6/Codex desktop)
# on behalf of Daniel Drizhuk
