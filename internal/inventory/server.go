package inventory

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	inventoryv1 "github.com/hokagedno/orderservice/internal/pb/inventory/v1"
	"github.com/hokagedno/orderservice/pkg/id"
)

// Server реализует сгенерированный интерфейс InventoryServiceServer.
//
// Встраивание UnimplementedInventoryServiceServer обязательно для forward
// compatibility: когда в .proto добавят новый метод, сервер продолжит
// компилироваться и вернёт по нему codes.Unimplemented вместо ошибки сборки.
type Server struct {
	inventoryv1.UnimplementedInventoryServiceServer

	store *Store
	log   *slog.Logger
}

func NewServer(store *Store, log *slog.Logger) *Server {
	return &Server{store: store, log: log}
}

func (s *Server) GetStock(ctx context.Context, req *inventoryv1.GetStockRequest) (*inventoryv1.GetStockResponse, error) {
	if req.GetSku() == "" {
		// Коды gRPC — часть контракта. Клиент разбирает их через status.FromError,
		// это аналог HTTP-статусов, но независимый от транспорта.
		return nil, status.Error(codes.InvalidArgument, "sku обязателен")
	}

	item, err := s.store.Get(req.GetSku())
	if err != nil {
		if errors.Is(err, ErrUnknownSKU) {
			return nil, status.Errorf(codes.NotFound, "sku %q не найден", req.GetSku())
		}
		return nil, status.Error(codes.Internal, "внутренняя ошибка")
	}

	return &inventoryv1.GetStockResponse{
		Sku:        item.SKU,
		Available:  item.Available,
		Reserved:   item.Reserved,
		PriceCents: item.PriceCents,
	}, nil
}

func (s *Server) Reserve(ctx context.Context, req *inventoryv1.ReserveRequest) (*inventoryv1.ReserveResponse, error) {
	if len(req.GetItems()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "список позиций пуст")
	}

	// Проверяем контекст до работы: если клиент уже отвалился по дедлайну,
	// нет смысла занимать замок склада.
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}

	items := make(map[string]int32, len(req.GetItems()))
	for _, it := range req.GetItems() {
		items[it.GetSku()] += it.GetQuantity()
	}

	reservationID, err := id.NewUUID()
	if err != nil {
		return nil, status.Error(codes.Internal, "не удалось создать идентификатор резерва")
	}

	res, insufficient, err := s.store.Reserve(reservationID, req.GetOrderId(), items)
	switch {
	case errors.Is(err, ErrInsufficientStock):
		// FailedPrecondition, а не InvalidArgument: запрос корректен,
		// но состояние системы не позволяет его выполнить.
		st := status.New(codes.FailedPrecondition, "недостаточно остатка")
		return &inventoryv1.ReserveResponse{InsufficientSkus: insufficient}, st.Err()
	case errors.Is(err, ErrUnknownSKU):
		return nil, status.Error(codes.NotFound, "неизвестный sku")
	case err != nil:
		return nil, status.Error(codes.Internal, err.Error())
	}

	s.log.InfoContext(ctx, "резерв создан",
		"reservation_id", res.ID, "order_id", res.OrderID, "total_cents", res.Total)

	resp := &inventoryv1.ReserveResponse{
		ReservationId: res.ID,
		TotalCents:    res.Total,
		Items:         make([]*inventoryv1.ReservedItem, 0, len(res.Items)),
	}
	for sku, line := range res.Items {
		resp.Items = append(resp.Items, &inventoryv1.ReservedItem{
			Sku:        sku,
			Quantity:   line.Quantity,
			PriceCents: line.PriceCents,
		})
	}
	return resp, nil
}

func (s *Server) Release(ctx context.Context, req *inventoryv1.ReleaseRequest) (*inventoryv1.ReleaseResponse, error) {
	if err := s.store.Release(req.GetReservationId()); err != nil {
		if errors.Is(err, ErrNoReservation) {
			// Идемпотентность: повторный Release уже снятого резерва
			// не считается ошибкой — иначе ретраи клиента ломали бы систему.
			return &inventoryv1.ReleaseResponse{Released: false}, nil
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &inventoryv1.ReleaseResponse{Released: true}, nil
}

// WatchStock — server-streaming RPC.
//
// Горутина живёт ровно столько, сколько живёт поток: выход происходит по
// ctx.Done() (клиент отключился или сработал дедлайн). Отписка через defer
// гарантирует, что канал подписчика не утечёт.
func (s *Server) WatchStock(req *inventoryv1.WatchStockRequest, stream inventoryv1.InventoryService_WatchStockServer) error {
	filter := make(map[string]struct{}, len(req.GetSkus()))
	for _, sku := range req.GetSkus() {
		filter[sku] = struct{}{}
	}

	updates, unsubscribe := s.store.Subscribe(32)
	defer unsubscribe()

	ctx := stream.Context()
	for {
		select {
		case <-ctx.Done():
			return status.FromContextError(ctx.Err()).Err()
		case u, ok := <-updates:
			if !ok {
				return nil
			}
			if len(filter) > 0 {
				if _, watched := filter[u.SKU]; !watched {
					continue
				}
			}
			err := stream.Send(&inventoryv1.StockUpdate{
				Sku:           u.SKU,
				Available:     u.Available,
				UpdatedAtUnix: u.At.Unix(),
			})
			if err != nil {
				return err
			}
		}
	}
}
