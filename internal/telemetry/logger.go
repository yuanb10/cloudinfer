package telemetry

import (
	"encoding/json"
	"log"
	"os"
	"sync"
)

type Logger interface {
	Log(evt TelemetryEvent)
}

type JSONStdoutLogger struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func NewJSONStdoutLogger() *JSONStdoutLogger {
	return &JSONStdoutLogger{
		enc: json.NewEncoder(os.Stdout),
	}
}

func (l *JSONStdoutLogger) Log(evt TelemetryEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if err := l.enc.Encode(evt); err != nil {
		log.Printf("telemetry log failed: %v", err)
	}
}
