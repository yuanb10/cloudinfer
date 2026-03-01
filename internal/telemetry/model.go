package telemetry

type TelemetryEvent struct {
	RequestID      string `json:"request_id"`
	Model          string `json:"model"`
	Stream         bool   `json:"stream"`
	Status         string `json:"status"`
	TTFTms         int64  `json:"ttft_ms"`
	TotalLatencyms int64  `json:"total_latency_ms"`
	CreatedUnix    int64  `json:"created_unix"`
}
