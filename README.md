# Order Service

Два взаимодействующих сервиса на Go:

* **orderapi** — REST API заказов на **Echo**, хранилище **PostgreSQL**
  (транзакции, `JOIN`, блокировки строк, Transactional Outbox);
* **inventory** — складской сервис на **gRPC** (unary + server-streaming,
  interceptors, health check, reflection).

Связаны они по gRPC. Всё поднимается одной командой в Docker.

```
              HTTP/JSON                     gRPC (HTTP/2 + protobuf)
   клиент ───────────────> orderapi ───────────────────────────> inventory
                              │
                              └── PostgreSQL (orders, order_items, outbox)
```

## Быстрый старт

```bash
docker compose up --build

# создать заказ
curl -X POST localhost:8081/api/v1/orders \
  -H 'Content-Type: application/json' \
  -d '{
        "customer_id": "cust-1",
        "items": [
          {"sku": "MOUSE-01",   "quantity": 2},
          {"sku": "MONITOR-27", "quantity": 1}
        ]
      }'

# посмотреть заказы клиента
curl 'localhost:8081/api/v1/orders?customer_id=cust-1'

# подтвердить заказ
curl -X PATCH localhost:8081/api/v1/orders/<id>/status \
  -H 'Content-Type: application/json' -d '{"status":"confirmed"}'
```

gRPC-сервис можно потрогать напрямую (включён reflection, `.proto` не нужен):

```bash
grpcurl -plaintext localhost:9090 list
grpcurl -plaintext -d '{"sku":"MOUSE-01"}' localhost:9090 inventory.v1.InventoryService/GetStock
grpcurl -plaintext -d '{"skus":["MOUSE-01"]}' localhost:9090 inventory.v1.InventoryService/WatchStock
```

Локально без Docker:

```bash
make proto        # перегенерировать код из .proto (нужен protoc)
make run-inventory  # терминал 1
make run-api        # терминал 2
make test-race
```

## Структура

```
api/proto/inventory/v1/inventory.proto   контракт gRPC
cmd/orderapi, cmd/inventory              точки входа
internal/
  app/                composition root обоих сервисов, graceful shutdown
  config/             конфигурация из переменных окружения
  grpcutil/           interceptors: логирование, recovery
  inventory/          склад: потокобезопасное хранилище + gRPC-сервер
  inventoryclient/    адаптер gRPC-клиента под доменный порт
  order/
    domain/           сущности, конечный автомат статусов, ПОРТЫ
    service/          бизнес-логика, стратегии скидок, outbox-воркер
    repository/       PostgreSQL: SQL, транзакции, миграции (embed)
    transport/http/   Echo: роутер, хендлеры, middleware, DTO
  pb/                 сгенерированный код protobuf/gRPC
pkg/id/               генерация UUID v4 средствами стандартной библиотеки
```

Зависимости направлены внутрь: `domain` не импортирует ничего, кроме
стандартной библиотеки; `service` работает с интерфейсами `OrderRepository` и
`InventoryClient`. Поэтому вся бизнес-логика тестируется без БД и без сети.

## gRPC

Контракт — `api/proto/inventory/v1/inventory.proto`, четыре метода:

| RPC | Тип | Назначение |
|---|---|---|
| `GetStock` | unary | остаток по SKU |
| `Reserve` | unary | атомарный резерв позиций заказа |
| `Release` | unary | компенсация: вернуть резерв на склад |
| `WatchStock` | server-streaming | поток обновлений остатков |

Что здесь важно и о чём стоит уметь рассказать:

* **Коды ошибок вместо строк.** `NotFound`, `InvalidArgument`,
  `FailedPrecondition` — часть контракта. `FailedPrecondition` для нехватки
  остатка выбран осознанно: запрос корректен, но состояние системы не
  позволяет его выполнить (`InvalidArgument` означал бы ошибку клиента).
* **Адаптер клиента.** `internal/inventoryclient` переводит коды gRPC в
  доменные ошибки, поэтому сервис заказов не знает о существовании gRPC.
* **Дедлайны.** Каждый вызов оборачивается `context.WithTimeout`: без дедлайна
  зависший сервер заблокировал бы горутину HTTP-обработчика навсегда.
* **Retry policy** в service config: до 3 попыток с экспоненциальным
  backoff — только для `UNAVAILABLE`, то есть только для заведомо
  безопасных повторов.
