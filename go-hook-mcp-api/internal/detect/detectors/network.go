package detectors

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"time"

	"go-hook-mcp-api/internal/detect"
)

var mcpProbePorts = []string{"3000", "5000", "8000", "8080", "11434"}

var aiDomains = []string{
	"api.anthropic.com",
	"api.openai.com",
	"api2.cursor.sh",
	"repo42.cursor.sh",
	"generativelanguage.googleapis.com",
	"huggingface.co",
	"ollama.ai",
}

type NetworkDetector struct {
	dialFn     func(network, addr string, timeout time.Duration) (net.Conn, error)
	connScanFn func() ([]string, error)
}

func NewNetworkDetector(dialFn func(string, string, time.Duration) (net.Conn, error), connFn func() ([]string, error)) *NetworkDetector {
	return &NetworkDetector{dialFn: dialFn, connScanFn: connFn}
}

func NewNetwork() *NetworkDetector {
	return NewNetworkDetector(
		func(n, a string, t time.Duration) (net.Conn, error) { return net.DialTimeout(n, a, t) },
		scanActiveConnections,
	)
}

func (d *NetworkDetector) Name() string { return "network" }

func (d *NetworkDetector) Detect(_ context.Context) ([]detect.Finding, error) {
	var findings []detect.Finding

	for _, port := range mcpProbePorts {
		addr := "127.0.0.1:" + port
		conn, err := d.dialFn("tcp", addr, 2*time.Second)
		if err != nil {
			continue
		}
		probe := `{"jsonrpc":"2.0","method":"initialize","params":{},"id":1}`
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		conn.Write([]byte(probe))
		buf := make([]byte, 256)
		n, _ := conn.Read(buf)
		conn.Close()
		if n > 0 && json.Valid(buf[:n]) {
			findings = append(findings, detect.Finding{
				Tool: "unknown", Module: "network",
				Signal:   "mcp server responding",
				Path:     addr,
				Severity: detect.SeverityMedium,
				Metadata: map[string]string{"port": port},
			})
		}
	}

	remotes, _ := d.connScanFn()
	for _, remote := range remotes {
		for _, domain := range aiDomains {
			if strings.Contains(remote, domain) {
				findings = append(findings, detect.Finding{
					Tool: domainToTool(domain), Module: "network",
					Signal:   "active connection to ai api",
					Path:     remote,
					Severity: detect.SeverityHigh,
				})
				break
			}
		}
	}
	return findings, nil
}

func domainToTool(domain string) string {
	switch {
	case strings.Contains(domain, "anthropic"):
		return "claude"
	case strings.Contains(domain, "openai"):
		return "codex"
	case strings.Contains(domain, "cursor"):
		return "cursor"
	default:
		return "unknown"
	}
}

// scanActiveConnections stub — real implementation would parse /proc/net/tcp or run netstat.
func scanActiveConnections() ([]string, error) {
	return nil, nil
}
