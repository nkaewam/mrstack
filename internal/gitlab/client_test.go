package gitlab

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/nkaewam/mrstack/internal/gitexec"
)

type fakeRunner struct {
	stdout []byte
	calls  [][]string
	err    error
}

func (f *fakeRunner) Run(_ context.Context, _ string, name string, args ...string) (gitexec.Result, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	return gitexec.Result{Stdout: append([]byte(nil), f.stdout...)}, f.err
}

func TestGlabTransportUsesLiteralArgvAndSanitizedProjectEndpoint(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{stdout: []byte(`{"id":1,"path_with_namespace":"team/a b","default_branch":"main"}`)}
	client := Client{Runner: runner, Dir: "/repo", Host: "gitlab.example.com"}
	project, err := client.Project(context.Background(), "team/a b")
	if err != nil {
		t.Fatal(err)
	}
	if project.PathWithNamespace != "team/a b" {
		t.Fatalf("bad project: %#v", project)
	}
	if project.OnlyAllowMergeIfPipeline != nil {
		t.Fatal("absent CI policy was fabricated as observable")
	}
	want := []string{"glab", "api", "--hostname", "gitlab.example.com", "/projects/team%2Fa%20b"}
	if !reflect.DeepEqual(runner.calls[0], want) {
		t.Fatalf("got %#v want %#v", runner.calls[0], want)
	}
}

func TestProjectPolicyAndMRProjectIDsPreserveProviderObservability(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{stdout: []byte(`{
		"id":1,"path_with_namespace":"team/repo","default_branch":"main",
		"only_allow_merge_if_pipeline_succeeds":false
	}`)}
	client := Client{Runner: runner}
	project, err := client.Project(context.Background(), "team/repo")
	if err != nil {
		t.Fatal(err)
	}
	if project.OnlyAllowMergeIfPipeline == nil || *project.OnlyAllowMergeIfPipeline {
		t.Fatalf("observable optional policy lost: %#v", project)
	}
	runner.stdout = []byte(`{
		"iid":7,"state":"opened","source_project_id":2,"target_project_id":1,
		"source_branch":"fork","target_branch":"main"
	}`)
	mr, err := client.MergeRequest(context.Background(), "1", 7)
	if err != nil {
		t.Fatal(err)
	}
	if mr.SourceProjectID.String() != "2" || mr.TargetProjectID.String() != "1" {
		t.Fatalf("cross-project identities lost: %#v", mr)
	}
}

func TestTargetUpdateDoesNotUseShell(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{stdout: []byte(`{"iid":7}`)}
	client := Client{Runner: runner}
	target := "feature/a; touch /tmp/pwned"
	if err := client.UpdateTarget(context.Background(), "123", 7, target); err != nil {
		t.Fatal(err)
	}
	wantTail := []string{"/projects/123/merge_requests/7", "--method", "PUT", "--field", "target_branch=" + target}
	got := runner.calls[0]
	if !reflect.DeepEqual(got[len(got)-len(wantTail):], wantTail) {
		t.Fatalf("literal argv mismatch: %#v", got)
	}
}

func TestIDsAreValidatedBeforeGlab(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{}
	client := Client{Runner: runner}
	if _, err := client.PipelineJobs(context.Background(), "1;bad", "2"); err == nil {
		t.Fatal("unsafe project ID accepted")
	}
	if _, err := client.JobTrace(context.Background(), "1", "-2"); err == nil {
		t.Fatal("unsafe job ID accepted")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("glab called for invalid input: %#v", runner.calls)
	}
	if _, err := client.MergeRequests(context.Background(), "1", "future"); err == nil {
		t.Fatal("unsafe merge request state accepted")
	}
	if _, err := client.Pipeline(context.Background(), "1", "bad"); err == nil {
		t.Fatal("unsafe pipeline ID accepted")
	}
	if _, err := client.PipelineMergeRequests(context.Background(), "1", "-2"); err == nil {
		t.Fatal("unsafe pipeline association ID accepted")
	}
	if _, err := client.Commit(context.Background(), "1", "not-an-object-id"); err == nil {
		t.Fatal("unsafe commit ID accepted")
	}
}

