package detectors

import (
	"os/exec"
	"runtime"
	"strings"
)

// listProcessNames returns running process names on the current OS.
func listProcessNames() ([]string, error) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("tasklist", "/fo", "csv", "/nh")
	default:
		cmd = exec.Command("ps", "-eo", "comm")
	}
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if runtime.GOOS == "windows" {
			line = strings.Trim(strings.SplitN(line, ",", 2)[0], `"`)
			line = strings.TrimSuffix(line, ".exe")
		}
		names = append(names, line)
	}
	return names, nil
}
