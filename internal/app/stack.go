package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nkaewam/mrstack/internal/api"
	"github.com/nkaewam/mrstack/internal/cli"
	"github.com/nkaewam/mrstack/internal/stackstore"
)

// stackStore opens the user-global named-stack registry. StacksDir lets tests
// point the store at a temp directory; production resolves ~/.mrstack/stacks.
func (h *Handler) stackStore() (*stackstore.Store, error) {
	dir := h.StacksDir
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, cli.Unavailable("stackstore_unavailable",
				"cannot resolve home directory for named stacks", false)
		}
		dir = filepath.Join(home, ".mrstack", "stacks")
	}
	store, err := stackstore.Open(dir)
	if err != nil {
		return nil, cli.Unavailable("stackstore_unavailable",
			"cannot open named-stack registry: "+err.Error(), false)
	}
	return store, nil
}

func (h *Handler) nowStamp() string {
	now := h.Now
	if now == nil {
		now = time.Now
	}
	return now().UTC().Format(time.RFC3339Nano)
}

func (h *Handler) stackCreate(ctx context.Context, inv cli.Invocation) (cli.Result, error) {
	rc, err := h.repository(ctx, inv.Globals.Remote, false)
	if err != nil {
		return cli.Result{}, err
	}
	store, err := h.stackStore()
	if err != nil {
		return cli.Result{}, err
	}
	stack, err := store.Create(inv.StackName, rc.fetch.Host, rc.fetch.Project, h.nowStamp())
	switch {
	case errors.Is(err, stackstore.ErrAlreadyExists):
		return cli.Result{}, cli.Invalid("invalid_arguments",
			fmt.Sprintf("a stack named %q already exists", inv.StackName))
	case errors.Is(err, stackstore.ErrInvalidName):
		return cli.Result{}, cli.Invalid("invalid_arguments",
			fmt.Sprintf("invalid stack name %q: use lowercase letters, digits, and hyphens (1-64 chars)", inv.StackName))
	case err != nil:
		return cli.Result{}, cli.Unavailable("stackstore_unavailable",
			"cannot create stack: "+err.Error(), false)
	}
	env, _, err := h.envelope(inv.Name)
	if err != nil {
		return cli.Result{}, cli.Internal("cannot create response envelope", err)
	}
	env.Data["stack"] = toStackData(stack)
	return result(env, fmt.Sprintf("Created stack %q bound to %s/%s",
		stack.Name, stack.Host, stack.Project))
}

func (h *Handler) stackAdd(ctx context.Context, inv cli.Invocation) (cli.Result, error) {
	store, err := h.stackStore()
	if err != nil {
		return cli.Result{}, err
	}
	stack, err := store.AddMembers(inv.StackName, inv.MemberIIDs, h.nowStamp())
	switch {
	case errors.Is(err, stackstore.ErrNotFound):
		return cli.Result{}, cli.Invalid("invalid_selector",
			fmt.Sprintf("no stack named %q; create it with `mrstack stack create` first", inv.StackName))
	case errors.Is(err, stackstore.ErrInvalidName):
		return cli.Result{}, cli.Invalid("invalid_arguments",
			fmt.Sprintf("invalid stack name %q", inv.StackName))
	case err != nil:
		return cli.Result{}, cli.Unavailable("stackstore_unavailable",
			"cannot update stack: "+err.Error(), false)
	}
	env, _, err := h.envelope(inv.Name)
	if err != nil {
		return cli.Result{}, cli.Internal("cannot create response envelope", err)
	}
	env.Data["stack"] = toStackData(stack)
	return result(env, fmt.Sprintf("Stack %q now has %d member(s): %v",
		stack.Name, len(stack.MemberIIDs), stack.MemberIIDs))
}

