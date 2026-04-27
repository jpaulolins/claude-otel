package detectors_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"go-hook-mcp-api/internal/detect"
	"go-hook-mcp-api/internal/detect/detectors"
)

func TestNetwork_MCPProbe_NoResponse(t *testing.T) {
	d := detectors.NewNetworkDetector(
		func(string, string, time.Duration) (net.Conn, error) { return nil, errors.New("refused") },
		func() ([]string, error) { return nil, nil },
	)
	findings, err := d.Detect(context.Background())
	if err != nil || len(findings) != 0 {
		t.Errorf("expected 0 findings; got %d err=%v", len(findings), err)
	}
}

func TestNetwork_ActiveConnection_HighSeverity(t *testing.T) {
	d := detectors.NewNetworkDetector(
		func(string, string, time.Duration) (net.Conn, error) { return nil, errors.New("refused") },
		func() ([]string, error) { return []string{"api.anthropic.com:443"}, nil },
	)
	findings, _ := d.Detect(context.Background())
	if len(findings) == 0 || findings[0].Severity != detect.SeverityHigh {
		t.Error("expected high finding for active anthropic connection")
	}
}
