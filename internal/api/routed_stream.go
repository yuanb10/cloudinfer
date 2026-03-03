package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/myusername/cloudinfer/internal/adapters"
	"github.com/myusername/cloudinfer/internal/routing"
)

const (
	defaultTTFTTimeout  = 1500 * time.Millisecond
	maxPreTokenAttempts = 2
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

func (s *Server) prepareBackendStream(ctx context.Context, requestModel string, messages []routing.Message) (*preparedBackendStream, string, error) {
	if s.router == nil || !s.router.HasBackends() {
		return nil, "", nil
	}

	decision := s.router.Choose(requestModel)
	for attempt := 0; attempt < maxPreTokenAttempts; attempt++ {
		if decision.Chosen.Client == nil || decision.Chosen.Name == "" {
			return nil, "", nil
		}

		responseModel := decision.Chosen.Client.ResolvedModel(requestModel)
		attemptCtx, cancel := context.WithCancel(ctx)
		tokenCh, errCh := decision.Chosen.Client.StreamText(attemptCtx, responseModel, messages)

		firstToken, firstTokenAt, status, err := s.waitForFirstToken(attemptCtx, tokenCh, errCh)
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
		if attempt == maxPreTokenAttempts-1 {
			return nil, status, err
		}

		nextDecision := s.router.Choose(requestModel)
		if nextDecision.Chosen.Name == "" || nextDecision.Chosen.Name == decision.Chosen.Name {
			return nil, status, err
		}
		decision = nextDecision
	}

	return nil, "upstream_error", io.EOF
}

func (s *Server) waitForFirstToken(ctx context.Context, tokenCh <-chan string, errCh <-chan error) (string, time.Time, string, error) {
	var (
		timer  *time.Timer
		timerC <-chan time.Time
	)
	if timeout := s.ttftTimeout(); timeout > 0 {
		timer = time.NewTimer(timeout)
		timerC = timer.C
		defer timer.Stop()
	}

	for tokenCh != nil || errCh != nil {
		select {
		case <-ctx.Done():
			return "", time.Time{}, "client_cancel", ctx.Err()
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

func (s *Server) ttftTimeout() time.Duration {
	if s == nil || s.cfg == nil || s.cfg.Routing.TTFTTimeoutMs <= 0 {
		return defaultTTFTTimeout
	}

	return time.Duration(s.cfg.Routing.TTFTTimeoutMs) * time.Millisecond
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

	s.router.ObserveWithCooldown(
		decision.Chosen.Name,
		status,
		ttftMs,
		isProviderFailureStatus(status),
		retryAfterFromError(err),
	)
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
