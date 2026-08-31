package service_test

import (
	"testing"

	"github.com/hokagedno/orderservice/internal/order/domain"
	"github.com/hokagedno/orderservice/internal/order/service"
)

func TestThresholdDiscount(t *testing.T) {
	policy := service.ThresholdDiscount{ThresholdCents: 100_000, Percent: 10}

	cases := []struct {
		name string
		o    domain.Order
		want int64
	}{
		{
			name: "ниже порога — скидки нет",
			o:    domain.Order{Items: []domain.Item{{SKU: "A", Quantity: 1, PriceCents: 50_000}}},
			want: 0,
		},
		{
			name: "ровно порог — скидка есть",
			o:    domain.Order{Items: []domain.Item{{SKU: "A", Quantity: 1, PriceCents: 100_000}}},
			want: 10_000,
		},
		{
			name: "выше порога",
			o:    domain.Order{Items: []domain.Item{{SKU: "A", Quantity: 3, PriceCents: 100_000}}},
			want: 30_000,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := policy.Apply(tc.o); got != tc.want {
				t.Errorf("Apply = %d, ожидали %d", got, tc.want)
			}
		})
	}
}

func TestBulkItemDiscount(t *testing.T) {
	policy := service.BulkItemDiscount{MinQuantity: 10, Percent: 15}

	o := domain.Order{Items: []domain.Item{
		{SKU: "A", Quantity: 10, PriceCents: 1_000}, // 10 000 -> 1 500
		{SKU: "B", Quantity: 5, PriceCents: 1_000},  // не дотягивает
	}}
	if got := policy.Apply(o); got != 1_500 {
		t.Errorf("Apply = %d, ожидали 1500", got)
	}
}

// BestOfPolicies выбирает самую выгодную скидку, а не сумму всех.
func TestBestOfPolicies(t *testing.T) {
	policy := service.BestOfPolicies{Policies: []service.DiscountPolicy{
		service.NoDiscount{},
		service.ThresholdDiscount{ThresholdCents: 10_000, Percent: 10},
		service.BulkItemDiscount{MinQuantity: 10, Percent: 15},
	}}

	o := domain.Order{Items: []domain.Item{{SKU: "A", Quantity: 10, PriceCents: 1_000}}}
	// threshold: 10% от 10 000 = 1 000; bulk: 15% от 10 000 = 1 500 -> берём 1 500
	if got := policy.Apply(o); got != 1_500 {
		t.Errorf("Apply = %d, ожидали 1500 (лучшая из политик)", got)
	}
}

func TestBestOfPolicies_NeverExceedsSubtotal(t *testing.T) {
	policy := service.BestOfPolicies{Policies: []service.DiscountPolicy{
		service.ThresholdDiscount{ThresholdCents: 0, Percent: 500}, // заведомо абсурдные 500%
	}}
	o := domain.Order{Items: []domain.Item{{SKU: "A", Quantity: 1, PriceCents: 1_000}}}

	if got := policy.Apply(o); got != 1_000 {
		t.Errorf("скидка = %d, не должна превышать сумму заказа 1000", got)
	}
}
