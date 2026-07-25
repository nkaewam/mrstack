//go:build livegitlab

// Package livecontract contains opt-in checks for provider behavior that a
// fake glab cannot establish. It is never selected by ordinary go test ./....
package livecontract

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/nkaewam/mrstack/internal/gitexec"
	"github.com/nkaewam/mrstack/internal/gitlab"
)

func TestAuthenticatedGitLabReadContract(t *testing.T) {
	host := os.Getenv("MRSTACK_CONTRACT_HOST")
	projectPath := os.Getenv("MRSTACK_CONTRACT_PROJECT")
	if host == "" || projectPath == "" || os.Getenv("MRSTACK_LIVE_CONTRACT") != "1" {
		t.Skip("set MRSTACK_LIVE_CONTRACT=1, MRSTACK_CONTRACT_HOST, and MRSTACK_CONTRACT_PROJECT")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := gitlab.Client{Runner: gitexec.ExecRunner{}, Dir: ".", Host: host}
	version, err := client.Version(ctx)
	if err != nil || version.Version == "" {
		t.Fatalf("GitLab /version contract: version=%q err=%v", version.Version, err)
	}
	user, err := client.CurrentUser(ctx)
	if err != nil || user.Username == "" || user.ID.String() == "" {
		t.Fatalf("GitLab authenticated user contract: username=%q id=%q err=%v", user.Username, user.ID, err)
	}
	project, err := client.Project(ctx, projectPath)
	if err != nil || project.ID.String() == "" || project.DefaultBranch == "" ||
		project.PathWithNamespace != projectPath {
		t.Fatalf("GitLab project contract: id=%q path=%q default=%q err=%v",
			project.ID, project.PathWithNamespace, project.DefaultBranch, err)
	}
}
