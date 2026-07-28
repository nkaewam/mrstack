package app

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/nkaewam/mrstack/internal/api"
	"github.com/nkaewam/mrstack/internal/cli"
	"github.com/nkaewam/mrstack/internal/gitexec"
	"github.com/nkaewam/mrstack/internal/gitlab"
	"github.com/nkaewam/mrstack/internal/stackstore"
)

func (h *Handler) view(ctx context.Context, inv cli.Invocation) (cli.Result, error) {
	store, err := h.stackStore()
	if err != nil {
		return cli.Result{}, err
	}
	all, err := store.List()
	if err != nil {
		return cli.Result{}, cli.Unavailable("stackstore_unavailable",
			"cannot list stacks: "+err.Error(), false)
	}
	if inv.StackName != "" {
		filtered := make([]stackstore.Stack, 0, 1)
		for _, s := range all {
			if s.Name == inv.StackName {
				filtered = append(filtered, s)
			}
		}
		all = filtered
	}

	var viewStacks []api.ViewStack
	var live bool
	if inv.AllStacks {
		live = inv.Refresh
		viewStacks = h.viewStacksCrossProject(ctx, all, live)
	} else {
		// Current-repo view is always live: it fetches member titles and status.
		live = true
		viewStacks, err = h.viewStacksForRepo(ctx, inv, all)
		if err != nil {
			return cli.Result{}, err
		}
	}

	out := api.ViewData{Stacks: viewStacks, Live: live}
	env, _, err := h.envelope(inv.Name)
	if err != nil {
		return cli.Result{}, cli.Internal("cannot create response envelope", err)
	}
	env.Data["view"] = out
	return result(env, renderView(out))
}

// viewStacksForRepo live-fetches the stacks bound to the current repository's
// project. Stacks belonging to other projects are silently excluded.
func (h *Handler) viewStacksForRepo(ctx context.Context, inv cli.Invocation,
	stacks []stackstore.Stack) ([]api.ViewStack, error) {
	rc, err := h.repository(ctx, inv.Globals.Remote, true)
	if err != nil {
		return nil, err
	}
	var out []api.ViewStack
	for _, s := range stacks {
		if s.Host != rc.fetch.Host || s.Project != rc.fetch.Project {
			continue
		}
		out = append(out, h.liveViewStack(ctx, rc.client, rc.project.ID.String(),
			rc.project.DefaultBranch, s))
	}
	return out, nil
}

// viewStacksCrossProject renders every stack. When live is false only the
// stored membership is reported; when true each distinct project is resolved
// via glab and member status is fetched.
func (h *Handler) viewStacksCrossProject(ctx context.Context, stacks []stackstore.Stack, live bool) []api.ViewStack {
	out := make([]api.ViewStack, 0, len(stacks))
	if !live {
		for _, s := range stacks {
			out = append(out, membershipViewStack(s, "status unavailable; use --refresh to live-fetch"))
		}
		return out
	}
	// Cache project resolution per distinct host+project to avoid repeat calls.
	type projectKey struct{ host, project string }
	resolved := map[projectKey]gitlab.Project{}
	clients := map[string]gitlab.Client{}
	runner := h.Runner
	if runner == nil {
		runner = gitexec.ExecRunner{}
	}
	dir := h.Dir
	if dir == "" {
		dir = "."
	}
	for _, s := range stacks {
		key := projectKey{s.Host, s.Project}
		project, ok := resolved[key]
		if !ok {
			client, okClient := clients[s.Host]
			if !okClient {
				client = gitlab.Client{Runner: runner, Dir: dir, Host: s.Host}
				clients[s.Host] = client
			}
			p, pErr := client.Project(ctx, s.Project)
			if pErr != nil {
				out = append(out, membershipViewStack(s,
					fmt.Sprintf("cannot resolve project: %s", classifyGlab("resolve project", pErr).Error())))
				continue
			}
			resolved[key] = p
			project = p
		}
		client := clients[s.Host]
		out = append(out, h.liveViewStack(ctx, client, project.ID.String(), project.DefaultBranch, s))
	}
	return out
}

// liveViewStack fetches each member MR, orders the chain, and reports status.
func (h *Handler) liveViewStack(ctx context.Context, client gitlab.Client,
	projectID, defaultBranch string, s stackstore.Stack) api.ViewStack {
	mrs := make([]gitlab.MergeRequest, 0, len(s.MemberIIDs))
	missing := []int{}
	for _, iid := range s.MemberIIDs {
		mr, err := client.MergeRequest(ctx, projectID, iid)
		if err != nil {
			missing = append(missing, iid)
			continue
		}
		mrs = append(mrs, mr)
	}
	vs := api.ViewStack{Name: s.Name, Host: s.Host, Project: s.Project, DefaultBranch: defaultBranch}
	if len(missing) > 0 {
		vs.Note = fmt.Sprintf("could not fetch MR IIDs: %v", missing)
	}
	ordered, note := orderChain(mrs, defaultBranch)
	if note != "" {
		if vs.Note != "" {
			vs.Note += "; " + note
		} else {
			vs.Note = note
		}
	}
	vs.Members = make([]api.ViewMember, 0, len(ordered))
	for i, mr := range ordered {
		vs.Members = append(vs.Members, viewMember(i, mr))
	}
	return vs
}