func (h *Handler) stackRemove(ctx context.Context, inv cli.Invocation) (cli.Result, error) {
	store, err := h.stackStore()
	if err != nil {
		return cli.Result{}, err
	}
	stack, err := store.RemoveMembers(inv.StackName, inv.MemberIIDs, h.nowStamp())
	switch {
	case errors.Is(err, stackstore.ErrNotFound):
		return cli.Result{}, cli.Invalid("invalid_selector",
			fmt.Sprintf("no stack named %q", inv.StackName))
	case errors.Is(err, stackstore.ErrInvalidName):
		return cli.Result{}, cli.Invalid("invalid_arguments",
			fmt.Sprintf("invalid stack name %q", inv.StackName))
	case err != nil:
		return cli.Result{}, cli.Unavailable("stackstore_unavailable",
			"cannot update stack: "+err.Error(), false)
	}
	env, _, err := h.envelope(inv.Name)
	if err != nil {
		return cli.Result{}, cli.Internal("cannot create response envelope", err)
	}
	env.Data["stack"] = toStackData(stack)
	return result(env, fmt.Sprintf("Stack %q now has %d member(s): %v",
		stack.Name, len(stack.MemberIIDs), stack.MemberIIDs))
}

func (h *Handler) stackDelete(ctx context.Context, inv cli.Invocation) (cli.Result, error) {
	store, err := h.stackStore()
	if err != nil {
		return cli.Result{}, err
	}
	// Snapshot the stack before deletion so the response describes what was
	// removed even though the file no longer exists.
	stack, err := store.Get(inv.StackName)
	switch {
	case errors.Is(err, stackstore.ErrNotFound):
		return cli.Result{}, cli.Invalid("invalid_selector",
			fmt.Sprintf("no stack named %q", inv.StackName))
	case errors.Is(err, stackstore.ErrInvalidName):
		return cli.Result{}, cli.Invalid("invalid_arguments",
			fmt.Sprintf("invalid stack name %q", inv.StackName))
	case err != nil:
		return cli.Result{}, cli.Unavailable("stackstore_unavailable",
			"cannot read stack: "+err.Error(), false)
	}
	if err := store.Delete(inv.StackName); err != nil {
		return cli.Result{}, cli.Unavailable("stackstore_unavailable",
			"cannot delete stack: "+err.Error(), false)
	}
	env, _, err := h.envelope(inv.Name)
	if err != nil {
		return cli.Result{}, cli.Internal("cannot create response envelope", err)
	}
	env.Data["stack"] = toStackData(stack)
	return result(env, fmt.Sprintf("Deleted stack %q (had %d member(s))",
		stack.Name, len(stack.MemberIIDs)))
}

func (h *Handler) stackList(ctx context.Context, inv cli.Invocation) (cli.Result, error) {
	store, err := h.stackStore()
	if err != nil {
		return cli.Result{}, err
	}
	all, err := store.List()
	if err != nil {
		return cli.Result{}, cli.Unavailable("stackstore_unavailable",
			"cannot list stacks: "+err.Error(), false)
	}
	stacks := []api.StackData{}
	if inv.AllStacks {
		for _, s := range all {
			stacks = append(stacks, toStackData(s))
		}
	} else {
		rc, err := h.repository(ctx, inv.Globals.Remote, false)
		if err != nil {
			return cli.Result{}, err
		}
		for _, s := range all {
			if s.Host == rc.fetch.Host && s.Project == rc.fetch.Project {
				stacks = append(stacks, toStackData(s))
			}
		}
	}
	env, _, err := h.envelope(inv.Name)
	if err != nil {
		return cli.Result{}, cli.Internal("cannot create response envelope", err)
	}
	env.Data["stacks"] = stacks
	scope := "this repository"
	if inv.AllStacks {
		scope = "all repositories"
	}
	return result(env, fmt.Sprintf("%d named stack(s) in %s", len(stacks), scope))
}

func toStackData(s stackstore.Stack) api.StackData {
	iids := s.MemberIIDs
	if iids == nil {
		iids = []int{}
	}
	return api.StackData{
		Name: s.Name, Host: s.Host, Project: s.Project,
		MemberIIDs: iids, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
	}
}
