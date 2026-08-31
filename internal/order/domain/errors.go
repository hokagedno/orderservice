package domain

import "errors"

var (
	ErrNotFound             = errors.New("заказ не найден")
	ErrEmptyOrder           = errors.New("заказ не содержит позиций")
	ErrInvalidQuantity      = errors.New("некорректное количество")
	ErrInvalidStatus        = errors.New("некорректный статус")
	ErrTransitionForbidden  = errors.New("недопустимый переход статуса")
	ErrOutOfStock           = errors.New("недостаточно товара на складе")
	ErrUnknownSKU           = errors.New("товар не найден в каталоге")
	ErrInventoryUnavailable = errors.New("складской сервис недоступен")
	ErrConcurrentUpdate     = errors.New("заказ изменён параллельно")
)
