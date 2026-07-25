package gitexec

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

var ErrNotRepository = errors.New("not a git repository")

type Repository struct {
	Dir    string
	Runner CommandRunner
}

func Open(ctx context.Context, runner CommandRunner, dir string) (*Repository, error) {
	if runner == nil {
		runner = ExecRunner{}
	}
	res, err := runner.Run(ctx, dir, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotRepository, err)
	}
	root := strings.TrimSpace(string(res.Stdout))
	if root == "" {
		return nil, ErrNotRepository
	}
	return &Repository{Dir: root, Runner: runner}, nil
}

func (r *Repository) Git(ctx context.Context, args ...string) (Result, error) {
	return r.Runner.Run(ctx, r.Dir, "git", args...)
}

func (r *Repository) RevParse(ctx context.Context, rev string) (string, error) {
	res, err := r.Git(ctx, "rev-parse", "--verify", rev+"^{commit}")
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(string(res.Stdout))
	if !FullOID(sha) {
		return "", fmt.Errorf("git returned abbreviated or invalid object id %q", sha)
	}
	return sha, nil
}

func (r *Repository) IsAncestor(ctx context.Context, ancestor, descendant string) (bool, error) {
	_, err := r.Git(ctx, "merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	var commandErr *CommandError
	if errors.As(err, &commandErr) && commandErr.ExitCode == 1 {
		return false, nil
	}
	return false, err
}

func (r *Repository) CurrentBranch(ctx context.Context) (string, error) {
	res, err := r.Git(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(res.Stdout)), nil
}

func (r *Repository) UpstreamRemote(ctx context.Context, branch string) (string, error) {
	key := "branch." + branch + ".remote"
	res, err := r.Git(ctx, "config", "--get", key)
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(string(res.Stdout))
	if name == "" || name == "." {
		return "", errors.New("branch has no remote upstream")
	}
	return name, nil
}

type RemoteIdentity struct {
	Host    string
	Project string
}

// ParseRemoteIdentity accepts common GitLab HTTPS and SCP-like SSH forms while
// returning only a credential-free host/project identity.
func ParseRemoteIdentity(raw string) (RemoteIdentity, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return RemoteIdentity{}, errors.New("empty remote URL")
	}
	var host, path string
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" {
			return RemoteIdentity{}, fmt.Errorf("invalid remote URL")
		}
		host, path = strings.ToLower(u.Hostname()), u.Path
	} else {
		at := strings.LastIndex(raw, "@")
		colon := strings.Index(raw[at+1:], ":")
		if colon < 0 {
			return RemoteIdentity{}, fmt.Errorf("unsupported remote URL")
		}
		colon += at + 1
		host, path = raw[at+1:colon], raw[colon+1:]
		host = strings.ToLower(host)
	}
	project := strings.Trim(strings.TrimSuffix(path, ".git"), "/")
	if host == "" || project == "" || strings.Contains(project, "..") ||
		unsafeRemoteComponent(host) || unsafeRemoteComponent(project) {
		return RemoteIdentity{}, fmt.Errorf("unsafe remote identity")
	}
	return RemoteIdentity{Host: host, Project: project}, nil
}

func unsafeRemoteComponent(value string) bool {
	return strings.ContainsRune(value, '@') || strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsControl(r) || unicode.IsSpace(r)
	}) >= 0
}

func (r *Repository) RemoteIdentity(ctx context.Context, name string, push bool) (RemoteIdentity, error) {
	args := []string{"remote", "get-url"}
	if push {
		args = append(args, "--push")
	}
	args = append(args, name)
	res, err := r.Git(ctx, args...)
	if err != nil {
		return RemoteIdentity{}, err
	}
	return ParseRemoteIdentity(strings.TrimSpace(string(res.Stdout)))
}

type RefUpdate struct {
	Branch string
	OldOID string
	NewOID string
}

// AtomicPush publishes all updates in one transaction with an explicit lease
// for each ref. Ref names and OIDs are validated before Git sees them.
func (r *Repository) AtomicPush(ctx context.Context, remote string, updates []RefUpdate) error {
	if remote == "" || strings.HasPrefix(remote, "-") || len(updates) == 0 {
		return errors.New("invalid atomic push request")
	}
	sorted := append([]RefUpdate(nil), updates...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Branch < sorted[j].Branch })
	args := []string{"push", "--atomic", "--porcelain"}
	for _, update := range sorted {
		if !ValidBranch(update.Branch) || !FullOID(update.OldOID) || !FullOID(update.NewOID) {
			return fmt.Errorf("invalid ref update for %q", update.Branch)
		}
		ref := "refs/heads/" + update.Branch
		args = append(args, "--force-with-lease="+ref+":"+update.OldOID)
	}
	args = append(args, remote)
	for _, update := range sorted {
		args = append(args, update.NewOID+":refs/heads/"+update.Branch)
	}
	_, err := r.Git(ctx, args...)
	return err
}

// RemoteRefs reads exactly the requested branch refs without fetching or
// changing any local ref. Every requested ref must be present with a full OID.
func (r *Repository) RemoteRefs(ctx context.Context, remote string, branches []string) (map[string]string, error) {
	refs, err := r.RemoteRefsAllowMissing(ctx, remote, branches)
	if err != nil {
		return nil, err
	}
	for _, branch := range branches {
		if !FullOID(refs[branch]) {
			return nil, fmt.Errorf("remote branch %q is missing", branch)
		}
	}
	return refs, nil
}

// RemoteRefsAllowMissing is the observation variant used while classifying
// missing active branches and deleted legacy predecessor branches.
func (r *Repository) RemoteRefsAllowMissing(ctx context.Context, remote string, branches []string) (map[string]string, error) {
	if remote == "" || strings.HasPrefix(remote, "-") || len(branches) == 0 {
		return nil, errors.New("invalid remote ref request")
	}
	names := append([]string(nil), branches...)
	sort.Strings(names)
	args := []string{"ls-remote", "--heads", remote}
	for _, branch := range names {
		if !ValidBranch(branch) {
			return nil, fmt.Errorf("invalid remote branch %q", branch)
		}
		args = append(args, "refs/heads/"+branch)
	}
	result, err := r.Git(ctx, args...)
	if err != nil {
		return nil, err
	}
	refs := make(map[string]string, len(names))
	for _, line := range strings.Split(strings.TrimSpace(string(result.Stdout)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || !FullOID(fields[0]) || !strings.HasPrefix(fields[1], "refs/heads/") {
			return nil, errors.New("unexpected git ls-remote output")
		}
		refs[strings.TrimPrefix(fields[1], "refs/heads/")] = fields[0]
	}
	return refs, nil
}

func FullOID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func ValidBranch(branch string) bool {
	if branch == "" || strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "/") ||
		strings.HasSuffix(branch, "/") || strings.HasSuffix(branch, ".") ||
		strings.Contains(branch, "..") || strings.Contains(branch, "@{") ||
		strings.ContainsAny(branch, " ~^:?*[\\\x00\n\r") {
		return false
	}
	return filepath.Clean(branch) == branch
}
