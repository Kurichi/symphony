package orchestrator

import (
	"time"

	"github.com/Kurichi/symphony/internal/config"
)

// RetryDelay calculates the delay for a retry attempt. SPEC §8.4.
// Continuation retries (attempt=1 with continuation flag) use 1s fixed delay.
// Failure retries use exponential backoff: min(10000 * 2^(attempt-1), max_backoff_ms).
func RetryDelay(attempt int, isContinuation bool, maxBackoffMs int) time.Duration {
	if isContinuation && attempt == 1 {
		return time.Duration(config.ContinuationRetryDelayMs) * time.Millisecond
	}
	return failureRetryDelay(attempt, maxBackoffMs)
}

func failureRetryDelay(attempt int, maxBackoffMs int) time.Duration {
	if attempt <= 0 {
		attempt = 1
	}
	// Cap the exponent to prevent overflow
	exp := attempt - 1
	if exp > 10 {
		exp = 10
	}
	delay := config.FailureRetryBaseMs * (1 << exp)
	if delay > maxBackoffMs {
		delay = maxBackoffMs
	}
	return time.Duration(delay) * time.Millisecond
}

// NextRetryAttempt determines the next retry attempt number from a running entry.
// Returns nil for first-time retries (will become attempt 1).
func NextRetryAttempt(entry *RunningEntry) *int {
	if entry == nil || entry.Attempt == nil || *entry.Attempt <= 0 {
		return nil
	}
	next := *entry.Attempt + 1
	return &next
}
