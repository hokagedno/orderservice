package service_test

import (
	"testing"

	"github.com/hokagedno/orderservice/internal/order/domain"
	"github.com/hokagedno/orderservice/internal/order/service"
)

// --- ЗАДАНИЕ 2: тесты для CheapestItemFreeDiscount ------------------------
//
// Тесты падают, пока Apply не реализован. Менять их не нужно —
// они описывают требуемое поведение.

func TestCheapestItemFreeDiscount(t *testing.T) {
	policy := service.CheapestItemFreeDiscount{MinDistinctItems: 3}

	cases := []struct {
		name  string
		items []domain.Item
		want  int64
	}{
		{
			name:  "пустой заказ",
			items: nil,
			want:  0,
		},
		{
			name: "позиций меньше порога",
			items: []domain.Item{
				{SKU: "A", Quantity: 1, PriceCents: 10_000},
				{SKU: "B", Quantity: 1, PriceCents: 20_000},
			},
			want: 0,
		},
		{
			name: "ровно порог — дарим самую дешёвую единицу",
			items: []domain.Item{
				{SKU: "A", Quantity: 1, PriceCents: 30_000},
				{SKU: "B", Quantity: 1, PriceCents: 10_000},
				{SKU: "C", Quantity: 1, PriceCents: 20_000},
			},
			want: 10_000,
		},
		{
			name: "дарим одну единицу, а не всю позицию",
			items: []domain.Item{
				{SKU: "A", Quantity: 5, PriceCents: 1_000}, // Subtotal = 5000
				{SKU: "B", Quantity: 1, PriceCents: 20_000},
				{SKU: "C", Quantity: 1, PriceCents: 30_000},
			},
			want: 1_000,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := domain.Order{Items: tc.items}
			if got := policy.Apply(o); got != tc.want {
				t.Errorf("Apply = %d, ожидали %d", got, tc.want)
			}
		})
	}
}

func TestCheapestItemFreeDiscount_ZeroThresholdMeansOne(t *testing.T) {
	policy := service.CheapestItemFreeDiscount{MinDistinctItems: 0}

	o := domain.Order{Items: []domain.Item{{SKU: "A", Quantity: 1, PriceCents: 7_000}}}
	if got := policy.Apply(o); got != 7_000 {
		t.Errorf("Apply = %d, ожидали 7000: порог 0 должен трактоваться как 1", got)
	}
}

// ЗАДАНИЕ 2 (шаг «б»). Добавь новую акцию в DefaultPolicy в discount.go
// с порогом в 3 разные позиции.
//
// Заказ ниже подобран так, что остальные акции не срабатывают:
// сумма 60 000 меньше порога ThresholdDiscount (500 000), а количества
// по одной штуке не дотягивают до BulkItemDiscount (10 штук).
// Значит, скидку может дать только новая политика.
func TestDefaultPolicy_IncludesCheapestItemFree(t *testing.T) {
	o := domain.Order{Items: []domain.Item{
		{SKU: "A", Quantity: 1, PriceCents: 10_000},
		{SKU: "B", Quantity: 1, PriceCents: 20_000},
		{SKU: "C", Quantity: 1, PriceCents: 30_000},
	}}

	if got := service.DefaultPolicy().Apply(o); got != 10_000 {
		t.Errorf("DefaultPolicy().Apply = %d, ожидали 10000", got)
	}
}
