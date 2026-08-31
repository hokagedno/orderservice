package app

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	"github.com/hokagedno/orderservice/internal/config"
	"github.com/hokagedno/orderservice/internal/grpcutil"
	"github.com/hokagedno/orderservice/internal/inventory"
	inventoryv1 "github.com/hokagedno/orderservice/internal/pb/inventory/v1"
)

// RunInventory поднимает gRPC-сервис склада.
func RunInventory() error {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.LoadInventory()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store := inventory.NewStore(seedCatalog())

	// Перехватчики выстраиваются в цепочку в порядке объявления:
	// Recovery снаружи, чтобы поймать панику в том числе из логгера.
	server := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			grpcutil.UnaryRecovery(log),
			grpcutil.UnaryLogger(log),
		),
		grpc.ChainStreamInterceptor(grpcutil.StreamLogger(log)),
		grpc.ConnectionTimeout(5*time.Second),
	)

	inventoryv1.RegisterInventoryServiceServer(server, inventory.NewServer(store, log))

	// Стандартный health-check gRPC — его понимают Kubernetes и grpc_health_probe.
	healthSrv := health.NewServer()
	healthpb.RegisterHealthServer(server, healthSrv)
	healthSrv.SetServingStatus("inventory.v1.InventoryService", healthpb.HealthCheckResponse_SERVING)

	// Reflection позволяет вызывать методы через grpcurl без .proto-файла —
	// удобно в разработке. В проде обычно отключают.
	reflection.Register(server)

	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.GRPCAddr, err)
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("grpc-сервер склада запущен", "addr", cfg.GRPCAddr)
		if err := server.Serve(lis); err != nil {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("grpc-сервер: %w", err)
	case <-ctx.Done():
		log.Info("останавливаем склад")
	}

	// GracefulStop перестаёт принимать новые вызовы и ждёт завершения текущих,
	// включая открытые стримы. Таймер страхует от «вечного» стрима.
	done := make(chan struct{})
	go func() {
		server.GracefulStop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(cfg.ShutdownTimeout):
		log.Warn("graceful stop не уложился в таймаут, останавливаем принудительно")
		server.Stop()
	}
	return nil
}

// seedCatalog — стартовый каталог. В реальном сервисе это была бы БД.
func seedCatalog() []inventory.Item {
	return []inventory.Item{
		{SKU: "KEYBOARD-01", Available: 120, PriceCents: 499_00},
		{SKU: "MOUSE-01", Available: 300, PriceCents: 199_00},
		{SKU: "MONITOR-27", Available: 40, PriceCents: 2_499_00},
		{SKU: "DOCK-USB-C", Available: 75, PriceCents: 899_00},
		{SKU: "CABLE-HDMI", Available: 500, PriceCents: 49_00},
	}
}
