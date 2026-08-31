package service

import (
	"sort"

	"github.com/hokagedno/orderservice/internal/order/domain"
)

// DiscountPolicy — паттерн «Стратегия».
//
// Правило расчёта скидки вынесено за интерфейс, поэтому добавление новой
// акции не требует правки сервиса: достаточно зарегистрировать ещё одну
// реализацию. Это принцип открытости/закрытости (O из SOLID) на практике.
type DiscountPolicy interface {
	// Name используется в логах и в ответе API.
	Name() string
	// Apply возвращает размер скидки в копейках для данного заказа.
	Apply(o domain.Order) int64
}

// NoDiscount — политика по умолчанию (паттерн Null Object):
// сервису не нужно проверять policy на nil.
type NoDiscount struct{}

func (NoDiscount) Name() string             { return "none" }
func (NoDiscount) Apply(domain.Order) int64 { return 0 }

// ThresholdDiscount — процент от суммы при превышении порога.
type ThresholdDiscount struct {
	ThresholdCents int64
	Percent        int64 // 10 = 10%
}

func (d ThresholdDiscount) Name() string { return "threshold" }

func (d ThresholdDiscount) Apply(o domain.Order) int64 {
	subtotal := o.Subtotal()
	if subtotal < d.ThresholdCents {
		return 0
	}
	// Целочисленная арифметика: деньги во float64 приводят к ошибкам округления.
	return subtotal * d.Percent / 100
}

// BulkItemDiscount — скидка за количество одинаковых позиций.
type BulkItemDiscount struct {
	MinQuantity int32
	Percent     int64
}

func (d BulkItemDiscount) Name() string { return "bulk" }

func (d BulkItemDiscount) Apply(o domain.Order) int64 {
	var discount int64
	for _, it := range o.Items {
		if it.Quantity >= d.MinQuantity {
			discount += it.Subtotal() * d.Percent / 100
		}
	}
	return discount
}

// BestOfPolicies — композиция стратегий (паттерн «Компоновщик»):
// применяет все политики и выбирает самую выгодную для клиента.
// Скидки не суммируются — иначе они могли бы превысить стоимость заказа.
type BestOfPolicies struct {
	Policies []DiscountPolicy
}

func (b BestOfPolicies) Name() string { return "best-of" }

func (b BestOfPolicies) Apply(o domain.Order) int64 {
	var best int64
	for _, p := range b.Policies {
		if d := p.Apply(o); d > best {
			best = d
		}
	}
	// Скидка не может превышать сумму заказа.
	if subtotal := o.Subtotal(); best > subtotal {
		best = subtotal
	}
	return best
}

// DefaultPolicy — набор акций «по умолчанию».
func DefaultPolicy() DiscountPolicy {
	return BestOfPolicies{
		Policies: []DiscountPolicy{
			NoDiscount{},
			ThresholdDiscount{ThresholdCents: 500_000, Percent: 10}, // от 5000 ₽ — 10%
			BulkItemDiscount{MinQuantity: 10, Percent: 15},          // от 10 шт. — 15%
			CheapestItemFreeDiscount{MinDistinctItems: 3},            // от 3 позиций — дешёвая в подарок
		},
	}
}

// normalizeItems схлопывает дубликаты SKU и сортирует позиции.
//
// Детерминированный порядок важен: он убирает взаимные блокировки при
// параллельных транзакциях, которые берут блокировки на одни и те же строки
// в разном порядке (классическая причина deadlock в БД).
func normalizeItems(items []domain.Item) []domain.Item {
	merged := make(map[string]domain.Item, len(items))
	for _, it := range items {
		if existing, ok := merged[it.SKU]; ok {
			existing.Quantity += it.Quantity
			merged[it.SKU] = existing
			continue
		}
		merged[it.SKU] = it
	}

	result := make([]domain.Item, 0, len(merged))
	for _, it := range merged {
		result = append(result, it)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].SKU < result[j].SKU })
	return result
}

// applyReservedPrices проставляет позициям заказа цены, зафиксированные
// складом. Позиции, которых склад не вернул (старая версия сервиса,
// не знающая о поле items), сохраняют исходную цену.
func applyReservedPrices(items, reserved []domain.Item) []domain.Item {
	if len(reserved) == 0 {
		return items
	}
	prices := make(map[string]int64, len(reserved))
	for _, r := range reserved {
		prices[r.SKU] = r.PriceCents
	}
	for i := range items {
		if price, ok := prices[items[i].SKU]; ok {
			items[i].PriceCents = price
		}
	}
	return items
}
