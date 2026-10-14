# AGENTS.md - cbl-quote

Курсы и зафиксированные котировки для счетов.

Обязательные глобальные правила: `../../AGENTS.md`, `../../.specs/CBL-001-development-process.md`,
`../../.specs/CBL-002-repos-and-docker.md`, контракты `../../.specs/CBL-003-contracts.md`.
Длинное тире (U+2014) запрещено, используется только дефис.

## 1. Границы

In scope: LockQuote и GetRate, кеш курсов, аудит зафиксированных котировок.

Out of scope: другие сервисы платформы; взаимодействие только через контракты `cbl-protos`
(gRPC), Kafka и REST периметра (см. `CBL-000`, раздел 2).

## 2. Контракты

- REST: административный /healthz, /readyz
- gRPC: сервер QuoteService
- Kafka продюсирует: нет
- Kafka потребляет: нет
- Хранилище: PostgreSQL quote, Redis (кеш курсов)

Контракты заморожены в `cbl-protos` (CF-1 для gRPC, CF-2/CF-3 для событий), см. `CBL-004`.

## 3. Архитектура

Слои: `internal/transport` -> `internal/usecase` -> `internal/domain` -> `internal/storage`.
Библиотеки: стандартный набор `CBL-002` (Gin, GORM + pgx-драйвер, franz-go + pkg/sr, go-redis,
goose, zap). `AutoMigrate` запрещен, миграции только goose, только вперед.

## 4. Команды

```powershell
make build        # go build ./...
make test         # go test ./...
make test-integration  # integration (testcontainers: PostgreSQL, Redis)
make e2e          # compose-стенд + grpcurl: GetRate, LockQuote, проверка строки в quotes
make vet          # go vet ./...
make lint         # golangci-lint в Docker
make up           # docker compose -f deploy/docker-compose.yml up -d
make down         # docker compose -f deploy/docker-compose.yml down -v
```

## 5. Порты и окружение

