package inventory_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/hokagedno/orderservice/internal/inventory"
)

func newStore() *inventory.Store {
	return inventory.NewStore([]inventory.Item{
		{SKU: "A", Available: 100, PriceCents: 1_000},
		{SKU: "B", Available: 10, PriceCents: 5_000},
	})
}

func TestReserve_ReducesAvailable(t *testing.T) {
	s := newStore()

	res, _, err := s.Reserve("r1", "o1", map[string]int32{"A": 3})
	if err != nil {
		t.Fatalf("резерв: %v", err)
	}
	if res.Total != 3_000 {
		t.Errorf("сумма = %d, ожидали 3000", res.Total)
	}

	item, _ := s.Get("A")
	if item.Available != 97 || item.Reserved != 3 {
		t.Errorf("остаток = %d, резерв = %d; ожидали 97 и 3", item.Available, item.Reserved)
	}
}

// Резерв обязан быть «всё или ничего»: если хотя бы по одному SKU
// не хватает остатка, не должно измениться ничего.
func TestReserve_AllOrNothing(t *testing.T) {
	s := newStore()

	_, insufficient, err := s.Reserve("r1", "o1", map[string]int32{"A": 5, "B": 999})
	if !errors.Is(err, inventory.ErrInsufficientStock) {
		t.Fatalf("ожидали ErrInsufficientStock, получили %v", err)
	}
	if len(insufficient) != 1 || insufficient[0] != "B" {
		t.Errorf("ожидали список [B], получили %v", insufficient)
	}

	// Позиция A не должна была измениться.
	if item, _ := s.Get("A"); item.Available != 100 {
		t.Errorf("частичный резерв: остаток A = %d, ожидали 100", item.Available)
	}
}

// Reserve обязан отклонять неположительное количество: ноль или минус —
// это ошибка вызывающего кода, а не «резерв на ноль штук».
//
// Важно проверить не только факт ошибки, но и то, что состояние склада
// осталось нетронутым: валидация идёт первым проходом, до изменений.
func TestReserve_RejectsNonPositiveQuantity(t *testing.T) {
	cases := map[string]map[string]int32{
		"ноль":          {"A": 0},
		"отрицательное": {"A": -5},
		"одна из позиций плохая": {"A": 1, "B": -1},
	}

	for name, items := range cases {
		t.Run(name, func(t *testing.T) {
			s := newStore()

			_, insufficient, err := s.Reserve("r1", "o1", items)
			if err == nil {
				t.Fatal("ожидали ошибку, получили nil")
			}
			// Это некорректный запрос, а не нехватка остатка,
			// поэтому и ошибка должна быть другой.
			if errors.Is(err, inventory.ErrInsufficientStock) {
				t.Error("ожидали ошибку валидации, получили ErrInsufficientStock")
			}
			if len(insufficient) != 0 {
				t.Errorf("список недостающих SKU должен быть пустым, получили %v", insufficient)
			}

			// Свежий склад с теми же начальными данными — эталон для сравнения.
			pristine := newStore()
			for _, sku := range []string{"A", "B"} {
				want, _ := pristine.Get(sku)
				got, _ := s.Get(sku)
				if got != want {
					t.Errorf("остаток %s изменился: было %+v, стало %+v", sku, want, got)
				}
			}

			// Резерв не должен был появиться: снимать нечего.
			if err := s.Release("r1"); !errors.Is(err, inventory.ErrNoReservation) {
				t.Errorf("резерв не должен был создаться, Release вернул %v", err)
			}
		})
	}
}

func TestRelease_ReturnsStock(t *testing.T) {
	s := newStore()

	res, _, err := s.Reserve("r1", "o1", map[string]int32{"A": 10})
	if err != nil {
		t.Fatalf("резерв: %v", err)
	}
	if err := s.Release(res.ID); err != nil {
		t.Fatalf("снятие резерва: %v", err)
	}

	item, _ := s.Get("A")
	if item.Available != 100 || item.Reserved != 0 {
		t.Errorf("после снятия: остаток = %d, резерв = %d", item.Available, item.Reserved)
	}
	if err := s.Release(res.ID); !errors.Is(err, inventory.ErrNoReservation) {
		t.Errorf("повторное снятие должно вернуть ErrNoReservation, получили %v", err)
	}
}

// Главный тест конкурентности: 100 горутин одновременно резервируют по 1 шт.
// при остатке 100. Ровно 100 резервов должны пройти, и остаток обязан стать 0.
// Запускать с -race.
func TestReserve_ConcurrentNoOversell(t *testing.T) {
	s := inventory.NewStore([]inventory.Item{{SKU: "A", Available: 100, PriceCents: 100}})

	const goroutines = 150
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		success int
	)
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			_, _, err := s.Reserve(fmt.Sprintf("r%d", i), "o", map[string]int32{"A": 1})
			if err == nil {
				mu.Lock()
				success++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	if success != 100 {
		t.Errorf("успешных резервов = %d, ожидали ровно 100", success)
	}
	item, _ := s.Get("A")
	if item.Available != 0 || item.Reserved != 100 {
		t.Errorf("остаток = %d, резерв = %d; ожидали 0 и 100", item.Available, item.Reserved)
	}
}

func TestSubscribe_ReceivesUpdates(t *testing.T) {
	s := newStore()

	updates, unsubscribe := s.Subscribe(4)
	defer unsubscribe()

	if _, _, err := s.Reserve("r1", "o1", map[string]int32{"A": 1}); err != nil {
		t.Fatalf("резерв: %v", err)
	}

	select {
	case u := <-updates:
		if u.SKU != "A" || u.Available != 99 {
			t.Errorf("получили обновление %+v", u)
		}
	default:
		t.Error("подписчик не получил обновление")
	}
}
