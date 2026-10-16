// Package grpcserver contains the gRPC transport layer of the quote service:
// the GetRate and LockQuote methods.
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

// The CBL-003 RPC processing budgets (GetRate 100 ms, LockQuote 300 ms)
// are applied as the handler deadline with an allowance for transport.
const (
	getRateBudget   = 90 * time.Millisecond
	lockQuoteBudget = 250 * time.Millisecond
)

// Quote implements the gRPC QuoteService.
type Quote struct {
	quotev1.UnimplementedQuoteServiceServer
	svc *usecase.Service
}

// NewQuote creates the QuoteService server.
func NewQuote(svc *usecase.Service) *Quote {
	return &Quote{svc: svc}
}

// GetRate returns the aggregated rate of the pair with anomaly protection.
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

// LockQuote locks the rate of the pair for a TTL and saves the quote for audit.
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

// mapError maps domain errors to platform errors with a class for the gRPC code.
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
