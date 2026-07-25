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
	versionDetected := err == nil
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
	journalCapability := api.Capability{
		Name: "sqlite_journal", Status: "unsupported",
		Summary: "The SQLite journal could not be opened in WAL mode.",
	}
	if j, openErr := h.openJournal(rc.repo.Dir); openErr == nil {
		wal, walErr := j.WALMode(ctx)
		closeErr := j.Close()
		if walErr == nil && closeErr == nil && wal {
			journalCapability.Status = "verified"
			journalCapability.Summary = "The SQLite journal is available in WAL mode."
		}
	}
	env, _, err := h.envelope(inv.Name)
	if err != nil {
		return cli.Result{}, cli.Internal("cannot create response envelope", err)
	}
	var detectedMode, serverVersion *string
	serverModeCapability := api.Capability{
		Name: "server_mode", Status: "unverified",
		Summary: "GitLab server mode could not be detected; the explicit mode override is in use.",
	}
	if versionDetected {
		detected := string(mode)
		server := version.Version
		detectedMode = &detected
		serverVersion = &server
		serverModeCapability.Status = "verified"
		serverModeCapability.Summary = "GitLab server mode was detected."
	}
	env.Data["doctor"] = api.DoctorData{
		RequestedMode: inv.Globals.GitLabMode,
		DetectedMode:  detectedMode,
		EffectiveMode: string(mode),
		ServerVersion: serverVersion,
		GitVersion:    strings.TrimSpace(string(gitResult.Stdout)),
		GlabVersion:   strings.TrimSpace(strings.Split(string(glabResult.Stdout), "\n")[0]),
		Capabilities: []api.Capability{
			{Name: "repository_context", Status: "verified", Summary: "Git repository and selected remote resolve to the authenticated GitLab project."},
			{Name: "git", Status: "verified", Summary: "Git is available."},
			{Name: "glab", Status: "verified", Summary: "glab is available."},
			{Name: "gitlab_auth", Status: "verified", Summary: "GitLab authentication succeeded."},
			serverModeCapability,
			{Name: "atomic_push", Status: "unverified", Summary: "Atomic push behavior is verified safely only during a real publication."},
			{Name: "target_update", Status: "unverified", Summary: "Target-update permission is verified safely only during a real target update."},
			journalCapability,
		},
	}
	human := fmt.Sprintf("Repository: %s\nRemote: %s (%s)\nGitLab: %s/%s\nMode: %s\nGit: %s\nGlab: %s",
		rc.repo.Dir, rc.remoteName, rc.selection, rc.fetch.Host, rc.fetch.Project, mode,
		strings.TrimSpace(string(gitResult.Stdout)),
		strings.TrimSpace(strings.Split(string(glabResult.Stdout), "\n")[0]))
	return result(env, human)
}
