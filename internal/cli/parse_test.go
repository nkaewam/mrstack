package cli

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseEveryDocumentedCommand(t *testing.T) {
	t.Parallel()
	shaA := strings.Repeat("a", 40)
	shaB := strings.Repeat("b", 64)
	tests := []struct {
		name  string
		args  []string
		want  CommandName
		check func(*testing.T, Invocation)
	}{
		{"doctor", []string{"doctor"}, CommandDoctor, nil},
		{"check current", []string{"check"}, CommandCheck, nil},
		{"check selector", []string{"check", "feature/a"}, CommandCheck, func(t *testing.T, i Invocation) {
			equal(t, i.Selector.Value, "feature/a")
		}},
		{"check stack", []string{"check", "--stack", "stk_1"}, CommandCheck, func(t *testing.T, i Invocation) {
			equal(t, i.Selector.StackID, "stk_1")
		}},
		{"restack", []string{"restack", "42", "--snapshot", "snap_1"}, CommandRestackStart, func(t *testing.T, i Invocation) {
			equal(t, i.SnapshotID, "snap_1")
			equal(t, i.Selector.Value, "42")
		}},
		{"planned restack", []string{"restack", "--plan=plan_1", "--allow-signature-loss"}, CommandRestackStart, func(t *testing.T, i Invocation) {
			equal(t, i.PlanID, "plan_1")
			if !i.AllowSignatureLoss {
				t.Fatal("signature loss flag was not parsed")
			}
		}},
		{"restack plan", []string{"restack", "plan", "topic", "--snapshot=snap_1", "--layer-boundary", "43=" + shaA, "--layer-boundary=44=" + shaB}, CommandRestackPlan, func(t *testing.T, i Invocation) {
			equal(t, i.Selector.Value, "topic")
			equal(t, i.Boundaries, []LayerBoundary{{MR: "43", SHA: shaA}, {MR: "44", SHA: shaB}})
		}},
		{"continue", []string{"restack", "continue", "--session", "rs_1", "--drop-current"}, CommandRestackContinue, func(t *testing.T, i Invocation) {
			if !i.DropCurrent {
				t.Fatal("drop-current flag was not parsed")
			}
		}},
		{"continue plain", []string{"restack", "continue", "--session=rs_1"}, CommandRestackContinue, nil},
		{"abort", []string{"restack", "abort", "--session", "rs_1"}, CommandRestackAbort, nil},
		{"recover", []string{"restack", "recover", "--session", "rs_1"}, CommandRestackRecover, nil},
		{"abandon", []string{"restack", "abandon", "--session", "rs_1", "--accept-current-remote"}, CommandRestackAbandon, nil},
		{"ci logs", []string{"ci", "logs", "--pipeline", "9001", "--job", "1", "--job=2", "--max-bytes", "99"}, CommandCILogs, func(t *testing.T, i Invocation) {
			equal(t, i.PipelineID, "9001")
			equal(t, i.JobIDs, []string{"1", "2"})
			equal(t, i.MaxBytes, int64(99))
		}},
		{"history", []string{"history", "topic", "--limit", "200", "--cursor=opaque"}, CommandHistoryShow, func(t *testing.T, i Invocation) {
			equal(t, i.Limit, 200)
			equal(t, i.Cursor, "opaque")
		}},
		{"history alias", []string{"history", "alias", "--stack", "stk_1", "my stack"}, CommandHistoryAlias, func(t *testing.T, i Invocation) {
			equal(t, i.Alias, "my stack")
		}},
		{"history clear", []string{"history", "alias", "--clear", "--stack=stk_1"}, CommandHistoryAlias, func(t *testing.T, i Invocation) {
			if !i.ClearAlias {
				t.Fatal("clear flag was not parsed")
			}
		}},
		{"history prune", []string{"history", "prune", "--before", "30d", "--stack", "stk_1"}, CommandHistoryPrune, func(t *testing.T, i Invocation) {
			equal(t, i.Before, "30d")
		}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Parse(tt.args)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			equal(t, got.Name, tt.want)
			if tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}

func TestGlobalsAreAcceptedAnywhereAndRemovedBeforeCommandParsing(t *testing.T) {
	t.Parallel()
	permutations := [][]string{
		{"--json", "--no-input", "--yes", "--remote", "up(stream)$x", "--gitlab-mode", "legacy", "restack", "--snapshot", "snap;1"},
		{"restack", "--json", "--snapshot", "snap;1", "--remote=up(stream)$x", "--no-input", "--gitlab-mode=legacy", "--yes"},
		{"restack", "--snapshot", "snap;1", "--yes", "--gitlab-mode", "legacy", "--json", "--remote", "up(stream)$x", "--no-input"},
	}
	for _, args := range permutations {
		got, err := Parse(args)
		if err != nil {
			t.Fatalf("Parse(%q) error = %v", args, err)
		}
		equal(t, got.Name, CommandRestackStart)
		equal(t, got.SnapshotID, "snap;1")
		equal(t, got.Globals.Remote, "up(stream)$x")
		equal(t, got.Globals.GitLabMode, "legacy")
		if !got.Machine() || !got.Globals.Yes {
			t.Fatalf("globals not preserved: %#v", got.Globals)
		}
	}
}

func TestDefaults(t *testing.T) {
	t.Parallel()
	got, err := Parse([]string{"ci", "logs", "--pipeline", "1", "--job", "2"})
	if err != nil {
		t.Fatal(err)
	}
	equal(t, got.MaxBytes, int64(524288))
	equal(t, got.Globals.GitLabMode, "auto")

	got, err = Parse([]string{"history"})
	if err != nil {
		t.Fatal(err)
	}
	equal(t, got.Limit, 50)
}

func TestHelpIsHumanAndAvailableAtCommandScope(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"--help"},
		{"help"},
		{"doctor", "--help"},
		{"check", "-h"},
		{"restack", "continue", "--help"},
	} {
		got, err := Parse(args)
		if err != nil {
			t.Errorf("Parse(%q) error = %v", args, err)
			continue
		}
		if !got.Help {
			t.Errorf("Parse(%q) did not request help", args)
		}
	}
}

