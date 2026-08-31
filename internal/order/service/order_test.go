package service_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/hokagedno/orderservice/internal/order/domain"
	"github.com/hokagedno/orderservice/internal/order/service"
)

// --- стабы ----------------------------------------------------------------

type stubRepo struct {
	mu        sync.Mutex
	orders    map[string]domain.Order
	events    []domain.OutboxEvent
	createErr error
}

func newStubRepo() *stubRepo {
	return &stubRepo{orders: make(map[string]domain.Order)}
}

func (r *stubRepo) Create(_ context.Context, o *domain.Order, e domain.OutboxEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.createErr != nil {
		return r.createErr
	}
	r.orders[o.ID] = *o
	r.events = append(r.events, e)
	return nil
}

func (r *stubRepo) GetByID(_ context.Context, id string) (domain.Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.orders[id]
	if !ok {
		return domain.Order{}, domain.ErrNotFound
	}
	return o, nil
}

func (r *stubRepo) ListByCustomer(context.Context, string, int, int) ([]domain.Order, error) {
	return nil, nil
}

func (r *stubRepo) UpdateStatus(_ context.Context, id string, next domain.Status) (domain.Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.orders[id]
	if !ok {
		return domain.Order{}, domain.ErrNotFound
	}
	if !o.Status.CanTransitionTo(next) {
		return domain.Order{}, domain.ErrTransitionForbidden
	}
	o.Status = next
	o.Version++
	r.orders[id] = o
	return o, nil
}

func (r *stubRepo) FetchUnpublished(context.Context, int) ([]domain.OutboxEvent, error) {
	return nil, nil
}
func (r *stubRepo) MarkPublished(context.Context, []int64) error { return nil }

type stubInventory struct {
	mu          sync.Mutex
	reserveErr  error
	total       int64
	released    []string
	reserveCall int
}

func (s *stubInventory) Reserve(_ context.Context, orderID string, items []domain.Item) (domain.Reservation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reserveCall++
	if s.reserveErr != nil {
		return domain.Reservation{}, s.reserveErr
	}
	return domain.Reservation{ID: "res-" + orderID, TotalCents: s.total}, nil
}

func (s *stubInventory) Release(_ context.Context, reservationID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.released = append(s.released, reservationID)
	return nil
}

func (s *stubInventory) releasedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.released)
}

func newSUT() (*service.OrderService, *stubRepo, *stubInventory) {
	repo, inv := newStubRepo(), &stubInventory{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return service.New(repo, inv, log), repo, inv
}

// --- тесты ----------------------------------------------------------------

func TestCreate_Success(t *testing.T) {
	svc, repo, inv := newSUT()

	order, err := svc.Create(context.Background(), service.CreateInput{
		CustomerID: "cust-1",
		Items: []domain.Item{
			{SKU: "MOUSE-01", Quantity: 2, PriceCents: 19900},
		},
	})
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if order.Status != domain.StatusPending {
		t.Errorf("статус = %s, ожидали pending", order.Status)
	}
	if order.SubtotalCents != 39800 {
		t.Errorf("subtotal = %d, ожидали 39800", order.SubtotalCents)
	}
	if inv.reserveCall != 1 {
		t.Errorf("склад должен быть вызван один раз, вызван %d", inv.reserveCall)
	}
	// Событие outbox обязано быть записано вместе с заказом.
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.events) != 1 || repo.events[0].Type != "order.created" {
		t.Errorf("ожидали одно событие order.created, получили %+v", repo.events)
	}
}

func TestCreate_EmptyOrder(t *testing.T) {
	svc, _, _ := newSUT()
	if _, err := svc.Create(context.Background(), service.CreateInput{CustomerID: "c"}); !errors.Is(err, domain.ErrEmptyOrder) {
		t.Errorf("ожидали ErrEmptyOrder, получили %v", err)
	}
}

// Ключевой тест саги: если запись в БД упала, резерв на складе обязан
// откатиться компенсирующим вызовом Release.
func TestCreate_CompensatesReservationOnDBFailure(t *testing.T) {
	svc, repo, inv := newSUT()
	repo.createErr = errors.New("база недоступна")

	_, err := svc.Create(context.Background(), service.CreateInput{
		CustomerID: "cust-1",
		Items:      []domain.Item{{SKU: "MOUSE-01", Quantity: 1, PriceCents: 100}},
	})
	if err == nil {
		t.Fatal("ожидали ошибку создания заказа")
	}
	if inv.releasedCount() != 1 {
		t.Errorf("резерв не был снят: released=%d", inv.releasedCount())
	}
}

func TestCreate_OutOfStock(t *testing.T) {
	svc, _, inv := newSUT()
	inv.reserveErr = domain.ErrOutOfStock

	_, err := svc.Create(context.Background(), service.CreateInput{
		CustomerID: "cust-1",
		Items:      []domain.Item{{SKU: "MONITOR-27", Quantity: 999, PriceCents: 100}},
	})
	if !errors.Is(err, domain.ErrOutOfStock) {
		t.Errorf("ожидали ErrOutOfStock, получили %v", err)
	}
	// Резерв не создавался — компенсировать нечего.
	if inv.releasedCount() != 0 {
		t.Errorf("Release не должен вызываться, вызван %d раз", inv.releasedCount())
	}
}

// Позиции с одинаковым SKU должны схлопываться в одну.
func TestCreate_MergesDuplicateSKUs(t *testing.T) {
	svc, _, _ := newSUT()

	order, err := svc.Create(context.Background(), service.CreateInput{
		CustomerID: "cust-1",
		Items: []domain.Item{
			{SKU: "CABLE-HDMI", Quantity: 2, PriceCents: 4900},
			{SKU: "CABLE-HDMI", Quantity: 3, PriceCents: 4900},
		},
	})
	if err != nil {
		t.Fatalf("ошибка: %v", err)
	}
	if len(order.Items) != 1 {
		t.Fatalf("ожидали 1 позицию после слияния, получили %d", len(order.Items))
	}
	if order.Items[0].Quantity != 5 {
		t.Errorf("количество = %d, ожидали 5", order.Items[0].Quantity)
	}
}

func TestChangeStatus_ForbiddenTransition(t *testing.T) {
	svc, _, _ := newSUT()
	ctx := context.Background()

	order, err := svc.Create(ctx, service.CreateInput{
		CustomerID: "cust-1",
		Items:      []domain.Item{{SKU: "MOUSE-01", Quantity: 1, PriceCents: 100}},
	})
	if err != nil {
		t.Fatalf("создание: %v", err)
	}

	if _, err := svc.ChangeStatus(ctx, order.ID, domain.StatusShipped); !errors.Is(err, domain.ErrTransitionForbidden) {
		t.Errorf("pending -> shipped должен быть запрещён, получили %v", err)
	}
}

func TestChangeStatus_CancelReleasesReservation(t *testing.T) {
	svc, _, inv := newSUT()
	ctx := context.Background()

	order, _ := svc.Create(ctx, service.CreateInput{
		CustomerID: "cust-1",
		Items:      []domain.Item{{SKU: "MOUSE-01", Quantity: 1, PriceCents: 100}},
	})

	if _, err := svc.ChangeStatus(ctx, order.ID, domain.StatusCancelled); err != nil {
		t.Fatalf("отмена: %v", err)
	}
	if inv.releasedCount() != 1 {
		t.Errorf("при отмене резерв должен быть снят, released=%d", inv.releasedCount())
	}
}
