// Package gitexec contains the deliberately small, argv-only boundary to Git.
// It never invokes a shell and never mutates a caller-managed worktree unless a
// method's name explicitly says that it does.
package gitexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

type Result struct {
	Stdout []byte
	Stderr []byte
}

type CommandRunner interface {
	Run(ctx context.Context, dir, name string, args ...string) (Result, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, dir, name string, args ...string) (Result, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	result := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return result, &CommandError{
				Name: name, Args: append([]string(nil), args...),
				ExitCode: exitErr.ExitCode(), Stderr: strings.TrimSpace(stderr.String()),
			}
		}
		return result, err
	}
	return result, nil
}

type CommandError struct {
	Name     string
	Args     []string
	ExitCode int
	Stderr   string
}

func (e *CommandError) Error() string {
	return fmt.Sprintf("%s exited %d: %s", e.Name, e.ExitCode, e.Stderr)
}
