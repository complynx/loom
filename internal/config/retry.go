package config

import "errors"

const (
	defaultProviderRetryAttempts      = 6
	defaultProviderRetryWindowSeconds = 300
	maxProviderRetryAttempts          = 20
	maxProviderRetryWindowSeconds     = 3600
	maxProviderShortRetries           = 2
	maxProviderShortBudgetMS          = 3000
)

func (e Execution) validateProviderRetry() error {
	if e.ProviderRetryAttemptLimit < 0 || e.ProviderRetryAttemptLimit > maxProviderRetryAttempts {
		return errors.New("provider retry attempt limit must be between 0 and 20")
	}
	if e.ProviderRetryWindowSeconds < 0 || e.ProviderRetryWindowSeconds > maxProviderRetryWindowSeconds ||
		e.ProviderRetryAttemptLimit > 0 && e.ProviderRetryWindowSeconds == 0 {
		return errors.New("enabled provider retry needs a positive window up to 3600 seconds")
	}
	if e.ProviderRetryShortLimit < 0 || e.ProviderRetryShortLimit > maxProviderShortRetries ||
		e.ProviderRetryShortBudgetMS < 0 ||
		e.ProviderRetryShortBudgetMS > maxProviderShortBudgetMS {
		return errors.New("provider short retry needs 0-2 retries and a budget up to 3000 milliseconds")
	}
	return nil
}
