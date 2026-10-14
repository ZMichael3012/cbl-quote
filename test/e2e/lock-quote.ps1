# E2E CBL-6.2: LockQuote/GetRate на compose-стенде quote.
# Запуск из каталога сервиса: powershell -NoProfile -File test/e2e/lock-quote.ps1
# Сценарий: compose up -> healthz -> GetRate -> LockQuote -> строка в PostgreSQL -> compose down.
$ErrorActionPreference = "Stop"

$RepoRoot = Resolve-Path (Join-Path $PSScriptRoot "..\..")
$ComposeFile = Join-Path $RepoRoot "deploy\docker-compose.yml"

function Invoke-Grpcurl([string]$Json, [string]$Method) {
    $output = $Json | docker run --rm -i fullstorydev/grpcurl -plaintext -d "@" host.docker.internal:9070 $Method
    if ($LASTEXITCODE -ne 0) { throw "grpcurl $Method failed: $output" }
    return ($output -join "`n") | ConvertFrom-Json
}

Push-Location $RepoRoot
try {
    docker compose -f $ComposeFile --profile app up -d --build
    if ($LASTEXITCODE -ne 0) { throw "docker compose up failed" }

    $ready = $false
    foreach ($attempt in 1..30) {
        try {
            $response = Invoke-WebRequest -UseBasicParsing -TimeoutSec 2 http://localhost:8070/healthz
            if ($response.StatusCode -eq 200) { $ready = $true; break }
        } catch {
            Start-Sleep -Seconds 2
        }
    }
    if (-not $ready) { throw "quote healthz is not ready" }

    $rate = Invoke-Grpcurl '{ "base_asset": "USDT", "quote_asset": "RUB" }' "billing.quote.v1.QuoteService/GetRate"
    if ($rate.rate -ne "95.50") { throw "unexpected GetRate: $($rate.rate)" }

    $lockJson = '{ "from_asset": "USDT", "to_asset": "RUB", "amount": { "minor_units": 100000, "asset": "RUB", "decimals": 2 }, "ttl_seconds": 120 }'
    $lock = Invoke-Grpcurl $lockJson "billing.quote.v1.QuoteService/LockQuote"
    if (-not $lock.quoteId) { throw "LockQuote returned no quote id" }
    if ($lock.rate -ne "95.50") { throw "unexpected LockQuote rate: $($lock.rate)" }

    $count = docker compose -f $ComposeFile exec -T postgres psql -U cbl -d quote -t -A -c "SELECT count(*) FROM quotes WHERE id = '$($lock.quoteId)'"
    if ($count.Trim() -ne "1") { throw "quote $($lock.quoteId) is not persisted in PostgreSQL" }

    Write-Host "OK: GetRate=$($rate.rate), LockQuote=$($lock.quoteId) persisted, locked_until=$($lock.lockedUntil)"
} finally {
    docker compose -f $ComposeFile --profile app down -v | Out-Null
    Pop-Location
}
