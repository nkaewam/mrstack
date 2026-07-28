package app

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"testing"
)

const testStackName = "stk"

func testDirs(t *testing.T) (stateDir, stacksDir string) {
	t.Helper()
	root := t.TempDir()
	return filepath.Join(root, "state"), filepath.Join(root, "stacks")
}

func registerNamedStack(t *testing.T, handler *Handler, name string, iids ...int) {
	t.Helper()
	if run := runMachine(t, handler, "stack", "create", name); run.exit != 0 {
		t.Fatalf("stack create %q failed: %s", name, run.stdout)
	}
	args := []string{"stack", "add", name}
	for _, iid := range iids {
		args = append(args, strconv.Itoa(iid))
	}
	if run := runMachine(t, handler, args...); run.exit != 0 {
		t.Fatalf("stack add %q failed: %s", name, run.stdout)
	}
}

func addPerMREndpoints(responses map[string]json.RawMessage, payload []map[string]any) {
	for _, mr := range payload {
		iid, ok := mr["iid"].(int)
		if !ok {
			iid = int(mr["iid"].(float64))
		}
		body, err := json.Marshal(mr)
		if err != nil {
			panic(err)
		}
		responses[fmt.Sprintf("/projects/42/merge_requests/%d", iid)] = body
	}
}

func glabProjectResponses(pipelineRequired bool) map[string]json.RawMessage {
	pipeline := "false"
	if pipelineRequired {
		pipeline = "true"
	}
	return map[string]json.RawMessage{
		"/version": json.RawMessage(`{"version":"18.11.2"}`),
		"/projects/group%2Fproject": json.RawMessage(fmt.Sprintf(`{
			"id":42,"path_with_namespace":"group/project",
			"web_url":"https://gitlab.example/group/project","default_branch":"main",
			"only_allow_merge_if_pipeline_succeeds":%s
		}`, pipeline)),
	}
}
