// Пакет grpcserver содержит gRPC-транспортный слой сервиса quote:
// метод GetRate; LockQuote реализуется задачей 6.2.
package grpcserver

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	quotev1 "github.com/cbl/cbl-protos/gen/go/billing/quote/v1"

	"github.com/cbl/cbl-kit/grpcmw"

	"github.com/cbl/cbl-quote/internal/domain"
	"github.com/cbl/cbl-quote/internal/usecase"
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
	rate, err := s.svc.GetRate(ctx, req.GetBaseAsset(), req.GetQuoteAsset())
	if err != nil {
		return nil, mapError(err)
	}
	return &quotev1.GetRateResponse{
		Rate: rate.Value,
		AsOf: timestamppb.New(rate.AsOf),
	}, nil
}

// mapError переводит доменные ошибки в ошибки платформы с классом для gRPC-кода.
func mapError(err error) error {
	switch {
	case errors.Is(err, domain.ErrInvalidRate):
		return grpcmw.E(grpcmw.KindInvalidArgument, "invalid_rate_request", "некорректная пара или значение курса", err)
	case errors.Is(err, domain.ErrSpread):
		return grpcmw.E(grpcmw.KindUnavailable, "rate_spread_too_high", "расхождение источников курса выше предела", err)
	case errors.Is(err, domain.ErrNoSource):
		return grpcmw.E(grpcmw.KindUnavailable, "rate_source_unavailable", "источники курса недоступны", err)
	default:
		return status.Error(codes.Internal, "internal error")
	}
}