func TestPipelineEvidenceTransportUsesTypedEndpoints(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{stdout: []byte(`{"id":9,"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","source":"merge_request_event"}`)}
	client := Client{Runner: runner}
	pipeline, err := client.Pipeline(context.Background(), "42", "9")
	if err != nil {
		t.Fatal(err)
	}
	if pipeline.ID.String() != "9" || pipeline.Source != "merge_request_event" {
		t.Fatalf("pipeline evidence lost: %#v", pipeline)
	}
	runner.stdout = []byte(`[{"iid":7}]`)
	associated, err := client.PipelineMergeRequests(context.Background(), "42", "9")
	if err != nil {
		t.Fatal(err)
	}
	if len(associated) != 1 || associated[0].IID != 7 {
		t.Fatalf("association evidence lost: %#v", associated)
	}
	runner.stdout = []byte(`{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","parent_ids":["bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","cccccccccccccccccccccccccccccccccccccccc"]}`)
	commit, err := client.Commit(context.Background(), "42", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	if len(commit.ParentIDs) != 2 {
		t.Fatalf("parent evidence lost: %#v", commit)
	}
	wantEndpoints := []string{
		"/projects/42/pipelines/9",
		"/projects/42/pipelines/9/merge_requests",
		"/projects/42/repository/commits/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	for i, endpoint := range wantEndpoints {
		if !reflect.DeepEqual(runner.calls[i], []string{"glab", "api", endpoint}) {
			t.Fatalf("call %d=%#v want endpoint %q", i, runner.calls[i], endpoint)
		}
	}
}

func TestBoundLogsBudgetTailUTF8AndOrdering(t *testing.T) {
	t.Parallel()
	traces := [][]byte{
		[]byte("0123456789"),
		{'a', 'b', 0xff, 'd', 'e', 'f', 'g'},
		[]byte("xyz"),
	}
	logs, err := BoundLogs([]string{"9", "2", "7"}, traces, 8)
	if err != nil {
		t.Fatal(err)
	}
	// Allocations are 3, 3, 2.
	if logs[0].Text != "789" || !logs[0].Truncated || logs[0].ReturnedBytes != 3 {
		t.Fatalf("first log: %#v", logs[0])
	}
	if logs[1].Text != "efg" || !logs[1].Truncated || logs[1].InvalidUTF8Replaced {
		t.Fatalf("second log tail: %#v", logs[1])
	}
	if logs[2].Text != "yz" || !logs[2].Truncated {
		t.Fatalf("third log: %#v", logs[2])
	}
}

func TestBoundLogsReplacesInvalidUTF8InRetainedTail(t *testing.T) {
	t.Parallel()
	logs, err := BoundLogs([]string{"1"}, [][]byte{{'a', 0xff, 'b'}}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !logs[0].InvalidUTF8Replaced || !strings.Contains(logs[0].Text, "\uFFFD") {
		t.Fatalf("replacement not reported: %#v", logs[0])
	}
}

func TestBoundLogsLimits(t *testing.T) {
	t.Parallel()
	ids, traces := make([]string, MaxLogJobs+1), make([][]byte, MaxLogJobs+1)
	if _, err := BoundLogs(ids, traces, DefaultLogBudget); err == nil {
		t.Fatal("accepted too many jobs")
	}
	if _, err := BoundLogs([]string{"1"}, [][]byte{{1}}, MaxLogBudget+1); err == nil {
		t.Fatal("accepted oversized budget")
	}
}

func FuzzBoundLogsNeverExceedsBudget(f *testing.F) {
	f.Add([]byte("first"), []byte("second"), 7)
	f.Fuzz(func(t *testing.T, a, b []byte, budget int) {
		if budget <= 0 || budget > 1024 {
			return
		}
		logs, err := BoundLogs([]string{"1", "2"}, [][]byte{a, b}, budget)
		if err != nil {
			t.Fatal(err)
		}
		total := 0
		for _, log := range logs {
			total += log.ReturnedBytes
		}
		if total > budget {
			t.Fatalf("returned %d > budget %d", total, budget)
		}
	})
}

// mrJSON builds a minimal MR JSON object with the given iid.
func mrJSON(iid int) string {
	return `{"iid":` + itoa(iid) + `,"state":"opened","source_branch":"f` + itoa(iid) +
		`","target_branch":"main","sha":"` + strings.Repeat("a", 40) + `"}`
}

func itoa(n int) string {
	// avoid pulling strconv into a tiny helper used only by test fixtures
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestMergeRequestsDecodesSingleJSONArray covers the common single-page case
// (and the shape the existing fake fixtures return).
func TestMergeRequestsDecodesSingleJSONArray(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{stdout: []byte("[" + mrJSON(1) + "," + mrJSON(2) + "]")}
	client := Client{Runner: runner}
	mrs, err := client.MergeRequests(context.Background(), "42", "all")
	if err != nil {
		t.Fatal(err)
	}
	if len(mrs) != 2 || mrs[0].IID != 1 || mrs[1].IID != 2 {
		t.Fatalf("decoded %#v", mrs)
	}
}

// TestMergeRequestsDecodesConcatenatedArrays reproduces the reported bug:
// glab api --paginate in default JSON mode concatenates raw response bodies
// back-to-back with no separator, producing "[{...p1}][{...p2}]" which is not a
// valid single JSON document. The streaming decoder must flatten it.
func TestMergeRequestsDecodesConcatenatedArrays(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{stdout: []byte(
		"[" + mrJSON(1) + "," + mrJSON(2) + "][" + mrJSON(3) + "," + mrJSON(4) + "]",
	)}
	client := Client{Runner: runner}
	mrs, err := client.MergeRequests(context.Background(), "42", "all")
	if err != nil {
		t.Fatalf("multi-page concatenated arrays must decode, got: %v", err)
	}
	if len(mrs) != 4 {
		t.Fatalf("expected 4 MRs across concatenated pages, got %d", len(mrs))
	}
	for i, want := range []int{1, 2, 3, 4} {
		if mrs[i].IID != want {
			t.Fatalf("mrs[%d].IID=%d want %d", i, mrs[i].IID, want)
		}
	}
}

// TestMergeRequestsDecodesNDJSON covers glab's --output ndjson stream, where
// each array element is emitted as one JSON object per line.
func TestMergeRequestsDecodesNDJSON(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{stdout: []byte(
		mrJSON(1) + "\n" + mrJSON(2) + "\n" + mrJSON(3) + "\n",
	)}
	client := Client{Runner: runner}
	mrs, err := client.MergeRequests(context.Background(), "42", "all")
	if err != nil {
		t.Fatalf("ndjson stream must decode, got: %v", err)
	}
	if len(mrs) != 3 {
		t.Fatalf("expected 3 MRs from ndjson, got %d", len(mrs))
	}
	for i, want := range []int{1, 2, 3} {
		if mrs[i].IID != want {
			t.Fatalf("mrs[%d].IID=%d want %d", i, mrs[i].IID, want)
		}
	}
}

func TestMergeRequestsEmptyArrayDecodes(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{stdout: []byte("[]")}
	client := Client{Runner: runner}
	mrs, err := client.MergeRequests(context.Background(), "42", "all")
	if err != nil {
		t.Fatal(err)
	}
	if len(mrs) != 0 {
		t.Fatalf("expected empty result, got %#v", mrs)
	}
}

func TestMergeRequestsRejectsMalformedBody(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{stdout: []byte("not-json-at-all")}
	client := Client{Runner: runner}
	if _, err := client.MergeRequests(context.Background(), "42", "all"); err == nil {
		t.Fatal("malformed body must produce a decode error, not a silent success")
	}
}
