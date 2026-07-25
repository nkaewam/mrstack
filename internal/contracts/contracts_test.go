// Package contracts mechanically checks the checked-in public contract and
// documentation as part of ordinary go test and GitHub Actions.
package contracts

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/nkaewam/mrstack/internal/cli"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate contract test")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
}

func compileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	path := filepath.Join(repositoryRoot(t), "docs", "schema", "mrstack-v1.schema.json")
	compiler := jsonschema.NewCompiler()
	schema, err := compiler.Compile(path)
	if err != nil {
		t.Fatalf("compile public schema: %v", err)
	}
	return schema
}

func TestMachineFailureEnvelopeValidatesAgainstFrozenSchema(t *testing.T) {
	t.Parallel()
	schema := compileSchema(t)
	var stdout, stderr bytes.Buffer
	exit := cli.RunWithHandler([]string{"--json", "--no-input", "does-not-exist"}, &stdout, &stderr, nil)
	if exit != 2 {
		t.Fatalf("exit=%d stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(stdout.Bytes()))
	if err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("schema-invalid machine failure:\n%s\n%v", stdout.String(), err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope) != 12 {
		t.Fatalf("top-level key drift: got %d keys: %v", len(envelope), envelope)
	}
}

func TestSchemaRejectsMissingKeyUnknownEnumAndAbbreviatedOID(t *testing.T) {
	t.Parallel()
	schema := compileSchema(t)
	base := map[string]any{
		"api_version": "mrstack/v1", "generated_at": "2026-07-25T12:00:00Z",
		"command":     map[string]any{"name": "check", "invocation_id": "cmd_1"},
		"outcome":     map[string]any{"status": "failed", "class": "invalid_input", "code": "invalid_arguments", "exit_code": 2, "retryable": false},
		"disposition": nil, "stack": nil, "findings": []any{}, "evidence": []any{},
		"remediations": []any{}, "session": nil, "data": map[string]any{},
		"error": map[string]any{"message": "bad input"},
	}
	if err := schema.Validate(base); err != nil {
		t.Fatalf("valid control rejected: %v", err)
	}
	delete(base, "findings")
	if err := schema.Validate(base); err == nil {
		t.Fatal("schema accepted missing required top-level key")
	}
	base["findings"] = []any{}
	base["outcome"].(map[string]any)["class"] = "future_class"
	if err := schema.Validate(base); err == nil {
		t.Fatal("schema accepted unknown producer enum")
	}

	schemaBytes, err := os.ReadFile(filepath.Join(repositoryRoot(t), "docs", "schema", "mrstack-v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(schemaBytes, []byte(`{40}|[0-9A-Fa-f]{64}`)) {
		t.Fatal("schema no longer enforces full 40/64 character object IDs")
	}
}

var markdownLink = regexp.MustCompile(`\[[^\]]+\]\(([^)]+)\)`)

func TestDocumentationLinks(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	files := []string{filepath.Join(root, "README.md"), filepath.Join(root, "CONTEXT.md")}
	docs, err := filepath.Glob(filepath.Join(root, "docs", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, docs...)
	adrs, err := filepath.Glob(filepath.Join(root, "docs", "adr", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, adrs...)
	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range markdownLink.FindAllSubmatch(content, -1) {
			target := string(match[1])
			if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") ||
				strings.HasPrefix(target, "#") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			target = strings.SplitN(target, "#", 2)[0]
			if target == "" {
				continue
			}
			resolved := filepath.Clean(filepath.Join(filepath.Dir(file), filepath.FromSlash(target)))
			if _, err := os.Stat(resolved); err != nil {
				t.Errorf("%s: broken link %q (%s)", file, string(match[1]), resolved)
			}
		}
	}
}

func TestReleaseWorkflowRunsCompleteNonLiveGate(t *testing.T) {
	t.Parallel()
	path := filepath.Join(repositoryRoot(t), ".github", "workflows", "release.yml")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(content, &document); err != nil {
		t.Fatalf("release workflow is not valid YAML: %v", err)
	}

	required := []string{
		`test -z "$(gofmt -l .)"`,
		"go vet ./...",
		"go run honnef.co/go/tools/cmd/staticcheck@v0.6.1 ./...",
		"go test ./internal/api ./internal/cli ./internal/contracts",
		"go test -run TestDocumentationLinks ./internal/contracts",
		"go test -shuffle=on -count=1 ./...",
		"go test -race -shuffle=on -count=1 ./...",
		"needs: verify",
		"goos: [linux, darwin]",
		"goarch: [amd64, arm64]",
		`CGO_ENABLED: "0"`,
		"go build -trimpath",
	}
	for _, want := range required {
		if !bytes.Contains(content, []byte(want)) {
			t.Errorf("release workflow is missing non-live gate %q", want)
		}
	}

	action := regexp.MustCompile(`(?m)^\s*-\s+uses:\s+[^@\s]+@([^\s#]+)`)
	fullCommit := regexp.MustCompile(`^[0-9a-f]{40}$`)
	matches := action.FindAllSubmatch(content, -1)
	if len(matches) == 0 {
		t.Fatal("release workflow contains no actions")
	}
	for _, match := range matches {
		if !fullCommit.Match(match[1]) {
			t.Errorf("release action is not pinned to a full commit SHA: %s", match[0])
		}
	}
}
