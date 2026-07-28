package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nkaewam/mrstack/internal/cli"
	"github.com/nkaewam/mrstack/internal/stackstore"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func stackHandler(t *testing.T, repo string) (*Handler, string) {
	t.Helper()
	stacksDir := filepath.Join(t.TempDir(), "stacks")
	h := &Handler{
		Runner: fakeGlabRunner{responses: map[string]json.RawMessage{}},
		Dir:    repo, StacksDir: stacksDir,
		Now: func() time.Time { return time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC) },
	}
	return h, stacksDir
}

func runStack(t *testing.T, h *Handler, args ...string) (int, []byte, []byte) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	exit := cli.RunWithHandler(args, &stdout, &stderr, h)
	return exit, stdout.Bytes(), stderr.Bytes()
}

func validateStackEnvelope(t *testing.T, stdout []byte) {
	t.Helper()
	schemaPath := filepath.Join(repositoryRoot(t), "docs", "schema", "mrstack-v1.schema.json")
	schema, err := jsonschema.NewCompiler().Compile(schemaPath)
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(stdout))
	if err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("schema-invalid stack output:\n%s\n%v", stdout, err)
	}
}

// machineErrorMessage extracts the error.message field from a machine-mode
// failure envelope, returning "" for a non-JSON or successful document.
func machineErrorMessage(t *testing.T, stdout []byte) string {
	t.Helper()
	var env struct {
		Outcome struct {
			Status string `json:"status"`
		} `json:"outcome"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(stdout, &env); err != nil {
		t.Fatalf("cannot decode failure envelope: %v\n%s", err, stdout)
	}
	return env.Error.Message
}

func TestStackCreatePersistsAndSchemaValidates(t *testing.T) {
	repo, _, _, _, _ := createStackRepository(t)
	h, stacksDir := stackHandler(t, repo)

	exit, stdout, stderr := runStack(t, h,
		"--json", "--no-input", "--remote", "origin", "stack", "create", "web-migration")
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr)
	}
	validateStackEnvelope(t, stdout)

	var env struct {
		Data struct {
			Stack struct {
				Name    string `json:"name"`
				Host    string `json:"host"`
				Project string `json:"project"`
				Members []int  `json:"member_iids"`
			} `json:"stack"`
		} `json:"data"`
	}
	if err := json.Unmarshal(stdout, &env); err != nil {
		t.Fatal(err)
	}
	if env.Data.Stack.Name != "web-migration" ||
		env.Data.Stack.Host != "gitlab.example" ||
		env.Data.Stack.Project != "group/project" {
		t.Fatalf("unexpected stack: %+v", env.Data.Stack)
	}
	if len(env.Data.Stack.Members) != 0 {
		t.Fatalf("new stack must have no members: %v", env.Data.Stack.Members)
	}
	if _, err := os.Stat(filepath.Join(stacksDir, "web-migration.json")); err != nil {
		t.Fatalf("stack file not persisted: %v", err)
	}
}

func TestStackCreateRejectsDuplicateAndInvalidName(t *testing.T) {
	repo, _, _, _, _ := createStackRepository(t)
	h, _ := stackHandler(t, repo)

	args := []string{"--json", "--no-input", "--remote", "origin", "stack", "create", "web-migration"}
	if exit, _, _ := runStack(t, h, args...); exit != 0 {
		t.Fatalf("first create failed: exit=%d", exit)
	}
	exit, stdout, _ := runStack(t, h, args...)
	if exit == 0 {
		t.Fatal("duplicate create must fail")
	}
	if msg := machineErrorMessage(t, stdout); !bytes.Contains([]byte(msg), []byte("already exists")) {
		t.Fatalf("duplicate error must mention 'already exists': %q", msg)
	}

	exit, stdout, _ = runStack(t, h, "--json", "--no-input", "--remote", "origin", "stack", "create", "Bad Name")
	if exit == 0 {
		t.Fatal("invalid name create must fail")
	}
	if msg := machineErrorMessage(t, stdout); !bytes.Contains([]byte(msg), []byte("invalid stack name")) {
		t.Fatalf("invalid name error must mention 'invalid stack name': %q", msg)
	}
}

func TestStackAddAppendsDedupesAndRejectsUnknown(t *testing.T) {
	repo, _, _, _, _ := createStackRepository(t)
	h, _ := stackHandler(t, repo)

	if exit, _, _ := runStack(t, h,
		"--json", "--no-input", "--remote", "origin", "stack", "create", "s"); exit != 0 {
		t.Fatalf("create failed: exit=%d", exit)
	}
	exit, stdout, stderr := runStack(t, h,
		"--json", "--no-input", "stack", "add", "s", "3061", "3062", "3061")
	if exit != 0 {
		t.Fatalf("add failed: exit=%d stderr=%s", exit, stderr)
	}
	validateStackEnvelope(t, stdout)
	var env struct {
		Data struct {
			Stack struct {
				Members []int `json:"member_iids"`
			} `json:"stack"`
		} `json:"data"`
	}
	if err := json.Unmarshal(stdout, &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Data.Stack.Members) != 2 ||
		env.Data.Stack.Members[0] != 3061 || env.Data.Stack.Members[1] != 3062 {
		t.Fatalf("dedup add wrong members: %v", env.Data.Stack.Members)
	}

	// add does not require the current repo and rejects unknown stacks.
	exit, stdout, _ = runStack(t, h, "--json", "--no-input", "stack", "add", "missing", "1")
	if exit == 0 {
		t.Fatal("add to missing stack must fail")
	}
	if msg := machineErrorMessage(t, stdout); !bytes.Contains([]byte(msg), []byte("no stack named")) {
		t.Fatalf("missing-stack error must mention 'no stack named': %q", msg)
	}
}

func TestStackListScopedToRepoAndAllFlag(t *testing.T) {
	repo, _, _, _, _ := createStackRepository(t)
	h, stacksDir := stackHandler(t, repo)

	// Current-repo stack via the CLI.
	if exit, _, _ := runStack(t, h,
		"--json", "--no-input", "--remote", "origin", "stack", "create", "current"); exit != 0 {
		t.Fatalf("create current failed: exit=%d", exit)
	}
	// Foreign-project stack seeded directly in the same store.
	store, err := stackstore.Open(stacksDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create("foreign", "other.example", "other/project",
		"2026-07-28T12:00:00Z"); err != nil {
		t.Fatal(err)
	}

	exit, stdout, stderr := runStack(t, h,
		"--json", "--no-input", "--remote", "origin", "stack", "list")
	if exit != 0 {
		t.Fatalf("list failed: exit=%d stderr=%s", exit, stderr)
	}
	validateStackEnvelope(t, stdout)
	var env struct {
		Data struct {
			Stacks []struct {
				Name string `json:"name"`
			} `json:"stacks"`
		} `json:"data"`
	}
	if err := json.Unmarshal(stdout, &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Data.Stacks) != 1 || env.Data.Stacks[0].Name != "current" {
		t.Fatalf("scoped list must show only current-repo stack: %+v", env.Data.Stacks)
	}

	exit, stdout, stderr = runStack(t, h,
		"--json", "--no-input", "--remote", "origin", "stack", "list", "--all")
	if exit != 0 {
		t.Fatalf("list --all failed: exit=%d stderr=%s", exit, stderr)
	}
	validateStackEnvelope(t, stdout)
	if err := json.Unmarshal(stdout, &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Data.Stacks) != 2 {
		t.Fatalf("list --all must show both stacks: %+v", env.Data.Stacks)
	}
	names := []string{env.Data.Stacks[0].Name, env.Data.Stacks[1].Name}
	if names[0] != "current" || names[1] != "foreign" {
		t.Fatalf("list --all must be sorted: %+v", names)
	}
}

func TestStackCreateRequiresRepository(t *testing.T) {
	h, _ := stackHandler(t, t.TempDir()) // not a git repo
	exit, stdout, _ := runStack(t, h,
		"--json", "--no-input", "stack", "create", "x")
	if exit == 0 {
		t.Fatal("create outside a git repo must fail")
	}
	if msg := machineErrorMessage(t, stdout); !bytes.Contains([]byte(msg), []byte("repository")) {
		t.Fatalf("expected git/repository error, got: %q", msg)
	}
}

func TestStackRemoveDeletesMembersAndRejectsUnknown(t *testing.T) {
	repo, _, _, _, _ := createStackRepository(t)
	h, _ := stackHandler(t, repo)

	if exit, _, _ := runStack(t, h,
		"--json", "--no-input", "--remote", "origin", "stack", "create", "s"); exit != 0 {
		t.Fatalf("create failed: exit=%d", exit)
	}
	if exit, _, _ := runStack(t, h,
		"--json", "--no-input", "stack", "add", "s", "3061", "3062", "3063"); exit != 0 {
		t.Fatalf("add failed: exit=%d", exit)
	}
	exit, stdout, _ := runStack(t, h,
		"--json", "--no-input", "stack", "remove", "s", "3062", "9999")
	if exit != 0 {
		t.Fatalf("remove failed: exit=%d", exit)
	}
	validateStackEnvelope(t, stdout)
	var env struct {
		Data struct {
			Stack struct {
				Members []int `json:"member_iids"`
			} `json:"stack"`
		} `json:"data"`
	}
	if err := json.Unmarshal(stdout, &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Data.Stack.Members) != 2 ||
		env.Data.Stack.Members[0] != 3061 || env.Data.Stack.Members[1] != 3063 {
		t.Fatalf("remove left wrong members: %v", env.Data.Stack.Members)
	}

	// remove from a nonexistent stack is invalid_selector, not a store error.
	exit, stdout, _ = runStack(t, h, "--json", "--no-input", "stack", "remove", "missing", "1")
	if exit == 0 {
		t.Fatal("remove from missing stack must fail")
	}
	if msg := machineErrorMessage(t, stdout); !bytes.Contains([]byte(msg), []byte("no stack named")) {
		t.Fatalf("missing-stack error must mention 'no stack named': %q", msg)
	}
}

func TestStackDeleteRequiresYesInMachineModeAndSucceeds(t *testing.T) {
	repo, _, _, _, _ := createStackRepository(t)
	h, _ := stackHandler(t, repo)

	if exit, _, _ := runStack(t, h,
		"--json", "--no-input", "--remote", "origin", "stack", "create", "s"); exit != 0 {
		t.Fatalf("create failed: exit=%d", exit)
	}
	// Machine mode without --yes must be rejected before touching the store.
	exit, stdout, _ := runStack(t, h, "--json", "--no-input", "stack", "delete", "s")
	if exit == 0 {
		t.Fatal("delete in machine mode without --yes must fail")
	}
	if msg := machineErrorMessage(t, stdout); !bytes.Contains([]byte(msg), []byte("--yes")) {
		t.Fatalf("delete must demand --yes in machine mode: %q", msg)
	}
	// The stack must still exist (rejection happened before dispatch).
	exit, stdout, _ = runStack(t, h, "--json", "--no-input", "stack", "list", "--all")
	if exit != 0 {
		t.Fatalf("list failed: exit=%d", exit)
	}
	var list struct {
		Data struct {
			Stacks []struct {
				Name string `json:"name"`
			} `json:"stacks"`
		} `json:"data"`
	}
	if err := json.Unmarshal(stdout, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Data.Stacks) != 1 || list.Data.Stacks[0].Name != "s" {
		t.Fatalf("stack must still exist after rejected delete: %+v", list.Data.Stacks)
	}

	// With --yes the stack is deleted and the response describes the removed stack.
	exit, stdout, _ = runStack(t, h, "--json", "--no-input", "--yes", "stack", "delete", "s")
	if exit != 0 {
		t.Fatalf("delete with --yes failed: exit=%d", exit)
	}
	validateStackEnvelope(t, stdout)
	var env struct {
		Data struct {
			Stack struct {
				Name string `json:"name"`
			} `json:"stack"`
		} `json:"data"`
	}
	if err := json.Unmarshal(stdout, &env); err != nil {
		t.Fatal(err)
	}
	if env.Data.Stack.Name != "s" {
		t.Fatalf("delete response must name the removed stack: %+v", env.Data.Stack)
	}

	// Deleting again reports not found.
	exit, stdout, _ = runStack(t, h, "--json", "--no-input", "--yes", "stack", "delete", "s")
	if exit == 0 {
		t.Fatal("deleting a missing stack must fail")
	}
	if msg := machineErrorMessage(t, stdout); !bytes.Contains([]byte(msg), []byte("no stack named")) {
		t.Fatalf("missing delete must mention 'no stack named': %q", msg)
	}
}
