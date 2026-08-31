// Package postgres — реализация OrderRepository на pgx/v5.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hokagedno/orderservice/internal/order/domain"
)

type OrderRepository struct {
	pool *pgxpool.Pool
}

func NewOrderRepository(pool *pgxpool.Pool) *OrderRepository {
	return &OrderRepository{pool: pool}
}

// Create сохраняет заказ, его позиции и outbox-событие ОДНОЙ транзакцией.
//
// Это и есть смысл транзакции: три записи в разные таблицы либо применяются
// целиком, либо не применяются вовсе. Без неё возможно состояние
// «заказ есть, позиций нет» или «заказ есть, событие не отправится никогда».
func (r *OrderRepository) Create(ctx context.Context, o *domain.Order, event domain.OutboxEvent) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	// Rollback после успешного Commit — no-op, поэтому defer безопасен
	// и гарантирует откат при любом раннем return или панике.
	defer func() { _ = tx.Rollback(ctx) }()

	const insertOrder = `
		INSERT INTO orders (id, customer_id, status, subtotal_cents, discount_cents,
		                    total_cents, reservation_id, version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 1, $8, $8)`

	if _, err := tx.Exec(ctx, insertOrder,
		o.ID, o.CustomerID, string(o.Status), o.SubtotalCents, o.DiscountCents,
		o.TotalCents, o.ReservationID, o.CreatedAt,
	); err != nil {
		return fmt.Errorf("insert order: %w", err)
	}

	// pgx.CopyFrom — самый быстрый способ вставить много строк: данные уходят
	// бинарным протоколом COPY, без разбора N отдельных INSERT.
	rows := make([][]any, 0, len(o.Items))
	for _, it := range o.Items {
		rows = append(rows, []any{o.ID, it.SKU, it.Quantity, it.PriceCents})
	}
	_, err = tx.CopyFrom(ctx,
		pgx.Identifier{"order_items"},
		[]string{"order_id", "sku", "quantity", "price_cents"},
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		return fmt.Errorf("copy order items: %w", err)
	}

	const insertEvent = `
		INSERT INTO outbox (aggregate_id, type, payload, created_at)
		VALUES ($1, $2, $3, $4)`
	if _, err := tx.Exec(ctx, insertEvent,
		event.AggregateID, event.Type, event.Payload, o.CreatedAt); err != nil {
		return fmt.Errorf("insert outbox: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	o.Version = 1
	return nil
}

// GetByID возвращает заказ вместе с позициями одним запросом с JOIN.
//
// Почему один запрос с JOIN, а не «сначала заказ, потом позиции»:
// два запроса — это два round-trip'а и классическая проблема N+1
// при выборке списка заказов.
func (r *OrderRepository) GetByID(ctx context.Context, id string) (domain.Order, error) {
	const q = `
		SELECT o.id, o.customer_id, o.status, o.subtotal_cents, o.discount_cents,
		       o.total_cents, o.version, o.created_at, o.updated_at,
		       i.sku, i.quantity, i.price_cents
		FROM orders o
		JOIN order_items i ON i.order_id = o.id
		WHERE o.id = $1
		ORDER BY i.sku`

	rows, err := r.pool.Query(ctx, q, id)
	if err != nil {
		return domain.Order{}, fmt.Errorf("select order: %w", err)
	}
	defer rows.Close()

	var (
		o     domain.Order
		found bool
	)
	for rows.Next() {
		var (
			status string
			item   domain.Item
		)
		err := rows.Scan(
			&o.ID, &o.CustomerID, &status, &o.SubtotalCents, &o.DiscountCents,
			&o.TotalCents, &o.Version, &o.CreatedAt, &o.UpdatedAt,
			&item.SKU, &item.Quantity, &item.PriceCents,
		)
		if err != nil {
			return domain.Order{}, fmt.Errorf("scan order row: %w", err)
		}
		o.Status = domain.Status(status)
		o.Items = append(o.Items, item)
		found = true
	}
	if err := rows.Err(); err != nil {
		return domain.Order{}, fmt.Errorf("iterate rows: %w", err)
	}
	if !found {
		return domain.Order{}, domain.ErrNotFound
	}
	return o, nil
}

// ListByCustomer — заказы клиента с позициями, собранными агрегатной функцией.
//
// json_agg на стороне БД избавляет от проблемы N+1 и от «размножения» строк
// заказа по числу позиций: заказ остаётся одной строкой результата,
// а позиции приезжают одним JSON-массивом.
//
// Пагинация делается по заказам (подзапрос с LIMIT/OFFSET), а не по строкам
// джойна — иначе LIMIT обрезал бы позиции в середине заказа.
func (r *OrderRepository) ListByCustomer(ctx context.Context, customerID string, limit, offset int) ([]domain.Order, error) {
	const q = `
		WITH page AS (
			SELECT id FROM orders
			WHERE customer_id = $1
			ORDER BY created_at DESC
			LIMIT $2 OFFSET $3
		)
		SELECT o.id, o.customer_id, o.status, o.subtotal_cents, o.discount_cents,
		       o.total_cents, o.version, o.created_at, o.updated_at,
		       COALESCE(
		           json_agg(json_build_object(
		               'sku', i.sku, 'quantity', i.quantity, 'price_cents', i.price_cents
		           ) ORDER BY i.sku) FILTER (WHERE i.sku IS NOT NULL),
		           '[]'
		       ) AS items
		FROM orders o
		JOIN page p ON p.id = o.id
		LEFT JOIN order_items i ON i.order_id = o.id
		GROUP BY o.id
		ORDER BY o.created_at DESC`

	rows, err := r.pool.Query(ctx, q, customerID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("select orders: %w", err)
	}
	defer rows.Close()

	orders := make([]domain.Order, 0, limit)
	for rows.Next() {
		var (
			o      domain.Order
			status string
			items  []domain.Item
		)
		err := rows.Scan(
			&o.ID, &o.CustomerID, &status, &o.SubtotalCents, &o.DiscountCents,
			&o.TotalCents, &o.Version, &o.CreatedAt, &o.UpdatedAt, &items,
		)
		if err != nil {
			return nil, fmt.Errorf("scan order: %w", err)
		}
		o.Status = domain.Status(status)
		o.Items = items
		orders = append(orders, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate orders: %w", err)
	}
	return orders, nil
}

// UpdateStatus меняет статус заказа под пессимистичной блокировкой строки.
//
// SELECT ... FOR UPDATE блокирует строку до конца транзакции. Без него два
// параллельных запроса могли бы одновременно прочитать статус "pending" и оба
// решить, что переход допустим (состояние гонки типа lost update).
//
// Дополнительно версия увеличивается на каждом изменении — это оптимистическая
// блокировка для клиентов, которые читают заказ и присылают его обратно.
func (r *OrderRepository) UpdateStatus(ctx context.Context, id string, next domain.Status) (domain.Order, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Order{}, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		current string
		version int
	)
	err = tx.QueryRow(ctx,
		`SELECT status, version FROM orders WHERE id = $1 FOR UPDATE`, id).
		Scan(&current, &version)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Order{}, domain.ErrNotFound
		}
		return domain.Order{}, fmt.Errorf("select for update: %w", err)
	}

	// Правило перехода проверяется в домене — репозиторий только применяет его.
	if !domain.Status(current).CanTransitionTo(next) {
		return domain.Order{}, fmt.Errorf("%w: %s -> %s", domain.ErrTransitionForbidden, current, next)
	}

	_, err = tx.Exec(ctx,
		`UPDATE orders SET status = $1, version = version + 1, updated_at = $2 WHERE id = $3`,
		string(next), time.Now().UTC(), id)
	if err != nil {
		return domain.Order{}, fmt.Errorf("update status: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.Order{}, fmt.Errorf("commit: %w", err)
	}
	return r.GetByID(ctx, id)
}

