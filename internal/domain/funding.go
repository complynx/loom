package domain

import (
	"math/big"
	"net/http"
	"time"
)

const FundingFactorScale int64 = 1000000

type MonthlyFundingDefaults struct {
	Enabled          bool  `json:"enabled"`
	BaseMicrocredits int64 `json:"base_microcredits"`
	FactorPPM        int64 `json:"factor_ppm"`
}
type MonthlyFundingPolicy struct {
	Enabled          *bool  `json:"enabled"`
	BaseMicrocredits *int64 `json:"base_microcredits"`
	FactorPPM        *int64 `json:"factor_ppm"`
	Version          int64  `json:"version"`
}

func FundingDefaults(v *MonthlyFundingDefaults) MonthlyFundingDefaults {
	if v == nil {
		return MonthlyFundingDefaults{BaseMicrocredits: FundingFactorScale, FactorPPM: FundingFactorScale}
	}
	return *v
}

// FundingTarget rounds down once, after both fixed-precision factors are applied.
func FundingTarget(d MonthlyFundingDefaults, p MonthlyFundingPolicy) (int64, error) {
	base, factor := d.BaseMicrocredits, FundingFactorScale
	if p.BaseMicrocredits != nil {
		base = *p.BaseMicrocredits
	}
	if p.FactorPPM != nil {
		factor = *p.FactorPPM
	}
	if base < 0 || d.BaseMicrocredits < 0 || d.FactorPPM < 0 || factor < 0 {
		return 0, NewError(http.StatusUnprocessableEntity, "invalid_monthly_funding")
	}
	value := new(big.Int).Mul(big.NewInt(base), big.NewInt(d.FactorPPM))
	value.Mul(value, big.NewInt(factor))
	value.Quo(value, big.NewInt(FundingFactorScale*FundingFactorScale))
	if !value.IsInt64() {
		return 0, NewError(http.StatusUnprocessableEntity, "monthly_funding_overflow")
	}
	return value.Int64(), nil
}

type MonthlyFundingReceipt struct {
	Period               string                 `json:"period"`
	TargetMicrocredits   int64                  `json:"target_microcredits"`
	BalanceMicrocredits  int64                  `json:"balance_microcredits"`
	CreditedMicrocredits int64                  `json:"credited_microcredits"`
	Defaults             MonthlyFundingDefaults `json:"defaults"`
	Policy               MonthlyFundingPolicy   `json:"policy"`
	SettingsVersion      int64                  `json:"settings_version"`
	Reason               string                 `json:"reason"`
	CreatedAt            time.Time              `json:"created_at"`
}
type MonthlyFundingStatus struct {
	Enabled            bool                   `json:"enabled"`
	TargetMicrocredits int64                  `json:"target_microcredits"`
	Policy             MonthlyFundingPolicy   `json:"policy"`
	Defaults           MonthlyFundingDefaults `json:"defaults"`
	Classification     string                 `json:"classification"`
	LastReceipt        *MonthlyFundingReceipt `json:"last_receipt,omitempty"`
	NextRefillAt       *time.Time             `json:"next_refill_at,omitempty"`
}

// MonthlyFundingConfiguration edits funding without a browser roundtrip of unrelated int64 settings.
type MonthlyFundingConfiguration struct {
	Defaults MonthlyFundingDefaults `json:"defaults"`
	Version  int64                  `json:"version"`
}
