# E2E CBL-6.2/6.3: LockQuote/GetRate на compose-стенде quote.
# Запуск из каталога сервиса: powershell -NoProfile -File test/e2e/lock-quote.ps1
# Сценарий: compose up -> healthz -> GetRate -> LockQuote -> строка и аудит-поля
# в PostgreSQL -> compose down.
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
    if ($lock.quoteId -notmatch '^[0-9a-fA-F-]{36}$') { throw "unexpected quote id format: $($lock.quoteId)" }

    $count = docker compose -f $ComposeFile exec -T postgres psql -U cbl -d quote -t -A -c "SELECT count(*) FROM quotes WHERE id = '$($lock.quoteId)'"
    if ($count.Trim() -ne "1") { throw "quote $($lock.quoteId) is not persisted in PostgreSQL" }

    # Аудит-поля CBL-6.3: as_of, amount_minor, sources, guard_adjusted, spread.
    $audit = docker compose -f $ComposeFile exec -T postgres psql -U cbl -d quote -t -A -c "SELECT (as_of IS NOT NULL)::text || ',' || (amount_minor IS NOT NULL)::text || ',' || (jsonb_array_length(coalesce(sources, '[]'::jsonb)) > 0)::text || ',' || guard_adjusted::text || ',' || (spread IS NOT NULL)::text FROM quotes WHERE id = '$($lock.quoteId)'"
    if ($audit.Trim() -ne "true,true,true,false,true") { throw "quote audit fields are incomplete: '$($audit.Trim())'" }

    Write-Host "OK: GetRate=$($rate.rate), LockQuote=$($lock.quoteId) persisted with audit fields, locked_until=$($lock.lockedUntil)"
} finally {
    docker compose -f $ComposeFile --profile app down -v | Out-Null
    Pop-Location
}
