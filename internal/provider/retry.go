package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	rejectionBodyLimit = 8192
	maxRetryAfter      = time.Duration(1<<63 - 1)
	maxBackoffExponent = 6
	initialBackoff     = 250 * time.Millisecond
	codeOverloaded     = "server_is_overloaded"
)

// Retryable means the provider explicitly rejected a request before inference.
// Unknown usage, transport errors and unrecognized error codes are never retried here.
func Retryable(err error) bool {
	var failure *Failure
	if !errors.As(err, &failure) || failure.UnknownUsage {
		return false
	}
	return failure.Status == http.StatusTooManyRequests &&
		(failure.Code == "rate_limit_exceeded" || failure.Code == "rate_limit" || failure.Code == "slow_down") ||
		failure.Status == http.StatusServiceUnavailable && failure.Code == codeOverloaded
}

func httpFailure(res *http.Response) *Failure {
	failure := compactHTTPFailure(res.StatusCode)
	var wire struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, rejectionBodyLimit+1))
	if err == nil && len(data) <= rejectionBodyLimit && json.Unmarshal(data, &wire) == nil {
		switch wire.Error.Code {
		case "rate_limit_exceeded",
			"rate_limit",
			"slow_down",
			codeOverloaded,
			"insufficient_quota",
			"billing_hard_limit_reached",
			"spend_limit":
			failure.Code = wire.Error.Code
		}
	}
	if permanentRejection(failure.Code) ||
		failure.Status == http.StatusServiceUnavailable && failure.Code == codeOverloaded {
		failure.UnknownUsage = false
	}
	failure.RetryAfter = retryAfter(res.Header.Get("Retry-After"), time.Now())
	return failure
}

func retryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err == nil && seconds >= 0 {
		// Saturate unreasonably large values rather than overflow into an immediate retry.
		if seconds > int64(maxRetryAfter/time.Second) {
			return maxRetryAfter
		}
		return time.Duration(seconds) * time.Second
	}
	if errors.Is(err, strconv.ErrRange) && !strings.HasPrefix(value, "-") {
		return maxRetryAfter
	}
	if until, dateErr := http.ParseTime(value); dateErr == nil {
		return max(0, until.Sub(now))
	}
	return 0
}

// RetryDelay uses bounded exponential jitter; Retry-After is always a minimum.
func RetryDelay(attempt int, minimum time.Duration) time.Duration {
	attempt = min(max(attempt, 0), maxBackoffExponent)
	base := initialBackoff * time.Duration(1<<attempt)
	return max(
		minimum,
		base+time.Duration(rand.Int64N(int64(base))),
	) // #nosec G404 -- Scheduling jitter does not protect secrets.
}

// ShortRetry repeats only known rejections, within a shared backoff and dispatch budget.
// The caller keeps one reservation and checkpoints the final response once.
func ShortRetry(
	ctx context.Context,
	retries int,
	budget time.Duration,
	call func(context.Context) (Response, error),
) (Response, error) {
	started := time.Now()
	for attempt := 0; ; attempt++ {
		response, err := call(ctx)
		if !Retryable(err) || response.ID != "" || len(response.Output) != 0 || len(response.Calls) != 0 {
			return response, err
		}
		failure, _ := errors.AsType[*Failure](err)
		copyFailure := *failure
		copyFailure.RetryAttempts = attempt + 1
		err = &copyFailure
		delay := RetryDelay(attempt, failure.RetryAfter)
		elapsed := time.Since(started)
		if attempt >= retries || elapsed >= budget || delay > budget-elapsed || ctx.Err() != nil {
			return response, err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return response, err
		case <-timer.C:
		}
		if ctx.Err() != nil || time.Since(started) >= budget {
			return response, err
		}
	}
}

func permanentRejection(code string) bool {
	switch code {
	case "insufficient_quota", "billing_hard_limit_reached", "spend_limit":
		return true
	default:
		return false
	}
}
