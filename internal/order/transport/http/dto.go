package http

import (
	"time"

	"github.com/hokagedno/orderservice/internal/order/domain"
)

type createOrderRequest struct {
	CustomerID string           `json:"customer_id" validate:"required,min=1,max=64"`
	Items      []itemRequestDTO `json:"items" validate:"required,min=1,max=50,dive"`
}

type itemRequestDTO struct {
	SKU        string `json:"sku" validate:"required,min=1,max=64"`
	Quantity   int32  `json:"quantity" validate:"required,gt=0,lte=1000"`
	PriceCents int64  `json:"price_cents" validate:"gte=0"`
}

type changeStatusRequest struct {
	Status string `json:"status" validate:"required,oneof=pending confirmed cancelled shipped"`
}

type orderResponse struct {
	ID            string        `json:"id"`
	CustomerID    string        `json:"customer_id"`
	Status        string        `json:"status"`
	Items         []domain.Item `json:"items"`
	SubtotalCents int64         `json:"subtotal_cents"`
	DiscountCents int64         `json:"discount_cents"`
	TotalCents    int64         `json:"total_cents"`
	Version       int           `json:"version"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
}

type listOrdersResponse struct {
	Items []orderResponse `json:"items"`
	Count int             `json:"count"`
}

type errorResponse struct {
	Error   string `json:"error"`
	Details string `json:"details,omitempty"`
}

func toOrderResponse(o domain.Order) orderResponse {
	return orderResponse{
		ID:            o.ID,
		CustomerID:    o.CustomerID,
		Status:        string(o.Status),
		Items:         o.Items,
		SubtotalCents: o.SubtotalCents,
		DiscountCents: o.DiscountCents,
		TotalCents:    o.TotalCents,
		Version:       o.Version,
		CreatedAt:     o.CreatedAt,
		UpdatedAt:     o.UpdatedAt,
	}
}
