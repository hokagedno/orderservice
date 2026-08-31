package http_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hokagedno/orderservice/internal/order/domain"
	"github.com/hokagedno/orderservice/internal/order/service"
	orderhttp "github.com/hokagedno/orderservice/internal/order/transport/http"
)

// fakeService реализует порт orderhttp.OrderService.
type fakeService struct {
	createErr error
	getErr    error
	order     domain.Order
}

func (f *fakeService) Create(context.Context, service.CreateInput) (domain.Order, error) {
	if f.createErr != nil {
		return domain.Order{}, f.createErr
	}
	return f.order, nil
}
func (f *fakeService) Get(context.Context, string) (domain.Order, error) {
	if f.getErr != nil {
		return domain.Order{}, f.getErr
	}
	return f.order, nil
}
func (f *fakeService) ListByCustomer(context.Context, string, int, int) ([]domain.Order, error) {
	return []domain.Order{f.order}, nil
}
func (f *fakeService) ChangeStatus(context.Context, string, domain.Status) (domain.Order, error) {
	return f.order, nil
}

func newRouter(svc *fakeService) http.Handler {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return orderhttp.NewRouter(orderhttp.NewHandler(svc, log), log, orderhttp.RouterConfig{})
}

func sampleOrder() domain.Order {
	return domain.Order{
		ID:            "11111111-2222-4333-8444-555555555555",
		CustomerID:    "cust-1",
		Status:        domain.StatusPending,
		Items:         []domain.Item{{SKU: "MOUSE-01", Quantity: 1, PriceCents: 19900}},
		SubtotalCents: 19900,
		TotalCents:    19900,
		Version:       1,
	}
}

func do(h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestCreateOrder_Created(t *testing.T) {
	h := newRouter(&fakeService{order: sampleOrder()})

	w := do(h, http.MethodPost, "/api/v1/orders",
		`{"customer_id":"cust-1","items":[{"sku":"MOUSE-01","quantity":1,"price_cents":19900}]}`)

	if w.Code != http.StatusCreated {
		t.Fatalf("ожидали 201, получили %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateOrder_ValidationError(t *testing.T) {
	h := newRouter(&fakeService{order: sampleOrder()})

	// Пустой список позиций не проходит валидацию тега `min=1`.
	w := do(h, http.MethodPost, "/api/v1/orders", `{"customer_id":"cust-1","items":[]}`)

	if w.Code != http.StatusBadRequest {
		t.Errorf("ожидали 400, получили %d: %s", w.Code, w.Body.String())
	}
}

// Доменные ошибки должны превращаться в осмысленные HTTP-статусы.
func TestErrorMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"нет на складе", domain.ErrOutOfStock, http.StatusConflict},
		{"склад недоступен", domain.ErrInventoryUnavailable, http.StatusServiceUnavailable},
		{"пустой заказ", domain.ErrEmptyOrder, http.StatusBadRequest},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newRouter(&fakeService{createErr: tc.err, order: sampleOrder()})
			w := do(h, http.MethodPost, "/api/v1/orders",
				`{"customer_id":"c","items":[{"sku":"A","quantity":1,"price_cents":1}]}`)

			if w.Code != tc.want {
				t.Errorf("ожидали %d, получили %d (%s)", tc.want, w.Code, w.Body.String())
			}
		})
	}
}

func TestGetOrder_NotFound(t *testing.T) {
	h := newRouter(&fakeService{getErr: domain.ErrNotFound})

	w := do(h, http.MethodGet, "/api/v1/orders/does-not-exist", "")

	if w.Code != http.StatusNotFound {
		t.Errorf("ожидали 404, получили %d", w.Code)
	}
}

func TestListOrders_RequiresCustomerID(t *testing.T) {
	h := newRouter(&fakeService{order: sampleOrder()})

	if w := do(h, http.MethodGet, "/api/v1/orders", ""); w.Code != http.StatusBadRequest {
		t.Errorf("без customer_id ожидали 400, получили %d", w.Code)
	}
	if w := do(h, http.MethodGet, "/api/v1/orders?customer_id=cust-1", ""); w.Code != http.StatusOK {
		t.Errorf("с customer_id ожидали 200, получили %d", w.Code)
	}
}

func TestHealthz(t *testing.T) {
	h := newRouter(&fakeService{})
	if w := do(h, http.MethodGet, "/healthz", ""); w.Code != http.StatusOK {
		t.Errorf("ожидали 200, получили %d", w.Code)
	}
}
