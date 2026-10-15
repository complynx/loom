# Replay the isolated QA investigation. Use a disposable PostgreSQL DSN.
$ErrorActionPreference = 'Stop'
if (-not $env:CLX_TEST_DATABASE_DSN) { throw 'Set CLX_TEST_DATABASE_DSN to a disposable PostgreSQL database.' }
$qaRepository = (Resolve-Path (Join-Path $PSScriptRoot '../../..')).Path
$qaScratch = Join-Path $PSScriptRoot '_run'
$qaContainer = 'clx-acceptance-functionalqa-20261011'
$qaVolume = $qaContainer
if (Test-Path -LiteralPath $qaScratch) { throw 'QA scratch directory already exists; inspect it before replay.' }
if (docker ps -a --filter "name=^/$qaContainer$" --format '{{.Names}}') { throw 'QA container already exists; inspect it before replay.' }
if (docker volume ls --filter "name=^$qaVolume$" --format '{{.Name}}') { throw 'QA volume already exists; inspect it before replay.' }
Push-Location $qaRepository
try {
    docker build -f examples/acceptance.Dockerfile -t clx-acceptance-qa:20261011 .
    if ($LASTEXITCODE) { throw 'Fixture image build failed.' }
    docker volume create $qaVolume
    if ($LASTEXITCODE) { throw 'QA volume creation failed.' }
    docker run -d --name $qaContainer --read-only --cap-drop ALL --security-opt no-new-privileges --pids-limit 32 --memory 64m -p 127.0.0.1:58090:8090 -v "${qaVolume}:/data" -e CLX_QA_STATE_PATH=/data/state.json -e CLX_QA_READ_TOKEN=isolated-functionalqa-read -e CLX_QA_WRITE_TOKEN=isolated-functionalqa-write clx-acceptance-qa:20261011
    if ($LASTEXITCODE) { throw 'QA fixture launch failed.' }
    New-Item -ItemType Directory -Path $qaScratch | Out-Null
    foreach ($qaTemplate in Get-ChildItem -LiteralPath $PSScriptRoot -Filter '*_test.go.txt') {
        Copy-Item -LiteralPath $qaTemplate.FullName -Destination (Join-Path $qaScratch $qaTemplate.Name.Replace('.go.txt','.go'))
    }
    go test ./docs/validation/2026-10-11-acceptance-qa/_run -count=1 -v
    if ($LASTEXITCODE) { throw 'Behavioral QA failed.' }
} finally {
    docker rm -f $qaContainer
    docker volume rm $qaVolume
    if (Test-Path -LiteralPath $qaScratch) {
        $qaResolved = (Resolve-Path -LiteralPath $qaScratch).Path
        if ($qaResolved -ne (Join-Path $PSScriptRoot '_run')) { throw 'Unexpected QA scratch path.' }
        Remove-Item -LiteralPath $qaResolved -Recurse -Force
    }
    Pop-Location
}
