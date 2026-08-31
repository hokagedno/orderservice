// Package app — composition root обоих сервисов.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	nethttp "net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/hokagedno/orderservice/internal/config"
	"github.com/hokagedno/orderservice/internal/inventoryclient"
	"github.com/hokagedno/orderservice/internal/order/repository/postgres"
	"github.com/hokagedno/orderservice/internal/order/service"
	orderhttp "github.com/hokagedno/orderservice/internal/order/transport/http"
)

// RunAPI поднимает REST-сервис заказов.
func RunAPI() error {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	cfg, err := config.LoadAPI()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := postgres.NewPool(ctx, cfg.PostgresDSN, cfg.PostgresMaxConns, cfg.PostgresMinConns, 0)
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	defer pool.Close()

	if err := postgres.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("миграции: %w", err)
	}
	log.Info("миграции применены")

	invClient, err := inventoryclient.Dial(cfg.InventoryAddr, cfg.InventoryTimeout)
	if err != nil {
		return fmt.Errorf("клиент склада: %w", err)
	}
	defer func() { _ = invClient.Close() }()

	repo := postgres.NewOrderRepository(pool)
	orders := service.New(repo, invClient, log)

	outbox := service.NewOutboxWorker(repo, service.LogPublisher{Log: log}, log, service.OutboxConfig{
		Interval:  cfg.OutboxInterval,
		BatchSize: cfg.OutboxBatchSize,
		Workers:   cfg.OutboxWorkers,
	})
	outbox.Start(ctx)

	handler := orderhttp.NewHandler(orders, log)
	e := orderhttp.NewRouter(handler, log, orderhttp.RouterConfig{
		RequestTimeout: cfg.RequestTimeout,
	})

	errCh := make(chan error, 1)
	go func() {
		log.Info("http-сервер заказов запущен", "addr", cfg.HTTPAddr)
		if err := e.Start(cfg.HTTPAddr); err != nil && !errors.Is(err, nethttp.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("http-сервер: %w", err)
	case <-ctx.Done():
		log.Info("получен сигнал остановки")
	}

	// Порядок остановки: сначала перестаём принимать HTTP-запросы,
	// затем дожидаемся текущей пачки outbox, затем закрываются пулы (defer).
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := e.Shutdown(shutdownCtx); err != nil {
		log.Error("http-сервер не остановился штатно", "err", err)
	}
	outbox.Stop()
	log.Info("сервис заказов остановлен")
	return nil
}
