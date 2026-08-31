CREATE TABLE IF NOT EXISTS orders (
    id             UUID PRIMARY KEY,
    customer_id    TEXT        NOT NULL,
    status         TEXT        NOT NULL,
    subtotal_cents BIGINT      NOT NULL CHECK (subtotal_cents >= 0),
    discount_cents BIGINT      NOT NULL DEFAULT 0 CHECK (discount_cents >= 0),
    total_cents    BIGINT      NOT NULL CHECK (total_cents >= 0),
    reservation_id TEXT,
    version        INT         NOT NULL DEFAULT 1,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Ограничение на уровне БД: список статусов не должен зависеть
    -- от того, что приложение не забыло провалидировать.
    CONSTRAINT orders_status_chk
        CHECK (status IN ('pending', 'confirmed', 'cancelled', 'shipped'))
);

-- Основной запрос списка: WHERE customer_id = $1 ORDER BY created_at DESC.
-- Составной индекс закрывает и фильтрацию, и сортировку: планировщик
-- получает готовый порядок и не выполняет отдельный шаг Sort.
CREATE INDEX IF NOT EXISTS orders_customer_created_idx
    ON orders (customer_id, created_at DESC);

-- Частичный индекс под фоновую обработку «висящих» заказов:
-- он покрывает только pending-строки и потому в разы компактнее полного.
CREATE INDEX IF NOT EXISTS orders_pending_idx
    ON orders (created_at) WHERE status = 'pending';

CREATE TABLE IF NOT EXISTS order_items (
    id          BIGSERIAL PRIMARY KEY,
    order_id    UUID   NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    sku         TEXT   NOT NULL,
    quantity    INT    NOT NULL CHECK (quantity > 0),
    price_cents BIGINT NOT NULL CHECK (price_cents >= 0)
);

-- Внешний ключ сам по себе индекс НЕ создаёт. Без него каждый JOIN
-- по order_id и каждое каскадное удаление приводили бы к Seq Scan.
CREATE INDEX IF NOT EXISTS order_items_order_id_idx ON order_items (order_id);

-- Один и тот же SKU не должен встречаться в заказе дважды —
-- количество увеличивается в существующей позиции.
CREATE UNIQUE INDEX IF NOT EXISTS order_items_order_sku_uidx
    ON order_items (order_id, sku);

-- Transactional Outbox: событие пишется в одной транзакции с заказом.
CREATE TABLE IF NOT EXISTS outbox (
    id           BIGSERIAL PRIMARY KEY,
    aggregate_id UUID        NOT NULL,
    type         TEXT        NOT NULL,
    payload      JSONB       NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ
);

-- Очередь читается запросом WHERE published_at IS NULL ORDER BY id.
-- Частичный индекс содержит только неопубликованные события, поэтому
-- его размер не растёт вместе с историей.
CREATE INDEX IF NOT EXISTS outbox_unpublished_idx
    ON outbox (id) WHERE published_at IS NULL;
