package http

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"golang.org/x/time/rate"
)

// echoValidator подключает go-playground/validator к echo.Context.Validate.
// Echo, в отличие от Gin, не тянет валидатор по умолчанию — его подключают явно.
type echoValidator struct{ v *validator.Validate }

func (ev *echoValidator) Validate(i any) error { return ev.v.Struct(i) }

type RouterConfig struct {
	RequestTimeout time.Duration
	RateLimit      float64 // запросов в секунду на IP
}

// NewRouter собирает маршруты и цепочку middleware.
func NewRouter(h *Handler, log *slog.Logger, cfg RouterConfig) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Validator = &echoValidator{v: validator.New()}

	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 10 * time.Second
	}
	if cfg.RateLimit <= 0 {
		cfg.RateLimit = 50
	}

	// Порядок middleware важен: Recover должен стоять первым, чтобы
	// перехватывать панику из всех последующих слоёв.
	e.Use(middleware.Recover())
	e.Use(middleware.RequestID())
	e.Use(slogMiddleware(log))
	// Лимитер в памяти процесса: для одного экземпляра сервиса этого достаточно.
	// При нескольких репликах лимит нужно выносить в общее хранилище
	// (как сделано в проекте url-shortener через Redis).
	e.Use(middleware.RateLimiter(
		middleware.NewRateLimiterMemoryStore(rate.Limit(cfg.RateLimit)),
	))
	e.Use(middleware.TimeoutWithConfig(middleware.TimeoutConfig{
		Timeout: cfg.RequestTimeout,
	}))

	e.GET("/healthz", h.health)

	api := e.Group("/api/v1")
	api.POST("/orders", h.createOrder)
	api.GET("/orders", h.listOrders)
	api.GET("/orders/:id", h.getOrder)
	api.PATCH("/orders/:id/status", h.changeStatus)

	return e
}

// slogMiddleware пишет структурированный лог по каждому запросу.
func slogMiddleware(log *slog.Logger) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()
			err := next(c) // передаём управление дальше по цепочке

			status := c.Response().Status
			level := slog.LevelInfo
			if status >= http.StatusInternalServerError {
				level = slog.LevelError
			}
			log.Log(c.Request().Context(), level, "http request",
				"method", c.Request().Method,
				"path", c.Path(),
				"status", status,
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", c.Response().Header().Get(echo.HeaderXRequestID),
			)
			return err
		}
	}
}
