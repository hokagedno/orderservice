// Package domain — предметная область заказов: сущности, правила, порты.
package domain

import (
	"context"
	"time"
)

// Status — состояние заказа. Отдельный тип вместо string не даёт
// перепутать статус с любой другой строкой на этапе компиляции.
type Status string

const (
	StatusPending   Status = "pending"
	StatusConfirmed Status = "confirmed"
	StatusCancelled Status = "cancelled"
	StatusShipped   Status = "shipped"
)

// allowedTransitions — конечный автомат жизненного цикла заказа.
// Правила переходов лежат в домене, а не размазаны по хендлерам.
var allowedTransitions = map[Status][]Status{
	StatusPending:   {StatusConfirmed, StatusCancelled},
	StatusConfirmed: {StatusShipped, StatusCancelled},
	StatusShipped:   {},
	StatusCancelled: {},
}

// CanTransitionTo проверяет допустимость перехода.
func (s Status) CanTransitionTo(next Status) bool {
	for _, allowed := range allowedTransitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

func (s Status) Valid() bool {
	_, ok := allowedTransitions[s]
	return ok
}

// Item — позиция заказа.
type Item struct {
	SKU        string `json:"sku"`
	Quantity   int32  `json:"quantity"`
	PriceCents int64  `json:"price_cents"`
}

// Subtotal — стоимость позиции. Деньги хранятся в копейках (int64):
// float64 для денег недопустим из-за ошибок округления двоичной дроби.
func (i Item) Subtotal() int64 { return i.PriceCents * int64(i.Quantity) }

// Order — агрегат заказа.
type Order struct {
	ID            string    `json:"id"`
	CustomerID    string    `json:"customer_id"`
	Status        Status    `json:"status"`
	Items         []Item    `json:"items"`
	SubtotalCents int64     `json:"subtotal_cents"`
	DiscountCents int64     `json:"discount_cents"`
	TotalCents    int64     `json:"total_cents"`
	ReservationID string    `json:"-"`
	Version       int       `json:"version"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Subtotal считает сумму позиций без скидки.
func (o Order) Subtotal() int64 {
	var sum int64
	for _, it := range o.Items {
		sum += it.Subtotal()
	}
	return sum
}

// OutboxEvent — событие для паттерна Transactional Outbox.
//
// Событие пишется в БД в ТОЙ ЖЕ транзакции, что и заказ. Это решает проблему
// двойной записи: без outbox возможна ситуация «заказ сохранён, а сообщение
// в брокер не ушло» (или наоборот). Отдельный воркер потом читает таблицу
// и публикует события — доставка «хотя бы один раз».
type OutboxEvent struct {
	ID          int64
	AggregateID string
	Type        string
	Payload     []byte
	CreatedAt   time.Time
	PublishedAt *time.Time
}

// --- Порты ----------------------------------------------------------------

// OrderRepository — доступ к хранилищу заказов.
type OrderRepository interface {
	Create(ctx context.Context, o *Order, event OutboxEvent) error
	GetByID(ctx context.Context, id string) (Order, error)
	ListByCustomer(ctx context.Context, customerID string, limit, offset int) ([]Order, error)
	// UpdateStatus меняет статус под блокировкой строки (SELECT ... FOR UPDATE).
	UpdateStatus(ctx context.Context, id string, next Status) (Order, error)
	FetchUnpublished(ctx context.Context, limit int) ([]OutboxEvent, error)
	MarkPublished(ctx context.Context, ids []int64) error
}

// InventoryClient — порт складского сервиса. Реализация ходит по gRPC,
// но сервис заказов об этом не знает: в тестах подставляется стаб.
type InventoryClient interface {
	Reserve(ctx context.Context, orderID string, items []Item) (Reservation, error)
	Release(ctx context.Context, reservationID string) error
}

// Reservation — результат резервирования на складе.
// Items содержит позиции с ценами, зафиксированными складом.
type Reservation struct {
	ID         string
	TotalCents int64
	Items      []Item
}