HTTP `8070`, gRPC `9070`, метрики `9270`.
Переменные (применимые к сервису): `HTTP_ADDR`, `GRPC_ADDR`, `METRICS_ADDR`, `PG_DSN`,
`MIGRATIONS_DIR`, `REDIS_URL` (redis:// или rediss://, приоритет) либо `REDIS_ADDR` +
`REDIS_PASSWORD`, `RATE_SOURCE`, `RATE_CONFIG`, `RATE_HTTP_URL`, `RATE_HTTP_NAME`,
`RATE_MAX_SPREAD_PCT` (default 2), `RATE_ANOMALY_PCT` (default 10),
`RATE_ANOMALY_RESET` (default 15m), `RATE_ALLOWED_IDENTICAL_PAIRS`,
`RATE_CACHE_TTL` (default 5s), `RATE_LAST_TTL` (default 1h), `LOCK_QUOTE_TTL` (default 5m),
`LOCK_QUOTE_MAX_TTL` (default 15m).

## 6. Правила разработки

- Конвейер: SDD-спека в `.specs/` -> архитектура -> контракты -> тесты вперед -> код ->
  ревью 4 агентами (без контекста, архитектура, контекст, безопасность - блокирующий) ->
  AGENTS.md -> финальные прогоны -> линтеры -> коммит.
- Покрытие unit+integration не ниже 80%; денежные сценарии только на реальных PostgreSQL и
  Kafka (testcontainers); локальный e2e в docker-compose обязателен.
- Деньги только int64 в минимальных единицах плюс decimals; float запрещен.
- Docstring на русском в стиле godoc (первая строка с имени сущности, точка в конце);
  комментарии объясняют почему; ошибки через `%w`; панки в бизнес-коде запрещены.
- Коммит: `CBL-<эпик>.<номер>: <краткое сообщение>`, спека с тем же префиксом в том же коммите.

## 7. SLO и метрики

SLO уточняется в задаче 14.4. Базовые метрики: latency p95/p99, доля ошибок, outbox lag,
consumer lag; дашборды после задачи 0.7.

## 8. Trade-offs

| ID | Решение | Плюсы | Минусы и компенсация |
| :-- | :-- | :-- | :-- |
| T-QUOTE-01 | Состояние AnomalyGuard хранится в памяти инстанса | нет внешних зависимостей, минимум задержки | при нескольких репликах состояние не общее и сбрасывается при рестарте | окно ресинхронизации 15 минут ограничивает риск; общий кеш в 14.x |
| T-QUOTE-02 | Redis - кеш курсов (свежий TTL 5s и last-known 1h), не источник истины | 6.2: снята нагрузка с источников, деградация GetRate при их недоступности | значения кеша недоверенные (Redis может отравить ключ) | проверка формата и возраста при чтении; LockQuote фиксирует только свежий курс источников; ошибки Redis деградируют к источникам, WARN троттлится 30 с |
| T-QUOTE-03 | Пары с совпадающими активами запрещены, кроме whitelist | защита от некорректных запросов | нужен whitelist для USDT/USD | env `RATE_ALLOWED_IDENTICAL_PAIRS` |
| T-QUOTE-04 | `LockQuote` не идемпотентен на уровне RPC (в контракте CF-1 нет ключа) | простой контракт | повторный вызов создает новую котировку | эффекта на деньги нет; идемпотентность платежа обеспечивает Billing API/Payment (4.x) |
| T-QUOTE-05 | usecase импортирует storage (`storage.Quote` в сигнатуре QuoteStorage) | нет лишнего маппинга слоев, прецедент ledger T-LED-01 | границы слоев размыты | интерфейсы RateCache/QuoteStorage объявлены на стороне потребителя; пересмотр - при росте числа хранилищ |

## 9. Known issues

| ID | Проблема | Влияние | План | Статус |
| :-- | :-- | :-- | :-- | :-- |
| KI-quote-01 | CI и pre-commit не настроены | линтеры запускаются вручную | задача 0.2 | open |
| KI-quote-02 | Нет кеша курсов и LockQuote | нагрузка на источники, фиксация курса не готова | ЗАКРЫТ 14.10.2026: кеш Redis и LockQuote реализованы в 6.2 | closed |
| KI-quote-03 | Race detector недоступен локально (нет CGO) | конкурентный тест выполняется без -race | включить -race в CI (0.2) | open |
| KI-quote-04 | Каталоги e2e и load пусты | сквозная проверка котировки отсутствовала | ЗАКРЫТ 14.10.2026: e2e LockQuote/GetRate на compose в 6.2; k6 в 14.1 | closed |
| KI-quote-05 | Нет mTLS на gRPC QuoteService | порт 9070 не публиковать за пределы доверенной сети; prod-выкладка запрещена | задача 0.6 | open |
| KI-quote-06 | Redis в dev без TLS (пароль есть); в prod нет ACL и приватной сети | теоретический вектор отравления кеша закрыт валидацией; запись ключей из чужой сети | rediss:// уже поддержан (`REDIS_URL`); ACL, приватная сеть и TLS в prod - 0.6 | open |

## 10. Журнал задач

| CBL | Дата | Статус | Спека | Коммит |
| :-- | :-- | :-- | :-- | :-- |
| CBL-6.1 | 2026-10-14 | done, ревью закрыто (config+http источники, мьютекс guard, RPC GetRate, покрытие 90.5%) | `.specs/CBL-6.1-rate-source.md` | ec98004 |
| CBL-6.2 | 2026-10-14 | done, ревью закрыто (LockQuote по свежим данным, кеш Redis с проверкой недоверенных значений, e2e-скрипт, покрытие 91.2%) | `.specs/CBL-6.2-lock-quote-cache.md` | этот коммит |

## 11. Runbook

Заполняется первой продуктовой задачей сервиса. Типовые аварии: недоступность зависимости,
рост ошибок, лаг обработки; действия: проверить health/metrics, логи с request_id, состояние
Kafka consumer group и outbox lag, при необходимости перезапустить сервис.
