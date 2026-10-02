package irswap

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
)

type Currency string

type DayCountConvention string

const (
	DayCountActual360      DayCountConvention = "ACT/360"
	DayCountActual365Fixed DayCountConvention = "ACT/365F"
	DayCountThirty360      DayCountConvention = "30/360"
	DayCountActualActual   DayCountConvention = "ACT/ACT"
)

type BusinessDayConvention string

const (
	BusinessDayFollowing         BusinessDayConvention = "following"
	BusinessDayModifiedFollowing BusinessDayConvention = "modified_following"
	BusinessDayPreceding         BusinessDayConvention = "preceding"
	BusinessDayUnadjusted        BusinessDayConvention = "unadjusted"
)

type Frequency string

const (
	FrequencyMonthly    Frequency = "monthly"
	FrequencyQuarterly  Frequency = "quarterly"
	FrequencySemiannual Frequency = "semiannual"
	FrequencyAnnual     Frequency = "annual"
	FrequencyZero       Frequency = "zero"
)

type ContractStatus string

const (
	ContractStatusActive     ContractStatus = "active"
	ContractStatusTerminated ContractStatus = "terminated"
	ContractStatusMatured    ContractStatus = "matured"
)

type LegType string

const (
	LegTypeFixed    LegType = "fixed"
	LegTypeFloating LegType = "floating"
)

type Contract struct {
	ID                    string
	Currency              Currency
	TradeDate             time.Time
	StartDate             time.Time
	MaturityDate          time.Time
	FixedRate             decimal.Decimal
	FloatingIndex         string
	Spread                decimal.Decimal
	FixedDayCount         DayCountConvention
	FloatingDayCount      DayCountConvention
	PaymentFrequency      Frequency
	ResetFrequency        Frequency
	BusinessDayConvention BusinessDayConvention
	CalendarID            string
	FixedPayer            bool
	Notional              decimal.Decimal
	NotionalSchedule      []NotionalChange
	Status                ContractStatus
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type NotionalChange struct {
	Date     time.Time
	Notional decimal.Decimal
}

type Cashflow struct {
	ID           string
	ContractID   string
	Leg          LegType
	PeriodStart  time.Time
	PeriodEnd    time.Time
	ResetDate    time.Time
	PaymentDate  time.Time
	Rate         decimal.Decimal
	Notional     decimal.Decimal
	Amount       decimal.Decimal
	Currency     Currency
	Confirmed    bool
	ResetApplied bool
	CreatedAt    time.Time
}

type RateReset struct {
	ContractID string
	ResetDate  time.Time
	Rate       decimal.Decimal
	CurveID    string
	CreatedAt  time.Time
}

type Payment struct {
	ID             string
	ContractID     string
	PeriodStart    time.Time
	PeriodEnd      time.Time
	PaymentDate    time.Time
	FixedAmount    decimal.Decimal
	FloatingAmount decimal.Decimal
	NetAmount      decimal.Decimal
	Currency       Currency
	ConfirmedAt    time.Time
}

type Termination struct {
	ContractID string
	Date       time.Time
	Amount     decimal.Decimal
	Currency   Currency
	CreatedAt  time.Time
}

type MarketCurve interface {
	ID() string
	DiscountFactor(ctx context.Context, currency Currency, date time.Time) (decimal.Decimal, error)
	ForwardRate(ctx context.Context, currency Currency, resetDate, periodStart, periodEnd time.Time, dayCount DayCountConvention) (decimal.Decimal, error)
}

type BusinessCalendar interface {
	ID() string
	IsBusinessDay(date time.Time) bool
}

type CalendarRegistry interface {
	Get(id string) (BusinessCalendar, error)
}
