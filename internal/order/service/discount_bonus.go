package service

import "github.com/hokagedno/orderservice/internal/order/domain"

// CheapestItemFreeDiscount — акция «самая дешёвая единица товара в подарок».
//
// Реализует интерфейс DiscountPolicy, поэтому её можно подставить всюду,
// где ожидается стратегия скидки, не меняя ни строчки в сервисе заказов.
type CheapestItemFreeDiscount struct {
	// MinDistinctItems — сколько разных позиций должно быть в заказе,
	// чтобы акция сработала.
	MinDistinctItems int
}

func (d CheapestItemFreeDiscount) Name() string { return "cheapest-free" }

func (d CheapestItemFreeDiscount) Apply(o domain.Order) int64 {
	// Ноль и отрицательное значение трактуем как 1: политика с порогом,
	// который невозможно не выполнить, всё равно осмысленна, а падать
	// или молча отключаться из-за неудачной конфигурации не стоит.
	minDistinct := d.MinDistinctItems
	if minDistinct <= 0 {
		minDistinct = 1
	}

	// Дубликаты SKU схлопываются раньше, в normalizeItems, поэтому
	// длина среза — это и есть число разных позиций.
	// Эта же проверка защищает от паники ниже: при пустом заказе
	// len(o.Items) == 0 всегда меньше minDistinct, и до o.Items[0] не дойдём.
	if len(o.Items) < minDistinct {
		return 0
	}

	// Ищем минимум. Начальное значение берём из первого элемента, а не из
	// нуля: с нулём минимум никогда бы не обновился, ведь цены не бывают
	// отрицательными.
	cheapest := o.Items[0].PriceCents
	for _, it := range o.Items[1:] {
		if it.PriceCents < cheapest {
			cheapest = it.PriceCents
		}
	}

	// Именно PriceCents, а не Subtotal: в подарок идёт одна штука,
	// а не вся позиция целиком.
	return cheapest
}
