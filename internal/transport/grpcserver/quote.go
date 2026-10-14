// Пакет grpcserver содержит gRPC-транспортный слой сервиса quote:
// методы GetRate и LockQuote.
package grpcserver

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	quotev1 "github.com/cbl/cbl-protos/gen/go/billing/quote/v1"

	"github.com/cbl/cbl-kit/grpcmw"

	"github.com/cbl/cbl-quote/internal/domain"
	"github.com/cbl/cbl-quote/internal/usecase"
)

// Бюджеты CBL-003 на обработку RPC (GetRate 100 мс, LockQuote 300 мс)
// применяются как дедлайн обработчика с запасом на транспорт.
const (
	getRateBudget   = 90 * time.Millisecond
	lockQuoteBudget = 250 * time.Millisecond
)

// Quote реализует gRPC-сервис QuoteService.
type Quote struct {
	quotev1.UnimplementedQuoteServiceServer
	svc *usecase.Service
}

// NewQuote создает сервер QuoteService.
func NewQuote(svc *usecase.Service) *Quote {
	return &Quote{svc: svc}
}

// GetRate возвращает агрегированный курс пары с защитой от аномалий.
func (s *Quote) GetRate(ctx context.Context, req *quotev1.GetRateRequest) (*quotev1.GetRateResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, getRateBudget)
	defer cancel()

	rate, err := s.svc.GetRate(ctx, req.GetBaseAsset(), req.GetQuoteAsset())
	if err != nil {
		return nil, mapError(err)
	}
	return &quotev1.GetRateResponse{
		Rate: rate.Value,
		AsOf: timestamppb.New(rate.AsOf),
	}, nil
}

// LockQuote фиксирует курс пары на срок TTL и сохраняет котировку для аудита.
func (s *Quote) LockQuote(ctx context.Context, req *quotev1.LockQuoteRequest) (*quotev1.LockQuoteResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, lockQuoteBudget)
	defer cancel()

	locked, err := s.svc.LockQuote(ctx, usecase.LockQuoteRequest{
		FromAsset:   req.GetFromAsset(),
		ToAsset:     req.GetToAsset(),
		AmountAsset: req.GetAmount().GetAsset(),
		AmountMinor: req.GetAmount().GetMinorUnits(),
		TTLSeconds:  req.GetTtlSeconds(),
	})
	if err != nil {
		return nil, mapError(err)
	}
	return &quotev1.LockQuoteResponse{
		QuoteId:     locked.ID,
		Rate:        locked.Rate,
		LockedUntil: timestamppb.New(locked.LockedUntil),
	}, nil
}

// mapError переводит доменные ошибки в ошибки платформы с классом для gRPC-кода.
func mapError(err error) error {
	switch {
	case errors.Is(err, domain.ErrInvalidRate):
		return grpcmw.E(grpcmw.KindInvalidArgument, "invalid_rate_request", "некорректная пара или значение курса", err)
	case errors.Is(err, domain.ErrInvalidLock):
		return grpcmw.E(grpcmw.KindInvalidArgument, "invalid_lock_request", "некорректный запрос фиксации курса", err)
	case errors.Is(err, domain.ErrSpread):
		return grpcmw.E(grpcmw.KindUnavailable, "rate_spread_too_high", "расхождение источников курса выше предела", err)
	case errors.Is(err, domain.ErrNoSource):
		return grpcmw.E(grpcmw.KindUnavailable, "rate_source_unavailable", "источники курса недоступны", err)
	case errors.Is(err, domain.ErrLockStorage):
		return grpcmw.E(grpcmw.KindUnavailable, "quote_storage_unavailable", "хранилище котировок недоступно", err)
	case errors.Is(err, context.DeadlineExceeded):
		return grpcmw.E(grpcmw.KindUnavailable, "rate_timeout", "превышен бюджет обработки курса", err)
	case errors.Is(err, context.Canceled):
		return grpcmw.E(grpcmw.KindUnavailable, "request_canceled", "запрос отменен", err)
	default:
		return status.Error(codes.Internal, "internal error")
	}
}
