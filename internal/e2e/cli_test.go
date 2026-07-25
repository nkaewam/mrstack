package e2e

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

var (
	binaryPath string
	buildErr   error
)

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "mrstack-e2e-")
	if err != nil {
		buildErr = err
		os.Exit(m.Run())
	}
	defer os.RemoveAll(tmp)
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		buildErr = errors.New("cannot locate repository")
		os.Exit(m.Run())
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	binaryPath = filepath.Join(tmp, "mrstack")
	cmd := exec.Command("go", "build", "-trimpath", "-o", binaryPath, "./cmd/mrstack")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOCACHE="+filepath.Join(tmp, "go-cache"))
	if output, err := cmd.CombinedOutput(); err != nil {
		buildErr = errors.New(err.Error() + ": " + string(output))
	}
	os.Exit(m.Run())
}

type commandResult struct {
	stdout, stderr string
	exit           int
}

func run(t *testing.T, dir string, args ...string) commandResult {
	t.Helper()
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	cmd := exec.Command(binaryPath, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	exit := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("run mrstack: %v", err)
		}
		exit = exitErr.ExitCode()
	}
	return commandResult{stdout: stdout.String(), stderr: stderr.String(), exit: exit}
}

func decodeOneDocument(t *testing.T, text string) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(text))
	var envelope map[string]any
	if err := decoder.Decode(&envelope); err != nil {
		t.Fatalf("decode machine output %q: %v", text, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		t.Fatalf("machine output contained more than one JSON document: %q", text)
	}
	if len(envelope) != 12 {
		t.Fatalf("machine envelope has %d keys, want 12: %v", len(envelope), envelope)
	}
	return envelope
}

func TestHelpAndVersionAreHumanOnly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, args := range [][]string{{"--help"}, {"help"}, {"--version"}} {
		got := run(t, dir, args...)
		if got.exit != 0 || got.stdout == "" || got.stderr != "" {
			t.Fatalf("%v: exit=%d stdout=%q stderr=%q", args, got.exit, got.stdout, got.stderr)
		}
	}
	got := run(t, dir, "--json", "--no-input", "--help")
	if got.exit != 2 {
		t.Fatalf("machine help exit=%d output=%s", got.exit, got.stdout)
	}
	decodeOneDocument(t, got.stdout)
}

func TestUnknownCommandIsExactMachineExitTwo(t *testing.T) {
	t.Parallel()
	got := run(t, t.TempDir(), "--json", "--no-input", "unknown-command")
	if got.exit != 2 || got.stderr != "" || strings.Contains(strings.ToLower(got.stdout), "prompt") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", got.exit, got.stdout, got.stderr)
	}
	env := decodeOneDocument(t, got.stdout)
	outcome := env["outcome"].(map[string]any)
	if outcome["class"] != "invalid_input" || outcome["code"] != "unknown_command" ||
		int(outcome["exit_code"].(float64)) != 2 {
		t.Fatalf("outcome=%v", outcome)
	}
	if env["disposition"] != nil || env["error"] == nil {
		t.Fatalf("failure envelope made partial authoritative claim: %v", env)
	}
}

func TestUnavailableOutsideRepositoryIsMachineExitThree(t *testing.T) {
	t.Parallel()
	got := run(t, t.TempDir(), "doctor", "--json", "--no-input", "--remote=origin")
	if got.exit != 3 || got.stderr != "" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", got.exit, got.stdout, got.stderr)
	}
	env := decodeOneDocument(t, got.stdout)
	outcome := env["outcome"].(map[string]any)
	if outcome["class"] != "unavailable" || outcome["code"] != "not_git_repository" {
		t.Fatalf("outcome=%v", outcome)
	}
}

func TestGlobalOptionOrderAndEqualsFormsHaveSameFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	forms := [][]string{
		{"--json", "--no-input", "--remote", "origin", "doctor"},
		{"doctor", "--remote=origin", "--no-input", "--json"},
	}
	for _, args := range forms {
		got := run(t, dir, args...)
		if got.exit != 3 {
			t.Fatalf("%v exit=%d stdout=%s stderr=%s", args, got.exit, got.stdout, got.stderr)
		}
		env := decodeOneDocument(t, got.stdout)
		if env["command"].(map[string]any)["name"] != "doctor" {
			t.Fatalf("%v command=%v", args, env["command"])
		}
	}
}

func TestCLIArgumentErrorMatrix(t *testing.T) {
	t.Parallel()
	tests := [][]string{
		{"--json", "check"},
		{"--json", "--no-input", "--json", "check"},
		{"--json", "--no-input", "check", "one", "two"},
		{"--json", "--no-input", "restack", "--snapshot", "s"},
		{"--json", "--no-input", "--yes", "restack"},
		{"--json", "--no-input", "--yes", "restack", "--snapshot", "s", "--plan", "p"},
		{"--json", "--no-input", "restack", "plan", "--snapshot", "s"},
		{"--json", "--no-input", "restack", "plan", "--snapshot", "s", "--layer-boundary", "1=short"},
		{"--json", "--no-input", "restack", "continue", "--session", "s", "--drop-current", "--keep-empty"},
		{"--json", "--no-input", "ci", "logs", "--pipeline", "1"},
		{"--json", "--no-input", "ci", "logs", "--pipeline", "one", "--job", "1"},
		{"--json", "--no-input", "ci", "logs", "--pipeline", "1", "--job", "1", "--max-bytes", "4194305"},
		{"--json", "--no-input", "history", "--limit", "201"},
		{"--json", "--no-input", "--gitlab-mode=future", "check"},
	}
	for index, args := range tests {
		got := run(t, t.TempDir(), args...)
		if got.exit != 2 {
			t.Errorf("case %d %v: exit=%d stdout=%s stderr=%s", index, args, got.exit, got.stdout, got.stderr)
			continue
		}
		if got.stdout == "" {
			t.Errorf("case %d %v: exit 2 produced no machine stdout; stderr=%s", index, args, got.stderr)
			continue
		}
		decodeOneDocument(t, got.stdout)
	}
}

func TestCILogsAcceptsTwentyJobsButRejectsTwentyOne(t *testing.T) {
	t.Parallel()
	base := []string{"--json", "--no-input", "ci", "logs", "--pipeline", "1"}
	twenty := append([]string(nil), base...)
	for i := 1; i <= 20; i++ {
		twenty = append(twenty, "--job", strconv.Itoa(i))
	}
	got := run(t, t.TempDir(), twenty...)
	if got.exit != 3 { // Parsing succeeded and repository resolution failed.
		t.Fatalf("20 jobs should parse, exit=%d stdout=%s", got.exit, got.stdout)
	}
	twentyOne := append(append([]string(nil), twenty...), "--job", "21")
	got = run(t, t.TempDir(), twentyOne...)
	if got.exit != 2 {
		t.Fatalf("21 jobs should fail input validation, exit=%d stdout=%s", got.exit, got.stdout)
	}
}

func TestMetacharacterSelectorIsLiteralAndCannotCreateFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	marker := filepath.Join(dir, "PWNED")
	selector := "feature;touch " + marker
	got := run(t, dir, "--json", "--no-input", "check", selector)
	if got.exit != 3 {
		t.Fatalf("exit=%d stdout=%s stderr=%s", got.exit, got.stdout, got.stderr)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("selector was evaluated by a shell: %v", err)
	}
}