* **Interceptors** (`internal/grpcutil`) — тот же паттерн, что middleware в
  HTTP: логирование и recovery от паники.
* **Идемпотентность.** Повторный `Release` уже снятого резерва возвращает
  `released: false`, а не ошибку, — иначе ретраи ломали бы систему.
* **`UnimplementedInventoryServiceServer`** встроен в сервер: при добавлении
  нового метода в `.proto` код продолжит компилироваться.
* Тесты gRPC используют **bufconn** — соединение в памяти: проходит весь
  настоящий стек (кодирование protobuf, интерсепторы, коды ошибок), но без
  сети и занятых портов.

## PostgreSQL: транзакции, блокировки, JOIN

### Создание заказа — одна транзакция

`orders` + `order_items` + `outbox` пишутся одной транзакцией. Иначе возможно
состояние «заказ есть, позиций нет» или «заказ есть, а событие не уйдёт
никогда». Позиции вставляются через `pgx.CopyFrom` (бинарный протокол COPY) —
быстрее, чем N отдельных `INSERT`.

### Смена статуса — пессимистичная блокировка

```sql
SELECT status, version FROM orders WHERE id = $1 FOR UPDATE;
-- проверка допустимости перехода
UPDATE orders SET status = $1, version = version + 1, updated_at = $2 WHERE id = $3;
```

`FOR UPDATE` держит блокировку строки до конца транзакции. Без неё два
параллельных запроса прочитали бы статус `pending` одновременно, оба сочли бы
переход допустимым и один результат потерялся бы (lost update). Колонка
`version` — оптимистическая блокировка для клиентов, которые читают заказ и
присылают его обратно.

### Чтение заказа — JOIN вместо N+1

```sql
SELECT o.*, i.sku, i.quantity, i.price_cents
FROM orders o
JOIN order_items i ON i.order_id = o.id
WHERE o.id = $1;
```

Один round-trip вместо двух («сначала заказ, потом позиции»).

### Список заказов — агрегация на стороне БД

```sql
WITH page AS (
    SELECT id FROM orders WHERE customer_id = $1
    ORDER BY created_at DESC LIMIT $2 OFFSET $3
)
SELECT o.*, json_agg(...) FILTER (WHERE i.sku IS NOT NULL) AS items
FROM orders o
JOIN page p ON p.id = o.id
LEFT JOIN order_items i ON i.order_id = o.id
GROUP BY o.id;
```

Два неочевидных решения:

1. **Пагинация в CTE, а не по строкам джойна.** `LIMIT` поверх джойна обрезал
   бы позиции в середине заказа.
2. **`json_agg` вместо отдельного запроса за позициями.** Заказ остаётся одной
   строкой результата, позиции приезжают массивом — нет ни проблемы N+1, ни
   «размножения» строк заказа по числу позиций.

### Очередь на таблице

```sql
SELECT ... FROM outbox WHERE published_at IS NULL
ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED;
```

`SKIP LOCKED` превращает таблицу в очередь: несколько реплик сервиса читают её
параллельно и не ждут друг друга — занятые другим воркером строки пропускаются.

### Индексы

| Индекс | Зачем |
|---|---|
| `orders (customer_id, created_at DESC)` | закрывает и фильтр, и сортировку в списке заказов — без отдельного шага `Sort` |
| `orders (created_at) WHERE status='pending'` | частичный: покрывает только «висящие» заказы, поэтому компактный |
| `order_items (order_id)` | внешний ключ сам индекс НЕ создаёт; без него каждый `JOIN` и каскадное удаление — `Seq Scan` |
| `order_items (order_id, sku)` UNIQUE | один SKU не может встречаться в заказе дважды |
| `outbox (id) WHERE published_at IS NULL` | частичный: размер не растёт вместе с историей событий |

Проверить план: `make psql`, затем `EXPLAIN (ANALYZE, BUFFERS) ...`.

## Согласованность между сервисами (сага)

Распределённой транзакции между сервисом заказов и складом не существует.
Поэтому используется **компенсирующая транзакция**:

1. `Reserve` на складе (gRPC);
2. запись заказа в БД одной транзакцией;
3. если шаг 2 упал — вызывается `Release`, резерв возвращается на склад.

