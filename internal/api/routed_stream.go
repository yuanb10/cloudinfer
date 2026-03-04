package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/myusername/cloudinfer/internal/adapters"
	"github.com/myusername/cloudinfer/internal/config"
	"github.com/myusername/cloudinfer/internal/routing"
)

const (
	timeoutStatusBudget = "timeout"
)

type preparedBackendStream struct {
	decision      routing.Decision
	backendName   string
	responseModel string
	tokenCh       <-chan string
	errCh         <-chan error
	firstToken    string
	firstTokenAt  time.Time
	cancel        context.CancelFunc
}

type streamEvent struct {
	token       string
	tokenClosed bool
	err         error
	errClosed   bool
	status      string
}

func (s *Server) prepareBackendStream(ctx context.Context, requestModel string, messages []routing.Message) (*preparedBackendStream, string, error) {
	if s.router == nil || !s.router.HasBackends() {
		return nil, "", nil
	}

	budget := routing.BudgetFromContext(ctx)
	decision := s.router.Choose(requestModel)
	for attempts := 1; ; attempts++ {
		if decision.Chosen.Client == nil || decision.Chosen.Name == "" {
			return nil, "", nil
		}
		if s.metrics != nil {
			s.metrics.ObserveRouteDecision(decision.Chosen.Name, decision.Reason)
		}
		retryPolicy := s.retryPolicyFor(decision.Chosen.Name)
		timeouts := s.timeoutPolicyFor(decision.Chosen.Name)

		responseModel := decision.Chosen.Client.ResolvedModel(requestModel)
		attemptCtx, cancel := context.WithCancel(ctx)
		tokenCh, errCh := decision.Chosen.Client.StreamText(attemptCtx, responseModel, messages)

		firstToken, firstTokenAt, status, err := s.waitForFirstToken(attemptCtx, tokenCh, errCh, budget, timeouts.TTFT)
		if status == "" {
			return &preparedBackendStream{
				decision:      decision,
				backendName:   decision.Chosen.Name,
				responseModel: responseModel,
				tokenCh:       tokenCh,
				errCh:         errCh,
				firstToken:    firstToken,
				firstTokenAt:  firstTokenAt,
				cancel:        cancel,
			}, "", nil
		}

		cancel()
		if status == "client_cancel" {
			return nil, status, err
		}

		s.observeRouteWithError(decision, status, -1, err)
		if !retryPolicy.ShouldRetry(false, status, attempts) {
			return nil, status, err
		}

		nextDecision := s.router.Choose(requestModel)
		if nextDecision.Chosen.Name == "" || nextDecision.Chosen.Name == decision.Chosen.Name {
			return nil, status, err
		}

		if s.metrics != nil {
			s.metrics.ObserveRouteRetry(decision.Chosen.Name, status)
			s.metrics.ObserveRouteFallback(decision.Chosen.Name, nextDecision.Chosen.Name, status)
		}

		backoff := retryPolicy.Backoff(attempts-1, retryAfterFromError(err))
		if waitErr := waitForRetryBackoff(ctx, budget, backoff); waitErr != nil {
			if errors.Is(waitErr, context.Canceled) {
				return nil, "client_cancel", waitErr
			}
			return nil, timeoutStatusBudget, &adapters.Error{
				Category: adapters.CategoryTimeout,
				Message:  "total request timeout exceeded during retry backoff",
				Err:      waitErr,
			}
		}
		decision = nextDecision
	}
}

func (s *Server) waitForFirstToken(ctx context.Context, tokenCh <-chan string, errCh <-chan error, budget routing.DeadlineBudget, ttftTimeout time.Duration) (string, time.Time, string, error) {
	var (
		timer  *time.Timer
		timerC <-chan time.Time
	)
	if timeout := budget.PhaseTimeout(time.Now(), ttftTimeout); timeout > 0 {
		timer = time.NewTimer(timeout)
		timerC = timer.C
		defer timer.Stop()
	}

	for tokenCh != nil || errCh != nil {
		select {
		case <-ctx.Done():
			return "", time.Time{}, statusForExecutionContext(ctx), ctx.Err()
		case <-timerC:
			return "", time.Time{}, "timeout", &adapters.Error{
				Category: adapters.CategoryTimeout,
				Message:  "time to first token exceeded",
			}
		case token, ok := <-tokenCh:
			if !ok {
				tokenCh = nil
				continue
			}
			if token == "" {
				continue
			}
			return token, time.Now(), "", nil
		case err, ok := <-errCh:
			if !ok {
				errCh = nil
				continue
			}
			if err == nil {
				continue
			}
			return "", time.Time{}, statusForAdapterError(err), err
		}
	}

	return "", time.Time{}, "upstream_error", io.EOF
}

