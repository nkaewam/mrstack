package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/nkaewam/mrstack/internal/api"
	"github.com/nkaewam/mrstack/internal/cli"
	"github.com/nkaewam/mrstack/internal/stack"
)

func (h *Handler) doctor(ctx context.Context, inv cli.Invocation) (cli.Result, error) {
	rc, err := h.repository(ctx, inv.Globals.Remote, true)
	if err != nil {
		return cli.Result{}, err
	}
	gitResult, err := rc.repo.Git(ctx, "--version")
	if err != nil {
		return cli.Result{}, cli.Unavailable("git_unavailable", "cannot determine Git version", false)
	}
	glabResult, err := rc.repo.Runner.Run(ctx, rc.repo.Dir, "glab", "--version")
	if err != nil {
		return cli.Result{}, classifyGlab("determine glab version", err)
	}
	if _, err := rc.client.CurrentUser(ctx); err != nil {
		return cli.Result{}, classifyGlab("verify GitLab authentication", err)
	}
	version, err := rc.client.Version(ctx)
	if err != nil {
		if inv.Globals.GitLabMode == "auto" {
			return cli.Result{}, cli.Unavailable("server_mode_undetermined",
				"cannot detect GitLab version; pass an explicit --gitlab-mode", false)
		}
		version.Version = ""
	}
	var explicit stack.Mode
	if inv.Globals.GitLabMode != "auto" {
		explicit = stack.Mode(inv.Globals.GitLabMode)
	}
	mode, err := stack.SelectMode(version.Version, explicit)
	if err != nil {
		return cli.Result{}, cli.Invalid("invalid_arguments", err.Error())
	}
	env, _, err := h.envelope(inv.Name)
	if err != nil {
		return cli.Result{}, cli.Internal("cannot create response envelope", err)
	}
	detected := string(mode)
	serverVersion := version.Version
	env.Data["doctor"] = api.DoctorData{
		RequestedMode: inv.Globals.GitLabMode,
		DetectedMode:  &detected,
		EffectiveMode: detected,
		ServerVersion: &serverVersion,
		GitVersion:    strings.TrimSpace(string(gitResult.Stdout)),
		GlabVersion:   strings.TrimSpace(strings.Split(string(glabResult.Stdout), "\n")[0]),
		Capabilities: []api.Capability{
			{Name: "repository_context", Status: "verified", Summary: "Git repository and upstream remote resolved"},
			{Name: "gitlab_authentication", Status: "verified", Summary: "glab authenticated API request succeeded"},
			{Name: "stack_discovery", Status: "verified", Summary: "project and merge request APIs are available"},
			{Name: "atomic_push", Status: "unverified", Summary: "verified safely only during an explicitly confirmed publication"},
		},
	}
	human := fmt.Sprintf("Repository: %s\nRemote: %s (%s)\nGitLab: %s/%s\nMode: %s\nGit: %s\nGlab: %s",
		rc.repo.Dir, rc.remoteName, rc.selection, rc.fetch.Host, rc.fetch.Project, mode,
		strings.TrimSpace(string(gitResult.Stdout)),
		strings.TrimSpace(strings.Split(string(glabResult.Stdout), "\n")[0]))
	return result(env, human)
}
