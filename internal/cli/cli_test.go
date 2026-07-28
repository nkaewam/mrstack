package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestRunHumanHelpAndVersionDoNotDispatch(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"--help"}, {"help"}, {"--version"}} {
		var stdout, stderr bytes.Buffer
		called := false
		exit := RunWithHandler(args, &stdout, &stderr, HandlerFunc(func(context.Context, Invocation) (Result, error) {
			called = true
			return Result{}, nil
		}))
		equal(t, exit, 0)
		if called {
			t.Fatalf("%q dispatched", args)
		}
		if stdout.Len() == 0 {
			t.Fatalf("%q produced no stdout", args)
		}
		equal(t, stderr.String(), "")
	}
}

func TestRunDispatchesParsedLiteralInvocation(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	var got Invocation
	exit := RunWithHandler(
		[]string{"check", "feature;$(false)|x"},
		&stdout, &stderr,
		HandlerFunc(func(_ context.Context, inv Invocation) (Result, error) {
			got = inv
			return Result{Human: "ok"}, nil
		}),
	)
	equal(t, exit, 0)
	equal(t, got.Selector.Value, "feature;$(false)|x")
	equal(t, stdout.String(), "ok\n")
	equal(t, stderr.String(), "")
}

func TestRunMachineSuccessIsExactlyOneJSONDocumentAndNoPrompt(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	machine := map[string]any{"api_version": APIVersion, "sentinel": "yes"}
	exit := RunWithHandler(
		[]string{"--json", "check", "stk", "--no-input"},
		&stdout, &stderr,
		HandlerFunc(func(_ context.Context, inv Invocation) (Result, error) {
			if !inv.Machine() {
				t.Fatal("handler did not receive machine mode")
			}
			return Result{Human: "PROMPT: continue?", Machine: machine}, nil
		}),
	)
	equal(t, exit, 0)
	equal(t, stderr.String(), "")
	if strings.Contains(stdout.String(), "PROMPT") {
		t.Fatalf("human output leaked: %q", stdout.String())
	}
	var decoded map[string]any
	decoder := json.NewDecoder(&stdout)
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	equal(t, decoded["sentinel"], any("yes"))
	if decoder.More() {
		t.Fatal("more than one JSON document")
	}
}

func TestRunMachineFailuresUseFrozenExitClassesAndCompleteEnvelope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		args    []string
		handler Handler
		exit    int
		class   string
		code    string
		command CommandName
	}{
		{
			"parse", []string{"check", "a", "b", "--json", "--no-input"}, nil,
			2, "invalid_input", "invalid_arguments", CommandCheck,
		},
		{
			"unavailable", []string{"check", "stk", "--json", "--no-input"},
			HandlerFunc(func(context.Context, Invocation) (Result, error) {
				return Result{}, Unavailable("gitlab_transport_failed", "transport failed", true)
			}),
			3, "unavailable", "gitlab_transport_failed", CommandCheck,
		},
		{
			"internal", []string{"check", "stk", "--json", "--no-input"},
			HandlerFunc(func(context.Context, Invocation) (Result, error) {
				return Result{}, errors.New("secret implementation detail")
			}),
			4, "internal", "internal_invariant_failed", CommandCheck,
		},
		{
			"nil machine result", []string{"check", "stk", "--json", "--no-input"},
			HandlerFunc(func(context.Context, Invocation) (Result, error) { return Result{}, nil }),
			4, "internal", "internal_invariant_failed", CommandCheck,
		},
		{
			"invalid handler classification", []string{"check", "stk", "--json", "--no-input"},
			HandlerFunc(func(context.Context, Invocation) (Result, error) {
				return Result{}, &ExitError{Class: "authoritative", Code: "ok", Message: "not a failure"}
			}),
			4, "internal", "internal_invariant_failed", CommandCheck,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			exit := RunWithHandler(tt.args, &stdout, &stderr, tt.handler)
			equal(t, exit, tt.exit)
			equal(t, stderr.String(), "")

			var got struct {
				APIVersion string `json:"api_version"`
				Command    struct {
					Name         CommandName `json:"name"`
					InvocationID string      `json:"invocation_id"`
				} `json:"command"`
				Outcome struct {
					Status    string `json:"status"`
					Class     string `json:"class"`
					Code      string `json:"code"`
					ExitCode  int    `json:"exit_code"`
					Retryable bool   `json:"retryable"`
				} `json:"outcome"`
				Disposition  any            `json:"disposition"`
				Stack        any            `json:"stack"`
				Findings     []any          `json:"findings"`
				Evidence     []any          `json:"evidence"`
				Remediations []any          `json:"remediations"`
				Session      any            `json:"session"`
				Data         map[string]any `json:"data"`
				Error        struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			decoder := json.NewDecoder(&stdout)
			if err := decoder.Decode(&got); err != nil {
				t.Fatalf("Decode() error = %v; stdout=%q", err, stdout.String())
			}
			equal(t, got.APIVersion, APIVersion)
			equal(t, got.Command.Name, tt.command)
			if got.Command.InvocationID == "" {
				t.Fatal("empty invocation ID")
			}
			equal(t, got.Outcome.Status, "failed")
			equal(t, got.Outcome.Class, tt.class)
			equal(t, got.Outcome.Code, tt.code)
			equal(t, got.Outcome.ExitCode, tt.exit)
			if got.Disposition != nil || got.Stack != nil || got.Session != nil {
				t.Fatal("failure claimed authoritative data")
			}
			if got.Findings == nil || got.Evidence == nil || got.Remediations == nil || got.Data == nil {
				t.Fatal("required empty containers were null")
			}
			if got.Error.Message == "" {
				t.Fatal("empty error message")
			}
			if decoder.More() {
				t.Fatal("more than one JSON document")
			}
			if strings.Contains(stdout.String(), "prompt") {
				t.Fatal("prompt leaked into machine output")
			}
		})
	}
}

func TestRunHumanFailuresUseStderrOnly(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	exit := RunWithHandler([]string{"wat"}, &stdout, &stderr, nil)
	equal(t, exit, 2)
	equal(t, stdout.String(), "")
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestNoHandlerIsInternalFailure(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	exit := RunWithHandler([]string{"doctor"}, &stdout, &stderr, nil)
	equal(t, exit, 4)
	if !strings.Contains(stderr.String(), "not configured") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