Компенсация выполняется с `context.WithoutCancel`: исходный контекст мог быть
уже отменён клиентом, но откатить резерв мы обязаны в любом случае. Этот
сценарий покрыт тестом `TestCreate_CompensatesReservationOnDBFailure`.

**Transactional Outbox** решает вторую половину задачи: событие `order.created`
пишется в БД в той же транзакции, что и заказ, а фоновый воркер публикует его
отдельно. Гарантия — at-least-once, потребитель обязан быть идемпотентным;
exactly-once в распределённой системе недостижим.

## Конкурентность

**Склад.** `sync.RWMutex` (чтений намного больше, чем записей), резерв в два
прохода под одним замком — сначала проверка всех SKU, потом изменение, поэтому
частичный резерв невозможен. Рассылка подписчикам делается уже после снятия
основного замка: удерживать его во время отправки в чужие каналы — прямой путь
к дедлоку. Тест `TestReserve_ConcurrentNoOversell` запускает 150 горутин на
остаток 100 и проверяет, что успешных резервов ровно 100.

**Outbox-воркер.** `time.Ticker` для опроса, канал `jobs` как очередь заданий,
пул горутин-потребителей, `sync.WaitGroup` для ожидания пачки, `sync.Mutex`
вокруг общего слайса результатов, `context` как единая точка остановки.
Отметка «опубликовано» ставится только после успешной публикации.

**Server-streaming.** Горутина стрима живёт ровно столько, сколько живёт
соединение: выход по `ctx.Done()`, отписка через `defer` — подписка не течёт.

## Паттерны и SOLID

| Паттерн | Где |
|---|---|
| Repository | `domain.OrderRepository` + реализация на pgx |
| Adapter | `inventoryclient` — gRPC за доменным интерфейсом |
| Strategy | `service.DiscountPolicy` и её реализации |
| Composite | `BestOfPolicies` — выбирает лучшую из вложенных стратегий |
| Null Object | `NoDiscount` — сервису не нужна проверка на nil |
| State machine | `domain.Status.CanTransitionTo` |
| Transactional Outbox | `outbox` + `OutboxWorker` |
| Saga / компенсация | `OrderService.compensate` |
| Worker Pool | `OutboxWorker.processBatch` |
| Functional Options | `service.WithDiscountPolicy`, `WithClock` |
| Chain of Responsibility | middleware Echo и gRPC-interceptors |

SOLID: единственная ответственность у каждого слоя; новая акция добавляется
реализацией `DiscountPolicy` без правки сервиса (O); стабы подставляются
вместо репозитория и склада (L); интерфейсы узкие (I); зависимости
инвертированы через порты в `domain` (D).

## API

| Метод | Путь | Описание |
|---|---|---|
| `POST` | `/api/v1/orders` | создать заказ |
| `GET` | `/api/v1/orders?customer_id=&limit=&offset=` | заказы клиента |
| `GET` | `/api/v1/orders/:id` | заказ по id |
| `PATCH` | `/api/v1/orders/:id/status` | сменить статус |
| `GET` | `/healthz` | health check |

Статусы: `201`, `200`, `400` (валидация или неизвестный SKU), `404`,
`409` (нет остатка или недопустимый переход статуса), `503` (склад недоступен,
с заголовком `Retry-After`), `500`.

Различие `400` и `409` здесь содержательное: неизвестный SKU — ошибка данных
клиента, а нехватка остатка — конфликт с текущим состоянием системы.

Жизненный цикл заказа:

```
pending ──> confirmed ──> shipped
   │            │
   └──────> cancelled <──┘
```

## Тесты

```bash
make test-race
```

* `internal/inventory` — конкурентный резерв без переторговки, «всё или
  ничего», подписка на обновления, gRPC через bufconn (коды ошибок, streaming).
* `internal/order/domain` — конечный автомат статусов.
* `internal/order/service` — создание заказа, компенсация резерва при сбое БД,
  слияние дублей SKU, запрет недопустимых переходов, стратегии скидок.
* `internal/order/transport/http` — хендлеры Echo через `httptest`: статусы,
  валидация, маппинг доменных ошибок.
* `pkg/id` — формат и уникальность UUID v4, бенчмарк.

## Стек

Go 1.24 · Echo v4 · gRPC + protobuf · pgx/v5 · log/slog · Docker · PostgreSQL 16
