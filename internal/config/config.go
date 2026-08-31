// Package config читает настройки обоих сервисов из переменных окружения.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// API — конфигурация сервиса заказов (REST + клиент склада).
type API struct {
	HTTPAddr        string
	ShutdownTimeout time.Duration
	RequestTimeout  time.Duration

	PostgresDSN      string
	PostgresMaxConns int32
	PostgresMinConns int32

	InventoryAddr    string
	InventoryTimeout time.Duration

	OutboxInterval  time.Duration
	OutboxBatchSize int
	OutboxWorkers   int
}

// Inventory — конфигурация складского gRPC-сервиса.
type Inventory struct {
	GRPCAddr        string
	ShutdownTimeout time.Duration
}

func LoadAPI() (API, error) {
	cfg := API{
		HTTPAddr:         env("HTTP_ADDR", ":8081"),
		ShutdownTimeout:  envDuration("SHUTDOWN_TIMEOUT", 15*time.Second),
		RequestTimeout:   envDuration("REQUEST_TIMEOUT", 10*time.Second),
		PostgresDSN:      env("POSTGRES_DSN", "postgres://orders:orders@localhost:5433/orders?sslmode=disable"),
		PostgresMaxConns: int32(envInt("POSTGRES_MAX_CONNS", 10)),
		PostgresMinConns: int32(envInt("POSTGRES_MIN_CONNS", 2)),
		InventoryAddr:    env("INVENTORY_ADDR", "localhost:9090"),
		InventoryTimeout: envDuration("INVENTORY_TIMEOUT", 3*time.Second),
		OutboxInterval:   envDuration("OUTBOX_INTERVAL", 2*time.Second),
		OutboxBatchSize:  envInt("OUTBOX_BATCH_SIZE", 100),
		OutboxWorkers:    envInt("OUTBOX_WORKERS", 4),
	}
	if cfg.PostgresDSN == "" {
		return API{}, fmt.Errorf("config: POSTGRES_DSN обязателен")
	}
	if cfg.InventoryAddr == "" {
		return API{}, fmt.Errorf("config: INVENTORY_ADDR обязателен")
	}
	return cfg, nil
}

func LoadInventory() (Inventory, error) {
	return Inventory{
		GRPCAddr:        env("GRPC_ADDR", ":9090"),
		ShutdownTimeout: envDuration("SHUTDOWN_TIMEOUT", 10*time.Second),
	}, nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
