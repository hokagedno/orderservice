package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool создаёт пул соединений и проверяет доступность БД.
//
// Пул нужен, потому что установка TCP-соединения и аутентификация в Postgres
// стоят единицы миллисекунд — на каждый HTTP-запрос это недопустимо.
// MaxConns подбирается по числу ядер БД, а не «побольше»: лишние соединения
// в Postgres — это лишние процессы и деградация, а не рост пропускной способности.
func NewPool(ctx context.Context, dsn string, maxConns, minConns int32, lifetime time.Duration) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("разбор DSN: %w", err)
	}
	cfg.MaxConns = maxConns
	cfg.MinConns = minConns
	cfg.MaxConnLifetime = lifetime
	cfg.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("создание пула: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return pool, nil
}
