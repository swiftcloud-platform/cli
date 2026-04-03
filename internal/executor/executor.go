package executor

import (
	"fmt"
	"os/exec"
	"strings"
)

// LocalExecutor runs commands on the local machine
type LocalExecutor struct{}

// NewLocalExecutor creates a new local executor
func NewLocalExecutor() *LocalExecutor {
	return &LocalExecutor{}
}

// Run executes a command locally and returns stdout, stderr, and error
func (e *LocalExecutor) Run(command string) (string, string, error) {
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return "", "", fmt.Errorf("empty command")
	}

	cmd := exec.Command(parts[0], parts[1:]...)
	output, err := cmd.CombinedOutput()
	outputStr := strings.TrimSpace(string(output))

	if err != nil {
		return "", outputStr, fmt.Errorf("command failed: %w", err)
	}

	return outputStr, "", nil
}
