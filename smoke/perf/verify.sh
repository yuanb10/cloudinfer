#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/../.." && pwd)"
OUT_DIR="${1:-$ROOT_DIR/.artifacts/perf}"

mkdir -p "$OUT_DIR"

responses_out="$OUT_DIR/responses_performance_smoke.txt"
latency_out="$OUT_DIR/responses_latency_smoke.txt"
tracing_out="$OUT_DIR/tracing_benchmark.txt"

go test ./internal/api -run TestResponsesPerformanceSmoke -v | tee "$responses_out"
go test ./internal/api -run TestResponsesLatencySmoke -v | tee "$latency_out"
go test ./internal/api -run '^$' -bench BenchmarkTracingRequestOverhead -benchmem -count=1 | tee "$tracing_out"

extract_metric() {
  local bench_name="$1"
  local suffix="$2"
  local file="$3"
  awk -v bench="$bench_name" -v suffix="$suffix" '
    $1 ~ bench {
      for (i = 1; i <= NF; i++) {
        if ($i ~ suffix "$") {
          print $(i-1)
          exit
        }
      }
    }
  ' "$file"
}

off_ns="$(extract_metric 'BenchmarkTracingRequestOverhead/off' 'ns/op' "$tracing_out")"
on_ns="$(extract_metric 'BenchmarkTracingRequestOverhead/on_sampled' 'ns/op' "$tracing_out")"
on_bytes="$(extract_metric 'BenchmarkTracingRequestOverhead/on_sampled' 'B/op' "$tracing_out")"
on_allocs="$(extract_metric 'BenchmarkTracingRequestOverhead/on_sampled' 'allocs/op' "$tracing_out")"

if [[ -z "$off_ns" || -z "$on_ns" || -z "$on_bytes" || -z "$on_allocs" ]]; then
  echo "failed to parse tracing benchmark output" >&2
  exit 1
fi

overhead_pct=0
if [[ "$off_ns" -gt 0 ]]; then
  overhead_pct=$(( ( (on_ns - off_ns) * 100 ) / off_ns ))
fi

{
  echo "tracing_off_ns_per_op=$off_ns"
  echo "tracing_on_sampled_ns_per_op=$on_ns"
  echo "tracing_on_sampled_bytes_per_op=$on_bytes"
  echo "tracing_on_sampled_allocs_per_op=$on_allocs"
  echo "tracing_overhead_percent=$overhead_pct"
} | tee "$OUT_DIR/summary.txt"

if [[ "$on_ns" -gt 120000 ]]; then
  echo "sampled tracing latency regression: ${on_ns}ns/op > 120000ns/op" >&2
  exit 1
fi
if [[ "$overhead_pct" -gt 50 ]]; then
  echo "sampled tracing overhead regression: ${overhead_pct}% > 50%" >&2
  exit 1
fi
if [[ "$on_bytes" -gt 45000 ]]; then
  echo "sampled tracing memory regression: ${on_bytes}B/op > 45000B/op" >&2
  exit 1
fi
if [[ "$on_allocs" -gt 320 ]]; then
  echo "sampled tracing allocation regression: ${on_allocs} allocs/op > 320 allocs/op" >&2
  exit 1
fi