func TestInvalidInvocations(t *testing.T) {
	t.Parallel()
	twentyOneJobs := []string{"ci", "logs", "--pipeline", "1"}
	for n := 0; n < 21; n++ {
		twentyOneJobs = append(twentyOneJobs, "--job", "2")
	}

	tests := []struct {
		name string
		args []string
		code string
	}{
		{"empty", nil, "invalid_arguments"},
		{"unknown", []string{"wat"}, "unknown_command"},
		{"unknown ci subcommand", []string{"ci", "latest"}, "unknown_command"},
		{"machine pair json", []string{"doctor", "--json"}, "invalid_arguments"},
		{"machine pair no input", []string{"doctor", "--no-input"}, "invalid_arguments"},
		{"invalid mode", []string{"doctor", "--gitlab-mode", "future"}, "invalid_arguments"},
		{"duplicate global", []string{"doctor", "--remote", "a", "--remote=b"}, "invalid_arguments"},
		{"doctor args", []string{"doctor", "extra"}, "invalid_arguments"},
		{"selector xor stack", []string{"check", "42", "--stack", "stk"}, "invalid_arguments"},
		{"too many selectors", []string{"check", "a", "b"}, "invalid_arguments"},
		{"start needs snapshot", []string{"restack"}, "invalid_arguments"},
		{"plan xor snapshot", []string{"restack", "--plan", "p", "--snapshot", "s"}, "invalid_arguments"},
		{"plan xor selector", []string{"restack", "branch", "--plan", "p"}, "invalid_arguments"},
		{"plan boundary required", []string{"restack", "plan", "--snapshot", "s"}, "invalid_arguments"},
		{"bad boundary shape", []string{"restack", "plan", "--snapshot", "s", "--layer-boundary", "42"}, "invalid_arguments"},
		{"abbreviated boundary", []string{"restack", "plan", "--snapshot", "s", "--layer-boundary", "42=abc"}, "invalid_arguments"},
		{"zero boundary MR", []string{"restack", "plan", "--snapshot", "s", "--layer-boundary", "0=" + strings.Repeat("a", 40)}, "invalid_arguments"},
		{"session required", []string{"restack", "continue"}, "invalid_arguments"},
		{"continue choice xor", []string{"restack", "continue", "--session", "s", "--drop-current", "--keep-empty"}, "invalid_arguments"},
		{"abort rejects choice", []string{"restack", "abort", "--session", "s", "--drop-current"}, "invalid_arguments"},
		{"abandon accept required", []string{"restack", "abandon", "--session", "s"}, "invalid_arguments"},
		{"abandon no input", []string{"restack", "abandon", "--session", "s", "--accept-current-remote", "--json", "--no-input"}, "invalid_arguments"},
		{"pipeline required", []string{"ci", "logs", "--job", "1"}, "invalid_arguments"},
		{"job required", []string{"ci", "logs", "--pipeline", "1"}, "invalid_arguments"},
		{"decimal pipeline", []string{"ci", "logs", "--pipeline", "latest", "--job", "1"}, "invalid_arguments"},
		{"canonical decimal pipeline", []string{"ci", "logs", "--pipeline", "01", "--job", "1"}, "invalid_arguments"},
		{"decimal job", []string{"ci", "logs", "--pipeline", "1", "--job", "-2"}, "invalid_arguments"},
		{"too many jobs", twentyOneJobs, "invalid_arguments"},
		{"max bytes zero", []string{"ci", "logs", "--pipeline", "1", "--job", "2", "--max-bytes", "0"}, "invalid_arguments"},
		{"max bytes high", []string{"ci", "logs", "--pipeline", "1", "--job", "2", "--max-bytes", "4194305"}, "invalid_arguments"},
		{"history limit zero", []string{"history", "--limit", "0"}, "invalid_arguments"},
		{"history limit high", []string{"history", "--limit", "201"}, "invalid_arguments"},
		{"alias value xor clear", []string{"history", "alias", "--stack", "s", "x", "--clear"}, "invalid_arguments"},
		{"alias missing value", []string{"history", "alias", "--stack", "s"}, "invalid_arguments"},
		{"prune before required", []string{"history", "prune"}, "invalid_arguments"},
		{"prune before malformed", []string{"history", "prune", "--before", "someday"}, "invalid_arguments"},
		{"machine restack yes", []string{"restack", "--snapshot", "s", "--json", "--no-input"}, "invalid_arguments"},
		{"machine prune yes", []string{"history", "prune", "--before", "1d", "--json", "--no-input"}, "invalid_arguments"},
		{"human-only help machine", []string{"--help", "--json", "--no-input"}, "invalid_arguments"},
		{"empty option value", []string{"check", "--stack="}, "invalid_arguments"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse(tt.args)
			if err == nil {
				t.Fatal("Parse() unexpectedly succeeded")
			}
			var exitErr *ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("error type = %T", err)
			}
			equal(t, exitErr.Class, "invalid_input")
			equal(t, exitErr.Code, tt.code)
		})
	}
}