func (s *Server) applyRouteHeaders(w http.ResponseWriter, requestID string, decision routing.Decision, responseModel string) {
	if s == nil || s.router == nil || !s.router.Enabled() || w == nil || decision.Chosen.Name == "" {
		return
	}

	w.Header().Set("X-CloudInfer-Backend", decision.Chosen.Name)
	w.Header().Set("X-CloudInfer-Backend-Type", decision.Chosen.Type)
	w.Header().Set("X-CloudInfer-Route-Reason", decision.Reason)
	w.Header().Set("X-CloudInfer-Model", responseModel)
	s.logRouteDecision(requestID, decision)
}

func (s *Server) observeRouteWithError(decision routing.Decision, status string, ttftMs int64, err error) {
	if s == nil || s.router == nil || !s.router.Enabled() || decision.Chosen.Name == "" {
		return
	}

	result := s.router.ObserveWithCooldownResult(
		decision.Chosen.Name,
		status,
		ttftMs,
		isProviderFailureStatus(status),
		retryAfterFromError(err),
	)
	if s.metrics != nil && result.BreakerTransition.To != "" {
		s.metrics.ObserveBreakerTransition(
			decision.Chosen.Name,
			string(result.BreakerTransition.From),
			string(result.BreakerTransition.To),
		)
	}
}

func retryAfterFromError(err error) time.Duration {
	var adapterErr *adapters.Error
	if !errors.As(err, &adapterErr) || adapterErr == nil {
		return 0
	}

	if adapterErr.Category != adapters.CategoryRateLimited {
		return 0
	}

	if adapterErr.RetryAfter < 0 {
		return 0
	}

	return adapterErr.RetryAfter
}

func (p *preparedBackendStream) Close() {
	if p == nil || p.cancel == nil {
		return
	}

	p.cancel()
	p.cancel = nil
}

func waitForRetryBackoff(ctx context.Context, budget routing.DeadlineBudget, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}

	if bounded := budget.PhaseTimeout(time.Now(), delay); bounded > 0 {
		delay = bounded
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		if deadline, ok := budget.Deadline(); ok && !time.Now().Before(deadline) {
			return context.DeadlineExceeded
		}
		return nil
	}
}

func statusForExecutionContext(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timeout"
	}

	return "client_cancel"
}

func classifyExecutionFailure(r *http.Request, execCtx context.Context) string {
	if execCtx != nil && execCtx.Err() != nil {
		return statusForExecutionContext(execCtx)
	}

	return classifyStreamError(r)
}

func waitForNextStreamEvent(ctx context.Context, tokenCh <-chan string, errCh <-chan error, budget routing.DeadlineBudget, idleTimeout time.Duration) streamEvent {
	var (
		timer  *time.Timer
		timerC <-chan time.Time
	)
	if timeout := budget.PhaseTimeout(time.Now(), idleTimeout); timeout > 0 {
		timer = time.NewTimer(timeout)
		timerC = timer.C
		defer timer.Stop()
	}

	for tokenCh != nil || errCh != nil {
		select {
		case <-ctx.Done():
			return streamEvent{status: statusForExecutionContext(ctx), err: ctx.Err()}
		case <-timerC:
			return streamEvent{
				status: "timeout",
				err: &adapters.Error{
					Category: adapters.CategoryTimeout,
					Message:  "idle timeout exceeded",
				},
			}
		case token, ok := <-tokenCh:
			if !ok {
				return streamEvent{tokenClosed: true}
			}
			if token == "" {
				continue
			}
			return streamEvent{token: token}
		case err, ok := <-errCh:
			if !ok {
				return streamEvent{errClosed: true}
			}
			if err == nil {
				continue
			}
			return streamEvent{err: err, status: statusForAdapterError(err)}
		}
	}

	return streamEvent{}
}

func (s *Server) timeoutPolicy() routing.TimeoutPolicy {
	policy := routing.TimeoutPolicy{}
	if s != nil && s.router != nil {
		policy = s.router.TimeoutPolicy()
	}
	if s != nil && s.cfg != nil {
		if s.cfg.Routing.TotalTimeoutMs > 0 {
			policy.Total = time.Duration(s.cfg.Routing.TotalTimeoutMs) * time.Millisecond
		}
		if s.cfg.Routing.TTFTTimeoutMs > 0 {
			policy.TTFT = time.Duration(s.cfg.Routing.TTFTTimeoutMs) * time.Millisecond
		}
		if s.cfg.Routing.IdleTimeoutMs > 0 {
			policy.Idle = time.Duration(s.cfg.Routing.IdleTimeoutMs) * time.Millisecond
		}
	}
	return policy.Normalize()
}

