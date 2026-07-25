// Package cli owns mrstack's argv contract and process-level exit behavior.
//
// It deliberately does not perform repository or GitLab work. Callers inject a
// Handler, which makes parsing independently testable and keeps argv as data.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

const (
	APIVersion = "mrstack/v1"
)

// Version is overridden by the release main package from linker-injected
// metadata. It remains "dev" for local builds and tests.
var Version = "dev"

// Handler performs an already parsed invocation.
type Handler interface {
	Dispatch(context.Context, Invocation) (Result, error)
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(context.Context, Invocation) (Result, error)

func (f HandlerFunc) Dispatch(ctx context.Context, inv Invocation) (Result, error) {
	return f(ctx, inv)
}

// Result contains the two representations of a successful command. Machine
// must be a complete mrstack/v1 envelope; the CLI never splices JSON fragments.
type Result struct {
	Human   string
	Machine any
}

// ExitError classifies an operational failure at the process boundary.
type ExitError struct {
	Class     string
	Code      string
	Message   string
	Retryable bool
	Cause     error
}

func (e *ExitError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return e.Code
}

func (e *ExitError) Unwrap() error { return e.Cause }

func Invalid(code, message string) error {
	return &ExitError{Class: "invalid_input", Code: code, Message: message}
}

func Unavailable(code, message string, retryable bool) error {
	return &ExitError{Class: "unavailable", Code: code, Message: message, Retryable: retryable}
}

func Internal(message string, cause error) error {
	return &ExitError{
		Class: "internal", Code: "internal_invariant_failed",
		Message: message, Cause: cause,
	}
}

// Run parses args using a deliberately unconfigured handler. Production main
// packages should use RunWithHandler once application services are assembled.
func Run(args []string, stdout, stderr io.Writer) int {
	return RunWithHandler(args, stdout, stderr, nil)
}

// RunWithHandler parses, dispatches, renders exactly one representation, and
// maps failures to the frozen process exit classes.
func RunWithHandler(args []string, stdout, stderr io.Writer, handler Handler) int {
	inv, err := Parse(args)
	if err != nil {
		name := commandNameFromArgs(args)
		machine := wantsJSON(args)
		return renderFailure(stdout, stderr, name, machine, err)
	}

	if inv.Help {
		_, _ = io.WriteString(stdout, HelpText)
		return 0
	}
	if inv.ShowVersion {
		_, _ = fmt.Fprintf(stdout, "mrstack %s\n", Version)
		return 0
	}
	if handler == nil {
		return renderFailure(stdout, stderr, inv.Name, inv.Machine(),
			Internal("mrstack command services are not configured", nil))
	}

	result, err := handler.Dispatch(context.Background(), inv)
	if err != nil {
		return renderFailure(stdout, stderr, inv.Name, inv.Machine(), err)
	}
	if inv.Machine() {
		if result.Machine == nil {
			return renderFailure(stdout, stderr, inv.Name, true,
				Internal("handler returned no machine envelope", nil))
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(result.Machine); err != nil {
			_, _ = fmt.Fprintf(stderr, "mrstack: write output: %v\n", err)
			return 4
		}
		return 0
	}
	if result.Human != "" {
		_, _ = io.WriteString(stdout, result.Human)
		if result.Human[len(result.Human)-1] != '\n' {
			_, _ = io.WriteString(stdout, "\n")
		}
	}
	return 0
}

func renderFailure(stdout, stderr io.Writer, name CommandName, machine bool, err error) int {
	exitErr := normalizeError(err)
	exitCode := exitCodeForClass(exitErr.Class)
	if machine {
		envelope := failureEnvelope{
			APIVersion:  APIVersion,
			GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano),
			Command: commandEnvelope{
				Name:         name,
				InvocationID: newInvocationID(),
			},
			Outcome: outcomeEnvelope{
				Status: "failed", Class: exitErr.Class, Code: exitErr.Code,
				ExitCode: exitCode, Retryable: exitErr.Retryable,
			},
			Disposition:  nil,
			Stack:        nil,
			Findings:     []any{},
			Evidence:     []any{},
			Remediations: []any{},
			Session:      nil,
			Data:         map[string]any{},
			Error:        errorEnvelope{Message: exitErr.Error()},
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetEscapeHTML(false)
		if encodeErr := encoder.Encode(envelope); encodeErr != nil {
			_, _ = fmt.Fprintf(stderr, "mrstack: write output: %v\n", encodeErr)
			return 4
		}
		return exitCode
	}
	_, _ = fmt.Fprintf(stderr, "mrstack: %s\n", exitErr.Error())
	return exitCode
}

func normalizeError(err error) *ExitError {
	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		if validExitClassCode(exitErr.Class, exitErr.Code) {
			if exitErr.Class != "unavailable" {
				exitErr.Retryable = false
			}
			return exitErr
		}
	}
	return &ExitError{
		Class: "internal", Code: "internal_invariant_failed",
		Message: "unexpected internal failure", Cause: err,
	}
}

func validExitClassCode(class, code string) bool {
	switch class {
	case "invalid_input":
		return code == "invalid_arguments" || code == "invalid_selector" || code == "unknown_command"
	case "unavailable":
		switch code {
		case "not_git_repository", "git_unavailable", "glab_unavailable",
			"authentication_failed", "gitlab_transport_failed",
			"git_transport_failed", "server_mode_undetermined",
			"prerequisite_unsupported", "journal_unavailable":
			return true
		}
	case "internal":
		return code == "internal_invariant_failed"
	}
	return false
}

func exitCodeForClass(class string) int {
	switch class {
	case "invalid_input":
		return 2
	case "unavailable":
		return 3
	default:
		return 4
	}
}

var invocationCounter atomic.Uint64

func newInvocationID() string {
	// Uniqueness is process-local at this layer; durable IDs belong to services.
	return fmt.Sprintf("cmd_%d", invocationCounter.Add(1))
}

type failureEnvelope struct {
	APIVersion   string          `json:"api_version"`
	GeneratedAt  string          `json:"generated_at"`
	Command      commandEnvelope `json:"command"`
	Outcome      outcomeEnvelope `json:"outcome"`
	Disposition  any             `json:"disposition"`
	Stack        any             `json:"stack"`
	Findings     []any           `json:"findings"`
	Evidence     []any           `json:"evidence"`
	Remediations []any           `json:"remediations"`
	Session      any             `json:"session"`
	Data         map[string]any  `json:"data"`
	Error        errorEnvelope   `json:"error"`
}

type commandEnvelope struct {
	Name         CommandName `json:"name"`
	InvocationID string      `json:"invocation_id"`
}

type outcomeEnvelope struct {
	Status    string `json:"status"`
	Class     string `json:"class"`
	Code      string `json:"code"`
	ExitCode  int    `json:"exit_code"`
	Retryable bool   `json:"retryable"`
}

type errorEnvelope struct {
	Message string `json:"message"`
}
