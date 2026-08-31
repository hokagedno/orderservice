// Package service — бизнес-логика заказов.
package service

import (
	"context"
	"encoding/json"

	"fmt"
	"log/slog"
	"time"

	"github.com/hokagedno/orderservice/internal/order/domain"
	"github.com/hokagedno/orderservice/pkg/id"
)

// OrderService оркеструет создание заказа: резерв на складе (gRPC),
// расчёт скидки (стратегия), сохранение в БД (транзакция + outbox).
type OrderService struct {
	repo      domain.OrderRepository
	inventory domain.InventoryClient
	discount  DiscountPolicy
	log       *slog.Logger
	now       func() time.Time
}

type Option func(*OrderService)

func WithDiscountPolicy(p DiscountPolicy) Option {
	return func(s *OrderService) { s.discount = p }
}

func WithClock(now func() time.Time) Option {
	return func(s *OrderService) { s.now = now }
}

func New(
	repo domain.OrderRepository,
	inventory domain.InventoryClient,
	log *slog.Logger,
	opts ...Option,
) *OrderService {
	s := &OrderService{
		repo:      repo,
		inventory: inventory,
		discount:  DefaultPolicy(),
		log:       log,
		now:       time.Now,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

type CreateInput struct {
	CustomerID string
	Items      []domain.Item
}

// Create создаёт заказ.
//
// Порядок шагов и обработка ошибок здесь — самая содержательная часть сервиса:
//
//  1. валидация;
//  2. резерв на складе по gRPC (внешний вызов, может упасть);
//  3. расчёт скидки локальной стратегией;
//  4. запись заказа + позиций + outbox-события ОДНОЙ транзакцией.
//
// Если шаг 4 упал, шаг 2 нужно откатить — база и склад иначе разъедутся.
// Это компенсирующая транзакция (saga): распределённой транзакции между
// двумя сервисами не существует, поэтому согласованность достигается
// явной компенсацией.
func (s *OrderService) Create(ctx context.Context, in CreateInput) (domain.Order, error) {
	if len(in.Items) == 0 {
		return domain.Order{}, domain.ErrEmptyOrder
	}
	for _, it := range in.Items {
		if it.Quantity <= 0 {
			return domain.Order{}, fmt.Errorf("%w: sku %s", domain.ErrInvalidQuantity, it.SKU)
		}
	}

	orderID, err := id.NewUUID()
	if err != nil {
		return domain.Order{}, fmt.Errorf("генерация id: %w", err)
	}

	items := normalizeItems(in.Items)

	reservation, err := s.inventory.Reserve(ctx, orderID, items)
	if err != nil {
		return domain.Order{}, err
	}

	// Цены берём из ответа склада, а не из запроса: иначе клиент мог бы
	// оформить заказ по любой цене, какую пришлёт.
	items = applyReservedPrices(items, reservation.Items)

	order := domain.Order{
		ID:            orderID,
		CustomerID:    in.CustomerID,
		Status:        domain.StatusPending,
		Items:         items,
		ReservationID: reservation.ID,
		CreatedAt:     s.now().UTC(),
	}
	order.UpdatedAt = order.CreatedAt

	order.SubtotalCents = order.Subtotal()
	if reservation.TotalCents > 0 {
		// Итог, посчитанный складом, — источник истины.
		order.SubtotalCents = reservation.TotalCents
	}
	order.DiscountCents = s.discount.Apply(order)
	order.TotalCents = order.SubtotalCents - order.DiscountCents

	event, err := newOrderCreatedEvent(order)
	if err != nil {
		s.compensate(ctx, reservation.ID)
		return domain.Order{}, err
	}

	if err := s.repo.Create(ctx, &order, event); err != nil {
		s.compensate(ctx, reservation.ID)
		return domain.Order{}, err
	}

	s.log.InfoContext(ctx, "заказ создан",
		"order_id", order.ID, "total_cents", order.TotalCents,
		"discount", s.discount.Name(), "items", len(order.Items))
	return order, nil
}

// compensate возвращает резерв на склад. Ошибку компенсации только логируем:
// заказ уже не будет создан, а «повисший» резерв снимет фоновая чистка
// по времени жизни резерва.
func (s *OrderService) compensate(ctx context.Context, reservationID string) {
	// Отдельный контекст: исходный может быть уже отменён клиентом,
	// но компенсацию выполнить обязаны.
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	if err := s.inventory.Release(cctx, reservationID); err != nil {
		s.log.ErrorContext(ctx, "не удалось снять резерв",
			"reservation_id", reservationID, "err", err)
	}
}

func (s *OrderService) Get(ctx context.Context, orderID string) (domain.Order, error) {
	return s.repo.GetByID(ctx, orderID)
}

func (s *OrderService) ListByCustomer(ctx context.Context, customerID string, limit, offset int) ([]domain.Order, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	return s.repo.ListByCustomer(ctx, customerID, limit, offset)
}

// ChangeStatus переводит заказ в новый статус.
// Проверка допустимости перехода и блокировка строки — внутри репозитория,
// в одной транзакции; сервис отвечает только за побочные эффекты.
func (s *OrderService) ChangeStatus(ctx context.Context, orderID string, next domain.Status) (domain.Order, error) {
	if !next.Valid() {
		return domain.Order{}, fmt.Errorf("%w: %s", domain.ErrInvalidStatus, next)
	}

	order, err := s.repo.UpdateStatus(ctx, orderID, next)
	if err != nil {
		return domain.Order{}, err
	}

	// Отмена заказа освобождает резерв на складе.
	if next == domain.StatusCancelled && order.ReservationID != "" {
		s.compensate(ctx, order.ReservationID)
	}
	return order, nil
}

// orderCreatedPayload — тело доменного события.
type orderCreatedPayload struct {
	OrderID    string        `json:"order_id"`
	CustomerID string        `json:"customer_id"`
	TotalCents int64         `json:"total_cents"`
	Items      []domain.Item `json:"items"`
	CreatedAt  time.Time     `json:"created_at"`
}

func newOrderCreatedEvent(o domain.Order) (domain.OutboxEvent, error) {
	payload, err := json.Marshal(orderCreatedPayload{
		OrderID:    o.ID,
		CustomerID: o.CustomerID,
		TotalCents: o.TotalCents,
		Items:      o.Items,
		CreatedAt:  o.CreatedAt,
	})
	if err != nil {
		return domain.OutboxEvent{}, fmt.Errorf("сериализация события: %w", err)
	}
	return domain.OutboxEvent{
		AggregateID: o.ID,
		Type:        "order.created",
		Payload:     payload,
		CreatedAt:   o.CreatedAt,
	}, nil
}
