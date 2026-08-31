// Package inventoryclient — адаптер gRPC-клиента склада под доменный порт
// domain.InventoryClient.
//
// Смысл адаптера: сервис заказов работает с доменными типами и доменными
// ошибками и ничего не знает ни о protobuf, ни о кодах gRPC. Замена gRPC на
// HTTP или на локальный вызов затронет только этот файл (паттерн «Адаптер»).
package inventoryclient

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/hokagedno/orderservice/internal/order/domain"
	inventoryv1 "github.com/hokagedno/orderservice/internal/pb/inventory/v1"
)

type Client struct {
	conn    *grpc.ClientConn
	api     inventoryv1.InventoryServiceClient
	timeout time.Duration
}

// Dial создаёт соединение со складским сервисом.
//
// grpc.NewClient не блокирует: соединение устанавливается лениво, при первом
// вызове. Балансировка round_robin вместе с DNS-резолвером означает, что при
// нескольких репликах склада (например, за Kubernetes-сервисом) запросы
// распределятся между ними, а не прилипнут к одной.
func Dial(target string, timeout time.Duration) (*Client, error) {
	conn, err := grpc.NewClient(target,
		// insecure — только для локальной разработки;
		// в проде здесь были бы TLS-креды.
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultServiceConfig(`{
			"loadBalancingConfig": [{"round_robin":{}}],
			"methodConfig": [{
				"name": [{"service": "inventory.v1.InventoryService"}],
				"retryPolicy": {
					"MaxAttempts": 3,
					"InitialBackoff": "0.1s",
					"MaxBackoff": "1s",
					"BackoffMultiplier": 2.0,
					"RetryableStatusCodes": ["UNAVAILABLE"]
				}
			}]
		}`),
	)
	if err != nil {
		return nil, fmt.Errorf("подключение к складу: %w", err)
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	return &Client{
		conn:    conn,
		api:     inventoryv1.NewInventoryServiceClient(conn),
		timeout: timeout,
	}, nil
}

func (c *Client) Close() error { return c.conn.Close() }

// Reserve резервирует позиции и переводит коды gRPC в доменные ошибки.
func (c *Client) Reserve(ctx context.Context, orderID string, items []domain.Item) (domain.Reservation, error) {
	// Дедлайн обязателен: без него зависший сервер заблокирует
	// горутину HTTP-обработчика на неопределённое время.
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req := &inventoryv1.ReserveRequest{OrderId: orderID}
	for _, it := range items {
		req.Items = append(req.Items, &inventoryv1.ReserveItem{
			Sku:      it.SKU,
			Quantity: it.Quantity,
		})
	}

	resp, err := c.api.Reserve(ctx, req)
	if err != nil {
		return domain.Reservation{}, translateError(err)
	}
	reserved := make([]domain.Item, 0, len(resp.GetItems()))
	for _, it := range resp.GetItems() {
		reserved = append(reserved, domain.Item{
			SKU:        it.GetSku(),
			Quantity:   it.GetQuantity(),
			PriceCents: it.GetPriceCents(),
		})
	}
	return domain.Reservation{
		ID:         resp.GetReservationId(),
		TotalCents: resp.GetTotalCents(),
		Items:      reserved,
	}, nil
}

func (c *Client) Release(ctx context.Context, reservationID string) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	_, err := c.api.Release(ctx, &inventoryv1.ReleaseRequest{ReservationId: reservationID})
	if err != nil {
		return translateError(err)
	}
	return nil
}

// translateError превращает gRPC-код в доменную ошибку.
// Так HTTP-слой сервиса заказов сможет отдать корректный статус,
// не зная ничего о gRPC.
func translateError(err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return fmt.Errorf("%w: %v", domain.ErrInventoryUnavailable, err)
	}
	switch st.Code() {
	case codes.FailedPrecondition:
		return fmt.Errorf("%w: %s", domain.ErrOutOfStock, st.Message())
	case codes.NotFound:
		// Склад не знает такого SKU — это ошибка данных клиента,
		// а не нехватка остатка: статусы наружу должны отличаться.
		return fmt.Errorf("%w: %s", domain.ErrUnknownSKU, st.Message())
	case codes.InvalidArgument:
		return fmt.Errorf("%w: %s", domain.ErrInvalidQuantity, st.Message())
	case codes.Unavailable, codes.DeadlineExceeded:
		return fmt.Errorf("%w: %s", domain.ErrInventoryUnavailable, st.Message())
	default:
		return fmt.Errorf("склад вернул ошибку %s: %s", st.Code(), st.Message())
	}
}
