package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nkaewam/mrstack/internal/api"
	"github.com/nkaewam/mrstack/internal/cli"
	"github.com/nkaewam/mrstack/internal/gitexec"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestDoctorReportsCompleteSchemaValidCapabilityAssessment(t *testing.T) {
	repo, _, _, _, _ := createStackRepository(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	runner := fakeGlabRunner{
		responses: map[string]json.RawMessage{
			"/projects/group%2Fproject": json.RawMessage(`{
				"id":42,
				"path_with_namespace":"group/project",
				"web_url":"https://gitlab.example/group/project",
				"default_branch":"main"
			}`),
			"/user":    json.RawMessage(`{"id":7,"username":"developer"}`),
			"/version": json.RawMessage(`{"version":"18.11.3-ee"}`),
		},
		dynamic: func(endpoint string, args []string) (gitexec.Result, error) {
			if endpoint == "" && reflect.DeepEqual(args, []string{"--version"}) {
				return gitexec.Result{Stdout: []byte("glab 1.70.0\n")}, nil
			}
			return gitexec.Result{}, nil
		},
	}
	handler := &Handler{
		Runner:   runner,
		Dir:      repo,
		StateDir: stateDir,
		Now: func() time.Time {
			return time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
		},
	}

	var stdout, stderr bytes.Buffer
	exit := cli.RunWithHandler(
		[]string{"doctor", "--json", "--no-input", "--remote", "origin"},
		&stdout,
		&stderr,
		handler,
	)
	if exit != 0 || stderr.Len() != 0 {
		t.Fatalf("doctor exit=%d stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
	}
	if bytes.Count(stdout.Bytes(), []byte("\n")) != 1 {
		t.Fatalf("machine output is not one JSON document: %q", stdout.String())
	}

	schemaPath := filepath.Join(repositoryRoot(t), "docs", "schema", "mrstack-v1.schema.json")
	schema, err := jsonschema.NewCompiler().Compile(schemaPath)
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(stdout.Bytes()))
	if err != nil {
		t.Fatalf("decode schema instance: %v", err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("schema-invalid doctor output:\n%s\n%v", stdout.String(), err)
	}

	var envelope struct {
		Data struct {
			Doctor api.DoctorData `json:"doctor"`
		} `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	doctor := envelope.Data.Doctor
	if doctor.RequestedMode != "auto" || doctor.DetectedMode == nil ||
		*doctor.DetectedMode != "legacy" || doctor.EffectiveMode != "legacy" ||
		doctor.ServerVersion == nil || *doctor.ServerVersion != "18.11.3-ee" {
		t.Fatalf("incorrect mode assessment: %#v", doctor)
	}
	want := []api.Capability{
		{Name: "repository_context", Status: "verified", Summary: "Git repository and selected remote resolve to the authenticated GitLab project."},
		{Name: "git", Status: "verified", Summary: "Git is available."},
		{Name: "glab", Status: "verified", Summary: "glab is available."},
		{Name: "gitlab_auth", Status: "verified", Summary: "GitLab authentication succeeded."},
		{Name: "server_mode", Status: "verified", Summary: "GitLab server mode was detected."},
		{Name: "atomic_push", Status: "unverified", Summary: "Atomic push behavior is verified safely only during a real publication."},
		{Name: "target_update", Status: "unverified", Summary: "Target-update permission is verified safely only during a real target update."},
		{Name: "sqlite_journal", Status: "verified", Summary: "The SQLite journal is available in WAL mode."},
	}
	if !reflect.DeepEqual(doctor.Capabilities, want) {
		t.Fatalf("capabilities:\n got: %#v\nwant: %#v", doctor.Capabilities, want)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "journal.sqlite")); err != nil {
		t.Fatalf("verified journal was not initialized: %v", err)
	}
}

func TestDoctorWithExplicitModeReportsUnobservableDetectionAsNull(t *testing.T) {
	repo, _, _, _, _ := createStackRepository(t)
	runner := fakeGlabRunner{
		responses: map[string]json.RawMessage{
			"/projects/group%2Fproject": json.RawMessage(`{
				"id":42,
				"path_with_namespace":"group/project",
				"web_url":"https://gitlab.example/group/project",
				"default_branch":"main"
			}`),
			"/user": json.RawMessage(`{"id":7,"username":"developer"}`),
		},
		dynamic: func(endpoint string, args []string) (gitexec.Result, error) {
			if endpoint == "" && reflect.DeepEqual(args, []string{"--version"}) {
				return gitexec.Result{Stdout: []byte("glab 1.70.0\n")}, nil
			}
			if endpoint == "/version" {
				return gitexec.Result{}, &gitexec.CommandError{
					Name: "glab", Args: args, ExitCode: 1, Stderr: "version unavailable",
				}
			}
			return gitexec.Result{}, nil
		},
	}
	handler := &Handler{
		Runner: runner, Dir: repo, StateDir: filepath.Join(t.TempDir(), "state"),
	}
	result, err := handler.doctor(context.Background(), cli.Invocation{
		Name: cli.CommandDoctor,
		Globals: cli.Globals{
			Remote: "origin", GitLabMode: "legacy",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	envelope := result.Machine.(api.Envelope)
	doctor := envelope.Data["doctor"].(api.DoctorData)
	if doctor.DetectedMode != nil || doctor.ServerVersion != nil ||
		doctor.EffectiveMode != "legacy" {
		t.Fatalf("unobservable explicit mode was represented as detected: %#v", doctor)
	}
}

// TestDebugFlagLogsGlabArgvAndStderr verifies --debug installs a runner wrapper
// that prints each glab argv and any stderr to the handler's stderr sink, so
// transport failures are diagnosable instead of collapsed to glab_unavailable.
func TestDebugFlagLogsGlabArgvAndStderr(t *testing.T) {
	repo, _, _, _, _ := createStackRepository(t)
	runner := fakeGlabRunner{
		responses: map[string]json.RawMessage{
			"/projects/group%2Fproject": json.RawMessage(`{
				"id":42,"path_with_namespace":"group/project",
				"web_url":"https://gitlab.example/group/project","default_branch":"main"
			}`),
			"/user": json.RawMessage(`{"id":7,"username":"developer"}`),
		},
		dynamic: func(endpoint string, args []string) (gitexec.Result, error) {
			if endpoint == "" && reflect.DeepEqual(args, []string{"--version"}) {
				return gitexec.Result{Stdout: []byte("glab 1.70.0\n")}, nil
			}
			if endpoint == "/version" {
				return gitexec.Result{Stderr: []byte("version unavailable")},
					&gitexec.CommandError{Name: "glab", Args: args, ExitCode: 1, Stderr: "version unavailable"}
			}
			return gitexec.Result{}, nil
		},
	}
	var stderr bytes.Buffer
	handler := &Handler{
		Runner: runner, Dir: repo, Stderr: &stderr,
		StateDir: filepath.Join(t.TempDir(), "state"),
	}
	_, err := handler.Dispatch(context.Background(), cli.Invocation{
		Name:    cli.CommandDoctor,
		Globals: cli.Globals{Remote: "origin", GitLabMode: "legacy", Debug: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	log := stderr.String()
	if !strings.Contains(log, "glab api") {
		t.Fatalf("debug log must record glab argv, got: %s", log)
	}
	if !strings.Contains(log, "version unavailable") {
		t.Fatalf("debug log must record glab stderr, got: %s", log)
	}
}

// TestClassifyGlabSurfacesUnderlyingError verifies the unavailable mapping
// no longer swallows the real glab stderr / decode error.
func TestClassifyGlabSurfacesUnderlyingError(t *testing.T) {
	t.Parallel()
	err := classifyGlab("list merge requests", &gitexec.CommandError{
		Name: "glab", ExitCode: 2, Stderr: "boom from glab",
	})
	exitErr, ok := err.(*cli.ExitError)
	if !ok {
		t.Fatalf("expected *cli.ExitError, got %T", err)
	}
	if !strings.Contains(exitErr.Message, "boom from glab") || !strings.Contains(exitErr.Message, "exited 2") {
		t.Fatalf("underlying stderr/exit code not surfaced: %q", exitErr.Message)
	}

	decodeErr := classifyGlab("list merge requests", fmt.Errorf("decode GitLab response for /x: invalid character"))
	exitErr2, ok := decodeErr.(*cli.ExitError)
	if !ok {
		t.Fatalf("expected *cli.ExitError, got %T", decodeErr)
	}
	if !strings.Contains(exitErr2.Message, "invalid character") {
		t.Fatalf("underlying decode error not surfaced: %q", exitErr2.Message)
	}
}
