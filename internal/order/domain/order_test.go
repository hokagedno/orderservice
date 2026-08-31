package domain_test

import (
	"testing"

	"github.com/hokagedno/orderservice/internal/order/domain"
)

func TestStatusTransitions(t *testing.T) {
	cases := []struct {
		from, to domain.Status
		allowed  bool
	}{
		{domain.StatusPending, domain.StatusConfirmed, true},
		{domain.StatusPending, domain.StatusCancelled, true},
		{domain.StatusPending, domain.StatusShipped, false}, // нельзя отгрузить неподтверждённый
		{domain.StatusConfirmed, domain.StatusShipped, true},
		{domain.StatusShipped, domain.StatusCancelled, false}, // отгруженный не отменяют
		{domain.StatusCancelled, domain.StatusConfirmed, false},
	}

	for _, tc := range cases {
		t.Run(string(tc.from)+"->"+string(tc.to), func(t *testing.T) {
			if got := tc.from.CanTransitionTo(tc.to); got != tc.allowed {
				t.Errorf("CanTransitionTo = %v, ожидали %v", got, tc.allowed)
			}
		})
	}
}

func TestOrderSubtotal(t *testing.T) {
	o := domain.Order{Items: []domain.Item{
		{SKU: "A", Quantity: 2, PriceCents: 1000},
		{SKU: "B", Quantity: 3, PriceCents: 500},
	}}
	if got := o.Subtotal(); got != 3500 {
		t.Errorf("Subtotal = %d, ожидали 3500", got)
	}
}

func TestStatusValid(t *testing.T) {
	if domain.Status("unknown").Valid() {
		t.Error("неизвестный статус не должен считаться валидным")
	}
	if !domain.StatusPending.Valid() {
		t.Error("pending должен быть валидным")
	}
}