// FetchUnpublished забирает пачку неопубликованных событий outbox.
//
// FOR UPDATE SKIP LOCKED превращает таблицу в очередь: несколько экземпляров
// сервиса могут читать её параллельно и не будут ждать друг друга —
// заблокированные другим воркером строки просто пропускаются.
func (r *OrderRepository) FetchUnpublished(ctx context.Context, limit int) ([]domain.OutboxEvent, error) {
	const q = `
		SELECT id, aggregate_id, type, payload, created_at
		FROM outbox
		WHERE published_at IS NULL
		ORDER BY id
		LIMIT $1
		FOR UPDATE SKIP LOCKED`

	rows, err := r.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("select outbox: %w", err)
	}
	defer rows.Close()

	events := make([]domain.OutboxEvent, 0, limit)
	for rows.Next() {
		var e domain.OutboxEvent
		if err := rows.Scan(&e.ID, &e.AggregateID, &e.Type, &e.Payload, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan outbox: %w", err)
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

func (r *OrderRepository) MarkPublished(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	// ANY($1) с массивом вместо конкатенации списка в строку запроса:
	// один подготовленный запрос на любое число id и никакой SQL-инъекции.
	_, err := r.pool.Exec(ctx,
		`UPDATE outbox SET published_at = now() WHERE id = ANY($1)`, ids)
	if err != nil {
		return fmt.Errorf("mark published: %w", err)
	}
	return nil
}
