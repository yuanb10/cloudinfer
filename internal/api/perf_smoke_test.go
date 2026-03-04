package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/myusername/cloudinfer/internal/config"
	"github.com/myusername/cloudinfer/internal/metrics"
)

func TestResponsesLatencySmoke(t *testing.T) {
	mux := http.NewServeMux()
	NewServer(&config.Config{}, noopLogger{}, metrics.New(), nil, readyRuntime(), nil).RegisterRoutes(mux)
	body := `{"model":"default","input":[{"role":"user","content":"hi"}]}`

	const (
		requestCount     = 200
		maxP95Latency    = 10 * time.Millisecond
		maxHeapAllocDiff = 8 << 20
	)

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	latencies := make([]time.Duration, 0, requestCount)
	for i := 0; i < requestCount; i++ {
		start := time.Now()
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		res := rr.Result()
		if res.StatusCode != http.StatusOK {
			res.Body.Close()
			t.Fatalf("request %d status = %d, want %d", i, res.StatusCode, http.StatusOK)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		res.Body.Close()
		latencies = append(latencies, time.Since(start))
	}

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	slices.Sort(latencies)
	p95 := latencies[(len(latencies)*95)/100]
	heapAllocDiff := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	if heapAllocDiff < 0 {
		heapAllocDiff = 0
	}

	if p95 > maxP95Latency {
		t.Fatalf("p95 latency = %s, want <= %s", p95, maxP95Latency)
	}
	if heapAllocDiff > maxHeapAllocDiff {
		t.Fatalf("heap alloc delta = %d bytes, want <= %d", heapAllocDiff, maxHeapAllocDiff)
	}

	t.Logf("latency smoke: p95=%s heap_alloc_delta=%dB requests=%d", p95, heapAllocDiff, requestCount)
}