func (s *Server) timeoutPolicyFor(backendName string) routing.TimeoutPolicy {
	policy := routing.TimeoutPolicy{}
	if s != nil && s.router != nil {
		policy = s.router.TimeoutPolicyFor(backendName)
	}
	if s != nil && s.cfg != nil {
		if s.cfg.Routing.TotalTimeoutMs > 0 {
			policy.Total = time.Duration(s.cfg.Routing.TotalTimeoutMs) * time.Millisecond
		}
		if s.cfg.Routing.TTFTTimeoutMs > 0 {
			policy.TTFT = time.Duration(s.cfg.Routing.TTFTTimeoutMs) * time.Millisecond
		}
		if s.cfg.Routing.IdleTimeoutMs > 0 {
			policy.Idle = time.Duration(s.cfg.Routing.IdleTimeoutMs) * time.Millisecond
		}
		if override := s.backendRoutingConfig(backendName); override != nil {
			if override.TotalTimeoutMs != nil {
				policy.Total = time.Duration(*override.TotalTimeoutMs) * time.Millisecond
			}
			if override.TTFTTimeoutMs != nil {
				policy.TTFT = time.Duration(*override.TTFTTimeoutMs) * time.Millisecond
			}
			if override.IdleTimeoutMs != nil {
				policy.Idle = time.Duration(*override.IdleTimeoutMs) * time.Millisecond
			}
		}
	}

	return policy.Normalize()
}

func (s *Server) retryPolicy() routing.RetryPolicy {
	policy := routing.RetryPolicy{}
	if s != nil && s.router != nil {
		policy = s.router.RetryPolicy()
	}
	if s != nil && s.cfg != nil {
		if s.cfg.Routing.RetryMaxAttempts > 0 {
			policy.MaxAttempts = s.cfg.Routing.RetryMaxAttempts
		}
		if s.cfg.Routing.RetryBaseBackoffMs > 0 {
			policy.BaseBackoff = time.Duration(s.cfg.Routing.RetryBaseBackoffMs) * time.Millisecond
		}
		if s.cfg.Routing.RetryMaxBackoffMs > 0 {
			policy.MaxBackoff = time.Duration(s.cfg.Routing.RetryMaxBackoffMs) * time.Millisecond
		}
		if s.cfg.Routing.RetryJitterFraction > 0 {
			policy.JitterFraction = s.cfg.Routing.RetryJitterFraction
		}
	}
	return policy.Normalize()
}

func (s *Server) retryPolicyFor(backendName string) routing.RetryPolicy {
	policy := routing.RetryPolicy{}
	if s != nil && s.router != nil {
		policy = s.router.RetryPolicyFor(backendName)
	}
	if s != nil && s.cfg != nil {
		if s.cfg.Routing.RetryMaxAttempts > 0 {
			policy.MaxAttempts = s.cfg.Routing.RetryMaxAttempts
		}
		if s.cfg.Routing.RetryBaseBackoffMs > 0 {
			policy.BaseBackoff = time.Duration(s.cfg.Routing.RetryBaseBackoffMs) * time.Millisecond
		}
		if s.cfg.Routing.RetryMaxBackoffMs > 0 {
			policy.MaxBackoff = time.Duration(s.cfg.Routing.RetryMaxBackoffMs) * time.Millisecond
		}
		if s.cfg.Routing.RetryJitterFraction > 0 {
			policy.JitterFraction = s.cfg.Routing.RetryJitterFraction
		}
		if override := s.backendRoutingConfig(backendName); override != nil {
			if override.RetryMaxAttempts != nil {
				policy.MaxAttempts = *override.RetryMaxAttempts
			}
			if override.RetryBaseBackoffMs != nil {
				policy.BaseBackoff = time.Duration(*override.RetryBaseBackoffMs) * time.Millisecond
			}
			if override.RetryMaxBackoffMs != nil {
				policy.MaxBackoff = time.Duration(*override.RetryMaxBackoffMs) * time.Millisecond
			}
			if override.RetryJitterFraction != nil {
				policy.JitterFraction = *override.RetryJitterFraction
			}
		}
	}

	return policy.Normalize()
}

func (s *Server) executionContextForRequest(ctx context.Context, requestModel string) (context.Context, context.CancelFunc) {
	timeout := s.timeoutPolicy().Total
	if s != nil && s.router != nil && s.router.HasBackends() {
		decision := s.router.Explain(requestModel)
		if decision.Chosen.Name != "" {
			timeout = s.timeoutPolicyFor(decision.Chosen.Name).Total
		}
	}

	return routing.NewDeadlineBudget(time.Now(), timeout).WithContext(ctx)
}

func (s *Server) backendRoutingConfig(backendName string) *config.BackendRoutingConfig {
	if s == nil || s.cfg == nil || backendName == "" {
		return nil
	}

	for idx := range s.cfg.Backends {
		if s.cfg.Backends[idx].Name == backendName {
			return &s.cfg.Backends[idx].Routing
		}
	}

	return nil
}
