package inventory_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/hokagedno/orderservice/internal/inventory"
	inventoryv1 "github.com/hokagedno/orderservice/internal/pb/inventory/v1"
)

// bufconn поднимает gRPC поверх соединения в памяти: тест проходит через
// настоящий стек (кодирование protobuf, интерсепторы, коды ошибок),
// но без сети и без занятых портов — быстро и без флаки-тестов.
func startTestServer(t *testing.T, store *inventory.Store) inventoryv1.InventoryServiceClient {
	t.Helper()

	lis := bufconn.Listen(1024 * 1024)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	srv := grpc.NewServer()
	inventoryv1.RegisterInventoryServiceServer(srv, inventory.NewServer(store, log))

	go func() {
		if err := srv.Serve(lis); err != nil {
			return
		}
	}()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("подключение к bufconn: %v", err)
	}

	// t.Cleanup гарантирует освобождение ресурсов даже при падении теста.
	t.Cleanup(func() {
		_ = conn.Close()
		srv.Stop()
		_ = lis.Close()
	})
	return inventoryv1.NewInventoryServiceClient(conn)
}

func TestGRPC_GetStock(t *testing.T) {
	client := startTestServer(t, newStore())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	resp, err := client.GetStock(ctx, &inventoryv1.GetStockRequest{Sku: "A"})
	if err != nil {
		t.Fatalf("GetStock: %v", err)
	}
	if resp.GetAvailable() != 100 || resp.GetPriceCents() != 1_000 {
		t.Errorf("получили %+v", resp)
	}
}

func TestGRPC_GetStock_NotFound(t *testing.T) {
	client := startTestServer(t, newStore())

	_, err := client.GetStock(context.Background(), &inventoryv1.GetStockRequest{Sku: "НЕТ"})
	if status.Code(err) != codes.NotFound {
		t.Errorf("ожидали код NotFound, получили %s", status.Code(err))
	}
}

func TestGRPC_GetStock_InvalidArgument(t *testing.T) {
	client := startTestServer(t, newStore())

	_, err := client.GetStock(context.Background(), &inventoryv1.GetStockRequest{Sku: ""})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("ожидали код InvalidArgument, получили %s", status.Code(err))
	}
}

func TestGRPC_ReserveAndRelease(t *testing.T) {
	client := startTestServer(t, newStore())
	ctx := context.Background()

	res, err := client.Reserve(ctx, &inventoryv1.ReserveRequest{
		OrderId: "o1",
		Items:   []*inventoryv1.ReserveItem{{Sku: "A", Quantity: 2}},
	})
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if res.GetTotalCents() != 2_000 {
		t.Errorf("сумма = %d, ожидали 2000", res.GetTotalCents())
	}

	rel, err := client.Release(ctx, &inventoryv1.ReleaseRequest{ReservationId: res.GetReservationId()})
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if !rel.GetReleased() {
		t.Error("резерв должен был сняться")
	}
}

func TestGRPC_Reserve_OutOfStock(t *testing.T) {
	client := startTestServer(t, newStore())

	_, err := client.Reserve(context.Background(), &inventoryv1.ReserveRequest{
		OrderId: "o1",
		Items:   []*inventoryv1.ReserveItem{{Sku: "B", Quantity: 999}},
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("ожидали FailedPrecondition, получили %s (%v)", status.Code(err), err)
	}
}

// Проверяем server-streaming: после резерва подписчик должен получить
// обновление остатка, а закрытие контекста — завершить поток.
func TestGRPC_WatchStock(t *testing.T) {
	store := newStore()
	client := startTestServer(t, store)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := client.WatchStock(ctx, &inventoryv1.WatchStockRequest{Skus: []string{"A"}})
	if err != nil {
		t.Fatalf("WatchStock: %v", err)
	}

	// Небольшая пауза, чтобы сервер успел оформить подписку до события.
	time.Sleep(100 * time.Millisecond)
	if _, _, err := store.Reserve("r1", "o1", map[string]int32{"A": 5}); err != nil {
		t.Fatalf("резерв: %v", err)
	}

	update, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if update.GetSku() != "A" || update.GetAvailable() != 95 {
		t.Errorf("получили %+v", update)
	}
}
