// Package grpcutil — общие перехватчики (interceptors) для gRPC.
//
// Перехватчик в gRPC — тот же паттерн «Декоратор»/«Цепочка обязанностей», что
// и middleware в HTTP: он оборачивает вызов хендлера, добавляя сквозную
// функциональность (логирование, восстановление после паники, метрики).
package grpcutil

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// UnaryLogger логирует каждый unary-вызов: метод, код ответа, длительность.
func UnaryLogger(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req) // вызов следующего звена цепочки

		code := status.Code(err)
		level := slog.LevelInfo
		if code != codes.OK {
			level = slog.LevelWarn
		}
		log.Log(ctx, level, "grpc call",
			"method", info.FullMethod,
			"code", code.String(),
			"duration_ms", time.Since(start).Milliseconds(),
		)
		return resp, err
	}
}

// UnaryRecovery превращает панику в ошибку codes.Internal.
// Без него паника в одном хендлере уронила бы весь процесс сервера.
func UnaryRecovery(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				log.ErrorContext(ctx, "паника в grpc-хендлере",
					"method", info.FullMethod, "panic", r, "stack", string(debug.Stack()))
				// Именованное возвращаемое значение err позволяет подменить
				// результат функции уже из defer.
				err = status.Error(codes.Internal, "внутренняя ошибка сервера")
			}
		}()
		return handler(ctx, req)
	}
}

// StreamLogger — аналог UnaryLogger для потоковых вызовов.
func StreamLogger(log *slog.Logger) grpc.StreamServerInterceptor {
	return func(
		srv any,
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		start := time.Now()
		err := handler(srv, ss)
		log.Log(ss.Context(), slog.LevelInfo, "grpc stream closed",
			"method", info.FullMethod,
			"code", status.Code(err).String(),
			"duration_ms", time.Since(start).Milliseconds(),
		)
		return err
	}
}