// membershipViewStack renders a stack from stored membership only (no live fetch).
func membershipViewStack(s stackstore.Stack, note string) api.ViewStack {
	vs := api.ViewStack{Name: s.Name, Host: s.Host, Project: s.Project, Note: note}
	vs.Members = make([]api.ViewMember, 0, len(s.MemberIIDs))
	for i, iid := range s.MemberIIDs {
		vs.Members = append(vs.Members, api.ViewMember{Position: i, IID: iid})
	}
	return vs
}

func viewMember(position int, mr gitlab.MergeRequest) api.ViewMember {
	return api.ViewMember{
		Position: position, IID: mr.IID, Title: mr.Title,
		SourceBranch: mr.SourceBranch, TargetBranch: mr.TargetBranch,
		State: mr.State, WebURL: mr.WebURL,
		MergeStatus:    mergeStatus(mr),
		PipelineStatus: pipelineStatus(mr),
	}
}

func mergeStatus(mr gitlab.MergeRequest) string {
	if mr.HasConflicts {
		return "conflict"
	}
	switch mr.DetailedMergeStatus {
	case "checking", "unchecked":
		return "checking"
	case "mergeable", "not_conflict", "conflict", "cannot_be_merged_reloaded":
		return mr.DetailedMergeStatus
	}
	if mr.DetailedMergeStatus != "" {
		return mr.DetailedMergeStatus
	}
	return "unknown"
}

func pipelineStatus(mr gitlab.MergeRequest) string {
	if mr.HeadPipeline == nil {
		return "none"
	}
	return normalizePipelineStatus(mr.HeadPipeline.Status)
}

// orderChain walks the target_branch -> source_branch linkage anchored at the
// project default branch and returns members in chain order. When the chain
// is broken, ambiguous, or cyclic the returned note describes the problem and
// the remaining members are appended in their input order so none are hidden.
func orderChain(mrs []gitlab.MergeRequest, defaultBranch string) ([]gitlab.MergeRequest, string) {
	if len(mrs) == 0 {
		return nil, ""
	}
	byTarget := map[string][]gitlab.MergeRequest{}
	byIID := map[int]gitlab.MergeRequest{}
	for _, mr := range mrs {
		byTarget[mr.TargetBranch] = append(byTarget[mr.TargetBranch], mr)
		byIID[mr.IID] = mr
	}
	anchorCandidates := byTarget[defaultBranch]
	var ordered []gitlab.MergeRequest
	var note string
	switch {
	case len(anchorCandidates) == 0:
		note = fmt.Sprintf("no member targets the default branch %q", defaultBranch)
		ordered = append(ordered, mrs...)
		return ordered, note
	case len(anchorCandidates) > 1:
		note = fmt.Sprintf("multiple members target the default branch %q; chain is ambiguous", defaultBranch)
		ordered = append(ordered, mrs...)
		return ordered, note
	}
	cur := anchorCandidates[0]
	visited := map[int]bool{cur.IID: true}
	ordered = append(ordered, cur)
	for {
		nexts := byTarget[cur.SourceBranch]
		if len(nexts) == 0 {
			break
		}
		if len(nexts) > 1 {
			note = fmt.Sprintf("multiple members target %q (after !%d); chain is ambiguous",
				cur.SourceBranch, cur.IID)
			break
		}
		next := nexts[0]
		if visited[next.IID] {
			note = fmt.Sprintf("cycle detected at !%d", next.IID)
			break
		}
		visited[next.IID] = true
		ordered = append(ordered, next)
		cur = next
	}
	if len(ordered) < len(mrs) {
		missing := make([]gitlab.MergeRequest, 0)
		for _, mr := range mrs {
			if !visited[mr.IID] {
				missing = append(missing, mr)
			}
		}
		sort.Slice(missing, func(i, j int) bool { return missing[i].IID < missing[j].IID })
		ordered = append(ordered, missing...)
		if note == "" {
			iids := make([]string, 0, len(missing))
			for _, m := range missing {
				iids = append(iids, fmt.Sprintf("!%d", m.IID))
			}
			note = "broken chain: " + strings.Join(iids, ", ") + " not reachable from the base"
		}
	}
	return ordered, note
}

func renderView(data api.ViewData) string {
	if len(data.Stacks) == 0 {
		if data.Live {
			return "No named stacks for this repository."
		}
		return "No named stacks."
	}
	var b strings.Builder
	for _, s := range data.Stacks {
		fmt.Fprintf(&b, "%s (%s/%s)\n", s.Name, s.Host, s.Project)
		for _, m := range s.Members {
			title := m.Title
			if title == "" {
				title = fmt.Sprintf("!%d", m.IID)
			}
			badges := []string{}
			if m.MergeStatus == "conflict" {
				badges = append(badges, "Merge Conflict")
			} else if m.MergeStatus == "checking" {
				badges = append(badges, "Mergeability checking")
			}
			switch m.PipelineStatus {
			case "failed":
				badges = append(badges, "Pipeline failed")
			case "canceled", "skipped":
				badges = append(badges, "Pipeline "+m.PipelineStatus)
			case "running", "pending":
				badges = append(badges, "Pipeline running")
			}
			prefix := "-->"
			if m.Position > 0 {
				prefix = "|-->"
			}
			line := fmt.Sprintf("  %s %s", prefix, title)
			if m.State == "merged" {
				badges = append(badges, "merged")
			} else if m.State == "closed" {
				badges = append(badges, "closed")
			}
			if len(badges) > 0 {
				line += " (" + strings.Join(badges, ", ") + ")"
			}
			fmt.Fprintln(&b, line)
		}
		if s.Note != "" {
			fmt.Fprintf(&b, "  note: %s\n", s.Note)
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}
