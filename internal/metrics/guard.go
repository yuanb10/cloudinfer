package metrics

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	dto "github.com/prometheus/client_model/go"
)

type requestLabels struct {
	Endpoint string
	Backend  string
	Status   string
}

type latencyLabels struct {
	Backend string
	Model   string
}

var allowedMetricLabels = map[string]map[string]struct{}{
	"cloudinfer_requests_total": {
		"endpoint": {},
		"backend":  {},
		"status":   {},
	},
	"cloudinfer_route_decisions_total": {
		"backend": {},
		"reason":  {},
	},
	"cloudinfer_route_fallbacks_total": {
		"from_backend": {},
		"to_backend":   {},
		"reason":       {},
	},
	"cloudinfer_route_retries_total": {
		"backend": {},
		"status":  {},
	},
	"cloudinfer_breaker_transitions_total": {
		"backend":    {},
		"from_state": {},
		"to_state":   {},
	},
	"cloudinfer_ttft_seconds": {
		"backend": {},
		"model":   {},
	},
	"cloudinfer_stream_duration_seconds": {
		"backend": {},
		"model":   {},
	},
	"cloudinfer_draining": {},
}

var (
	uuidPattern  = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\b`)
	emailPattern = regexp.MustCompile(`(?i)\b[A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,}\b`)
	tokenPattern = regexp.MustCompile(`(?i)\b(sk-[A-Z0-9]{8,}|bearer\s+[A-Z0-9._\-]{8,}|eyJ[A-Z0-9._\-]{10,})\b`)
)

func AllowedMetricLabels() map[string][]string {
	out := make(map[string][]string, len(allowedMetricLabels))
	for name, labels := range allowedMetricLabels {
		keys := slices.Collect(maps.Keys(labels))
		slices.Sort(keys)
		out[name] = keys
	}

	return out
}

func ValidateMetricFamilies(families []*dto.MetricFamily) error {
	for _, family := range families {
		if family == nil || family.Name == nil {
			continue
		}

		if err := ValidateMetricFamily(family); err != nil {
			return err
		}
	}

	return nil
}

func ValidateMetricFamily(family *dto.MetricFamily) error {
	if family == nil || family.Name == nil {
		return nil
	}

	name := family.GetName()
	allowed, ok := allowedMetricLabels[name]
	if !ok {
		return fmt.Errorf("metric %q is not part of the allowlist", name)
	}

	for _, metric := range family.Metric {
		for _, label := range metric.Label {
			labelName := label.GetName()
			if _, ok := allowed[labelName]; !ok {
				return fmt.Errorf("metric %q has forbidden label %q", name, labelName)
			}
		}
	}

	return nil
}

func ValidateMetricMap(families map[string]*dto.MetricFamily) error {
	names := make([]string, 0, len(families))
	for name := range families {
		names = append(names, name)
	}
	slices.Sort(names)

	for _, name := range names {
		if err := ValidateMetricFamily(families[name]); err != nil {
			return err
		}
	}

	return nil
}

func ForbiddenLabels(metricName string, labels ...string) []string {
	allowed, ok := allowedMetricLabels[metricName]
	if !ok {
		return append([]string(nil), labels...)
	}

	forbidden := make([]string, 0, len(labels))
	for _, label := range labels {
		label = strings.TrimSpace(label)
		if label == "" {
			continue
		}
		if _, ok := allowed[label]; !ok {
			forbidden = append(forbidden, label)
		}
	}

	slices.Sort(forbidden)
	return forbidden
}

func sanitizeLabelValue(value string) string {
	value = strings.TrimSpace(value)
	switch {
	case value == "":
		return ""
	case len(value) > 64:
		return "redacted"
	case uuidPattern.MatchString(value):
		return "redacted"
	case emailPattern.MatchString(value):
		return "redacted"
	case tokenPattern.MatchString(value):
		return "redacted"
	default:
		return value
	}
}
