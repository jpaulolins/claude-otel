package detect

import "context"

type Severity string

const (
	SeverityInfo   Severity = "info"
	SeverityMedium Severity = "medium"
	SeverityHigh   Severity = "high"
)

type Finding struct {
	Tool     string            `json:"tool"`
	Module   string            `json:"module"`
	Signal   string            `json:"signal"`
	Path     string            `json:"path,omitempty"`
	Severity Severity          `json:"severity"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type Detector interface {
	Name() string
	Detect(ctx context.Context) ([]Finding, error)
}
