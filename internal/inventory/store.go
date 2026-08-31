// Package inventory — складской сервис, доступный по gRPC.
//
// Хранилище здесь в памяти: цель проекта — показать gRPC, конкурентность и
// корректную синхронизацию, а не второй раз тот же слой работы с Postgres
// (он подробно разобран в сервисе заказов).
package inventory

import (
	"errors"
	"sync"
	"time"
)

var (
	ErrUnknownSKU        = errors.New("неизвестный SKU")
	ErrInsufficientStock = errors.New("недостаточно остатка")
	ErrNoReservation     = errors.New("резерв не найден")
)

// Item — позиция на складе.
type Item struct {
	SKU        string
	Available  int32
	Reserved   int32
	PriceCents int64
}

// ReservedLine — позиция резерва с зафиксированной ценой.
type ReservedLine struct {
	Quantity   int32
	PriceCents int64
}

// Reservation — набор зарезервированных позиций одного заказа.
// Цена фиксируется в момент резерва: последующее изменение прайса
// не должно менять стоимость уже оформленного заказа.
type Reservation struct {
	ID        string
	OrderID   string
	Items     map[string]ReservedLine
	Total     int64
	CreatedAt time.Time
}

// Store — потокобезопасное хранилище остатков.
//
// Используется sync.RWMutex, а не Mutex: чтений (GetStock) на порядок больше,
// чем записей, а RLock допускает произвольное число одновременных читателей.
//
// Альтернатива «канал вместо мьютекса» здесь была бы медленнее: защищается
// обычная разделяемая структура, а не передача владения данными.
type Store struct {
	mu           sync.RWMutex
	items        map[string]*Item
	reservations map[string]*Reservation

	// subscribers — подписчики server-streaming'а WatchStock.
	subsMu sync.Mutex
	subs   map[int64]chan Update
	nextID int64
}

// Update — событие изменения остатка, уходящее в стрим.
type Update struct {
	SKU       string
	Available int32
	At        time.Time
}

func NewStore(seed []Item) *Store {
	s := &Store{
		items:        make(map[string]*Item, len(seed)),
		reservations: make(map[string]*Reservation),
		subs:         make(map[int64]chan Update),
	}
	for i := range seed {
		item := seed[i]
		s.items[item.SKU] = &item
	}
	return s
}

// Get возвращает копию позиции: наружу отдаётся значение, а не указатель
// на внутреннюю структуру, иначе вызывающий код смог бы изменить состояние
// хранилища в обход мьютекса.
func (s *Store) Get(sku string) (Item, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	it, ok := s.items[sku]
	if !ok {
		return Item{}, ErrUnknownSKU
	}
	return *it, nil
}

// Reserve атомарно резервирует все позиции заказа.
//
// Транзакционность «всё или ничего» обеспечивается двумя проходами под одним
// эксклюзивным замком: сначала проверяем достаточность остатков по всем SKU,
// и только потом изменяем состояние. Так частичный резерв невозможен.
//
// Возвращаемый список SKU — те, по которым остатка не хватило.
func (s *Store) Reserve(reservationID, orderID string, items map[string]int32) (*Reservation, []string, error) {
	s.mu.Lock()

	// Проход 1: валидация.
	var insufficient []string
	for sku, qty := range items {
		if qty <= 0 {
			s.mu.Unlock()
			return nil, nil, errors.New("количество должно быть положительным")
		}
		it, ok := s.items[sku]
		if !ok {
			s.mu.Unlock()
			return nil, nil, ErrUnknownSKU
		}
		if it.Available < qty {
			insufficient = append(insufficient, sku)
		}
	}
	if len(insufficient) > 0 {
		s.mu.Unlock()
		return nil, insufficient, ErrInsufficientStock
	}

	// Проход 2: применение.
	res := &Reservation{
		ID:        reservationID,
		OrderID:   orderID,
		Items:     make(map[string]ReservedLine, len(items)),
		CreatedAt: time.Now().UTC(),
	}
	updates := make([]Update, 0, len(items))
	for sku, qty := range items {
		it := s.items[sku]
		it.Available -= qty
		it.Reserved += qty
		res.Items[sku] = ReservedLine{Quantity: qty, PriceCents: it.PriceCents}
		res.Total += it.PriceCents * int64(qty)
		updates = append(updates, Update{SKU: sku, Available: it.Available, At: res.CreatedAt})
	}
	s.reservations[res.ID] = res
	s.mu.Unlock()

	// Рассылка подписчикам делается ПОСЛЕ снятия основного замка:
	// удерживать s.mu во время отправки в чужие каналы — прямой путь
	// к взаимной блокировке.
	s.broadcast(updates)
	return res, nil, nil
}

// Release — компенсирующая операция: возвращает резерв на склад.
func (s *Store) Release(reservationID string) error {
	s.mu.Lock()
	res, ok := s.reservations[reservationID]
	if !ok {
		s.mu.Unlock()
		return ErrNoReservation
	}
	now := time.Now().UTC()
	updates := make([]Update, 0, len(res.Items))
	for sku, line := range res.Items {
		if it, exists := s.items[sku]; exists {
			it.Available += line.Quantity
			it.Reserved -= line.Quantity
			updates = append(updates, Update{SKU: sku, Available: it.Available, At: now})
		}
	}
	delete(s.reservations, reservationID)
	s.mu.Unlock()

	s.broadcast(updates)
	return nil
}

// Subscribe регистрирует подписчика на обновления остатков.
// Возвращает канал и функцию отписки — вызывающий обязан вызвать её
// через defer, иначе подписка утечёт вместе с горутиной.
func (s *Store) Subscribe(buffer int) (<-chan Update, func()) {
	if buffer <= 0 {
		buffer = 16
	}
	ch := make(chan Update, buffer)

	s.subsMu.Lock()
	s.nextID++
	subID := s.nextID
	s.subs[subID] = ch
	s.subsMu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			s.subsMu.Lock()
			delete(s.subs, subID)
			s.subsMu.Unlock()
			close(ch)
		})
	}
	return ch, unsubscribe
}

// broadcast рассылает события неблокирующе: медленный подписчик пропустит
// обновление, но не затормозит склад целиком.
func (s *Store) broadcast(updates []Update) {
	s.subsMu.Lock()
	defer s.subsMu.Unlock()

	for _, u := range updates {
		for _, ch := range s.subs {
			select {
			case ch <- u:
			default: // подписчик не успевает — пропускаем событие
			}
		}
	}
}