func TestMachineMutationsAcceptYesAndReadOnlyCommandsDoNotRequireIt(t *testing.T) {
	t.Parallel()
	sha := strings.Repeat("a", 40)
	valid := [][]string{
		{"restack", "--snapshot", "s", "--json", "--no-input", "--yes"},
		{"restack", "--plan", "p", "--json", "--no-input", "--yes"},
		{"restack", "continue", "--session", "s", "--json", "--no-input", "--yes"},
		{"restack", "abort", "--session", "s", "--json", "--no-input", "--yes"},
		{"history", "prune", "--before", "1d", "--json", "--no-input", "--yes"},
		{"doctor", "--json", "--no-input"},
		{"check", "--json", "--no-input"},
		{"restack", "plan", "--snapshot", "s", "--layer-boundary", "1=" + sha, "--json", "--no-input"},
		{"restack", "recover", "--session", "s", "--json", "--no-input"},
		{"history", "alias", "--stack", "s", "--clear", "--json", "--no-input"},
	}
	for _, args := range valid {
		if _, err := Parse(args); err != nil {
			t.Errorf("Parse(%q) error = %v", args, err)
		}
	}
}

func TestMetacharactersRemainLiteralArgvData(t *testing.T) {
	t.Parallel()
	literals := []string{"$(touch /tmp/no)", "semi;colon", "pipe|value", "space value", "`id`", "quote'\""}
	for _, literal := range literals {
		got, err := Parse([]string{"check", literal})
		if err != nil {
			t.Fatalf("Parse(%q) error = %v", literal, err)
		}
		equal(t, got.Selector.Value, literal)
	}
}

func TestDuplicateLocalOptions(t *testing.T) {
	t.Parallel()
	tests := [][]string{
		{"history", "--limit", "50", "--limit=50"},
		{"history", "--cursor", "x", "--cursor", "y"},
		{"ci", "logs", "--pipeline", "1", "--job", "2", "--max-bytes", "524288", "--max-bytes=524288"},
		{"restack", "--snapshot", "a", "--snapshot=b"},
	}
	for _, args := range tests {
		if _, err := Parse(args); err == nil {
			t.Errorf("Parse(%q) unexpectedly succeeded", args)
		}
	}
}

func equal[T any](t *testing.T, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestErrorMessagesNeverJoinArgvAsShell(t *testing.T) {
	t.Parallel()
	_, err := Parse([]string{"doctor", "$(touch SHOULD_NOT_EXIST)"})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "sh -c") {
		t.Fatalf("shell entered error path: %v", err)
	}
}
