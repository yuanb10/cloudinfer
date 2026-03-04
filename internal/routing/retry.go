package routing

import "time"

const (
	defaultRetryAttempts    = 2
	defaultRetryBaseBackoff = 100 * time.Millisecond
	defaultRetryMaxBackoff  = 1 * time.Second
	defaultRetryJitter      = 0.2
)

type RetryPolicy struct {
	MaxAttempts    int
	BaseBackoff    time.Duration
	MaxBackoff     time.Duration
	JitterFraction float64
	JitterSource   func() float64
}

func (p RetryPolicy) Normalize() RetryPolicy {
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = defaultRetryAttempts
	}
	if p.BaseBackoff <= 0 {
		p.BaseBackoff = defaultRetryBaseBackoff
	}
	if p.MaxBackoff <= 0 {
		p.MaxBackoff = defaultRetryMaxBackoff
	}
	if p.MaxBackoff < p.BaseBackoff {
		p.MaxBackoff = p.BaseBackoff
	}
	if p.JitterFraction < 0 {
		p.JitterFraction = 0
	}
	if p.JitterFraction > 1 {
		p.JitterFraction = 1
	}
	if p.JitterSource == nil {
		p.JitterSource = func() float64 { return 0.5 }
	}
	return p
}

func (p RetryPolicy) ShouldRetry(firstTokenEmitted bool, status string, attemptsSoFar int) bool {
	p = p.Normalize()
	if firstTokenEmitted {
		return false
	}
	if attemptsSoFar >= p.MaxAttempts {
		return false
	}

	switch status {
	case "rate_limited", "timeout", "upstream_error":
		return true
	default:
		return false
	}
}

func (p RetryPolicy) Backoff(attempt int, retryAfter time.Duration) time.Duration {
	p = p.Normalize()
	if attempt < 0 {
		attempt = 0
	}

	if retryAfter <= 0 && attempt == 0 {
		return 0
	}

	backoff := p.BaseBackoff
	for i := 0; i < attempt; i++ {
		if backoff >= p.MaxBackoff {
			backoff = p.MaxBackoff
			break
		}
		backoff *= 2
		if backoff > p.MaxBackoff {
			backoff = p.MaxBackoff
			break
		}
	}

	if retryAfter > backoff {
		backoff = retryAfter
	}

	return applyDurationJitter(backoff, p.JitterFraction, p.JitterSource)
}
