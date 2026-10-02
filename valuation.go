package irswap

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

type ValuationRequest struct {
	Curve         MarketCurve
	ValuationDate time.Time
}

type ValuationResult struct {
	ContractID string
	Currency   Currency
	FixedPV    decimal.Decimal
	FloatingPV decimal.Decimal
	NetPV      decimal.Decimal
	AccruedNet decimal.Decimal
}

func (s *Service) ValueContract(ctx context.Context, contractID string, request ValuationRequest) (*ValuationResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if request.Curve == nil {
		return nil, fmt.Errorf("%w: market curve is required", ErrInvalidContract)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.valueContractLocked(ctx, contractID, request)
}

func (s *Service) ValueAll(ctx context.Context, request ValuationRequest) ([]ValuationResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if request.Curve == nil {
		return nil, fmt.Errorf("%w: market curve is required", ErrInvalidContract)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	ids := make([]string, 0, len(s.contracts))
	for id := range s.contracts {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	results := make([]ValuationResult, 0, len(ids))
	for _, id := range ids {
		result, err := s.valueContractLocked(ctx, id, request)
		if err != nil {
			return nil, err
		}
		results = append(results, *result)
	}
	return results, nil
}

func (s *Service) valueContractLocked(ctx context.Context, contractID string, request ValuationRequest) (*ValuationResult, error) {
	contract, ok := s.contracts[contractID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrContractNotFound, contractID)
	}
	if contract.Status == ContractStatusTerminated || contract.Status == ContractStatusMatured {
		return &ValuationResult{ContractID: contractID, Currency: contract.Currency}, nil
	}

	cashflows := s.cashflows[contractID]
	if len(cashflows) == 0 {
		generated, err := s.generateCashflowsLocked(contract)
		if err != nil {
			return nil, err
		}
		cashflows = generated
	}

	fixedPV := decimal.Zero
	floatingPV := decimal.Zero
	accruedNet := decimal.Zero
	for _, cashflow := range cashflows {
		if cashflow.Confirmed || cashflow.PaymentDate.Before(request.ValuationDate) {
			continue
		}

		df, err := request.Curve.DiscountFactor(ctx, contract.Currency, cashflow.PaymentDate)
		if err != nil {
			return nil, err
		}
		if cashflow.Leg == LegTypeFixed {
			fixedPV = fixedPV.Add(cashflow.Amount.Mul(df))
			continue
		}

		amount, err := s.floatingAmountLocked(ctx, contract, cashflow, request.Curve)
		if err != nil {
			return nil, err
		}
		floatingPV = floatingPV.Add(amount.Mul(df))
	}

	for _, cashflow := range cashflows {
		if cashflow.PeriodStart.Before(request.ValuationDate) && !cashflow.PeriodEnd.Before(request.ValuationDate) {
			floatingRate := cashflow.Rate
			if !cashflow.ResetApplied {
				forward, err := request.Curve.ForwardRate(ctx, contract.Currency, cashflow.ResetDate, cashflow.PeriodStart, cashflow.PeriodEnd, contract.FloatingDayCount)
				if err != nil {
					return nil, err
				}
				floatingRate = forward.Add(contract.Spread)
			}
			fixedYF, err := yearFraction(cashflow.PeriodStart, request.ValuationDate, contract.FixedDayCount)
			if err != nil {
				return nil, err
			}
			floatingYF, err := yearFraction(cashflow.PeriodStart, request.ValuationDate, contract.FloatingDayCount)
			if err != nil {
				return nil, err
			}
			fixedAccrued := cashflow.Notional.Mul(contract.FixedRate).Mul(fixedYF)
			floatingAccrued := cashflow.Notional.Mul(floatingRate).Mul(floatingYF)
			accruedNet = accruedNet.Add(fixedAccrued.Sub(floatingAccrued))
		}
	}

	fixedPV = fixedPV.Round(2)
	floatingPV = floatingPV.Round(2)
	accruedNet = accruedNet.Round(2)
	netPV := fixedPV.Sub(floatingPV)
	if !contract.FixedPayer {
		netPV = floatingPV.Sub(fixedPV)
	}
	return &ValuationResult{
		ContractID: contractID,
		Currency:   contract.Currency,
		FixedPV:    fixedPV,
		FloatingPV: floatingPV,
		NetPV:      netPV,
		AccruedNet: accruedNet,
	}, nil
}

func (s *Service) floatingAmountLocked(ctx context.Context, contract *Contract, cashflow Cashflow, curve MarketCurve) (decimal.Decimal, error) {
	if cashflow.ResetApplied {
		return cashflow.Amount, nil
	}
	forward, err := curve.ForwardRate(ctx, contract.Currency, cashflow.ResetDate, cashflow.PeriodStart, cashflow.PeriodEnd, contract.FloatingDayCount)
	if err != nil {
		return decimal.Zero, err
	}
	rate := forward.Add(contract.Spread)
	yf, err := yearFraction(cashflow.PeriodStart, cashflow.PeriodEnd, contract.FloatingDayCount)
	if err != nil {
		return decimal.Zero, err
	}
	return cashflow.Notional.Mul(rate).Mul(yf).Round(2), nil
}
