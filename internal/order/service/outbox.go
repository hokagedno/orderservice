package service

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/hokagedno/orderservice/internal/order/domain"
)

// Publisher — куда уходят события. В реальном проекте это Kafka/RabbitMQ;
// здесь интерфейс с одной реализацией-логгером, чтобы не тащить брокер
// в pet-проект, но оставить точку расширения.
type Publisher interface {
	Publish(ctx context.Context, e domain.OutboxEvent) error
}

// LogPublisher — заглушка, печатающая событие в лог.
type LogPublisher struct{ Log *slog.Logger }

func (p LogPublisher) Publish(ctx context.Context, e domain.OutboxEvent) error {
	p.Log.InfoContext(ctx, "событие опубликовано",
		"type", e.Type, "aggregate_id", e.AggregateID, "payload", string(e.Payload))
	return nil
}

// OutboxWorker периодически вычитывает неопубликованные события и
// отправляет их в Publisher параллельно, пулом горутин.
//
// Конкурентные примитивы:
//   - time.Ticker — периодический опрос таблицы;
//   - канал jobs как очередь заданий для пула воркеров;
//   - sync.WaitGroup — дожидаемся завершения пачки перед отметкой в БД;
//   - sync.Mutex вокруг слайса результатов: несколько горутин пишут в него;
//   - context — единая точка остановки для всех горутин.
type OutboxWorker struct {
	repo      domain.OrderRepository
	publisher Publisher
	log       *slog.Logger

	interval  time.Duration
	batchSize int
	workers   int

	wg   sync.WaitGroup
	stop chan struct{}
	once sync.Once
}

type OutboxConfig struct {
	Interval  time.Duration
	BatchSize int
	Workers   int
}

func NewOutboxWorker(repo domain.OrderRepository, pub Publisher, log *slog.Logger, cfg OutboxConfig) *OutboxWorker {
	if cfg.Interval <= 0 {
		cfg.Interval = 2 * time.Second
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 100
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 4
	}
	return &OutboxWorker{
		repo: repo, publisher: pub, log: log,
		interval: cfg.Interval, batchSize: cfg.BatchSize, workers: cfg.Workers,
		stop: make(chan struct{}),
	}
}

// Start запускает фоновый цикл. Возврат — сразу, работа идёт в горутине.
func (w *OutboxWorker) Start(ctx context.Context) {
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()

		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-w.stop:
				return
			case <-ticker.C:
				if err := w.processBatch(ctx); err != nil {
					w.log.ErrorContext(ctx, "ошибка обработки outbox", "err", err)
				}
			}
		}
	}()
}

// processBatch забирает пачку событий и публикует их пулом горутин.
func (w *OutboxWorker) processBatch(ctx context.Context) error {
	events, err := w.repo.FetchUnpublished(ctx, w.batchSize)
	if err != nil {
		return err
	}
	if len(events) == 0 {
		return nil
	}

	jobs := make(chan domain.OutboxEvent)

	var (
		mu        sync.Mutex
		published []int64
	)

	var wg sync.WaitGroup
	wg.Add(w.workers)
	for i := 0; i < w.workers; i++ {
		go func() {
			defer wg.Done()
			// Диапазон по каналу завершается сам, когда канал закрыт, —
			// не нужен отдельный сигнальный канал для остановки воркеров.
			for e := range jobs {
				if err := w.publisher.Publish(ctx, e); err != nil {
					w.log.ErrorContext(ctx, "не удалось опубликовать событие",
						"id", e.ID, "err", err)
					continue // событие останется неопубликованным и уйдёт в следующую пачку
				}
				mu.Lock()
				published = append(published, e.ID)
				mu.Unlock()
			}
		}()
	}

	// Отправка заданий: select с ctx.Done() не даёт зависнуть,
	// если контекст отменили, а воркеры уже завершились.
	for _, e := range events {
		select {
		case jobs <- e:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return ctx.Err()
		}
	}
	close(jobs)
	wg.Wait()

	// Отметка «опубликовано» ставится ПОСЛЕ успешной публикации.
	// Поэтому гарантия доставки — at-least-once: при падении между
	// публикацией и UPDATE событие уйдёт повторно. Потребитель обязан
	// быть идемпотентным; это осознанный компромисс, exactly-once
	// в распределённой системе недостижим.
	if err := w.repo.MarkPublished(ctx, published); err != nil {
		return err
	}
	w.log.DebugContext(ctx, "outbox: пачка обработана", "published", len(published))
	return nil
}

// Stop останавливает воркер и дожидается завершения текущей пачки.
func (w *OutboxWorker) Stop() {
	w.once.Do(func() { close(w.stop) })
	w.wg.Wait()
}
