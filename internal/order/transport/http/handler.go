// Package http — REST-транспорт сервиса заказов на Echo.
//
// Echo выбран, чтобы показать второй популярный фреймворк (в проекте
// url-shortener используется Gin). Принцип тот же: транспорт только
// разбирает запрос и переводит доменные ошибки в HTTP-статусы.
package http

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"

	"github.com/hokagedno/orderservice/internal/order/domain"
	"github.com/hokagedno/orderservice/internal/order/service"
)

// OrderService — порт, нужный хендлерам (объявлен у потребителя).
type OrderService interface {
	Create(ctx context.Context, in service.CreateInput) (domain.Order, error)
	Get(ctx context.Context, orderID string) (domain.Order, error)
	ListByCustomer(ctx context.Context, customerID string, limit, offset int) ([]domain.Order, error)
	ChangeStatus(ctx context.Context, orderID string, next domain.Status) (domain.Order, error)
}

type Handler struct {
	svc OrderService
	log *slog.Logger
}

func NewHandler(svc OrderService, log *slog.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

func (h *Handler) createOrder(c echo.Context) error {
	var req createOrderRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errorResponse{Error: "некорректное тело запроса"})
	}
	if err := c.Validate(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errorResponse{Error: "ошибка валидации", Details: err.Error()})
	}

	items := make([]domain.Item, 0, len(req.Items))
	for _, it := range req.Items {
		items = append(items, domain.Item{
			SKU:        it.SKU,
			Quantity:   it.Quantity,
			PriceCents: it.PriceCents,
		})
	}

	// c.Request().Context() отменяется, когда клиент разрывает соединение, —
	// прокидывать его вглубь обязательно, иначе сервис продолжит работу
	// над результатом, который уже некому получить.
	order, err := h.svc.Create(c.Request().Context(), service.CreateInput{
		CustomerID: req.CustomerID,
		Items:      items,
	})
	if err != nil {
		return h.fail(c, err)
	}
	return c.JSON(http.StatusCreated, toOrderResponse(order))
}

func (h *Handler) getOrder(c echo.Context) error {
	order, err := h.svc.Get(c.Request().Context(), c.Param("id"))
	if err != nil {
		return h.fail(c, err)
	}
	return c.JSON(http.StatusOK, toOrderResponse(order))
}

func (h *Handler) listOrders(c echo.Context) error {
	customerID := c.QueryParam("customer_id")
	if customerID == "" {
		return c.JSON(http.StatusBadRequest, errorResponse{Error: "customer_id обязателен"})
	}
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	offset, _ := strconv.Atoi(c.QueryParam("offset"))

	orders, err := h.svc.ListByCustomer(c.Request().Context(), customerID, limit, offset)
	if err != nil {
		return h.fail(c, err)
	}

	items := make([]orderResponse, 0, len(orders))
	for _, o := range orders {
		items = append(items, toOrderResponse(o))
	}
	return c.JSON(http.StatusOK, listOrdersResponse{Items: items, Count: len(items)})
}

func (h *Handler) changeStatus(c echo.Context) error {
	var req changeStatusRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errorResponse{Error: "некорректное тело запроса"})
	}

	order, err := h.svc.ChangeStatus(c.Request().Context(), c.Param("id"), domain.Status(req.Status))
	if err != nil {
		return h.fail(c, err)
	}
	return c.JSON(http.StatusOK, toOrderResponse(order))
}

func (h *Handler) health(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

// fail — единая таблица соответствия доменных ошибок и HTTP-статусов.
func (h *Handler) fail(c echo.Context, err error) error {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return c.JSON(http.StatusNotFound, errorResponse{Error: "заказ не найден"})
	case errors.Is(err, domain.ErrEmptyOrder), errors.Is(err, domain.ErrInvalidQuantity),
		errors.Is(err, domain.ErrInvalidStatus), errors.Is(err, domain.ErrUnknownSKU):
		return c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrTransitionForbidden):
		return c.JSON(http.StatusConflict, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrOutOfStock):
		// 409: запрос корректен, но состояние склада не позволяет его выполнить.
		return c.JSON(http.StatusConflict, errorResponse{Error: "недостаточно товара на складе"})
	case errors.Is(err, domain.ErrInventoryUnavailable):
		// 503 + Retry-After: клиенту имеет смысл повторить запрос позже.
		c.Response().Header().Set("Retry-After", "5")
		return c.JSON(http.StatusServiceUnavailable, errorResponse{Error: "складской сервис недоступен"})
	default:
		h.log.ErrorContext(c.Request().Context(), "внутренняя ошибка", "err", err, "path", c.Path())
		return c.JSON(http.StatusInternalServerError, errorResponse{Error: "внутренняя ошибка сервера"})
	}
}
