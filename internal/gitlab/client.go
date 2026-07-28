// Package gitlab implements mrstack's glab-only GitLab transport. All
// invocations are argv arrays; endpoint construction is allowlisted and no
// provider payload is exposed directly to the public API.
package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/nkaewam/mrstack/internal/gitexec"
)

type Client struct {
	Runner gitexec.CommandRunner
	Dir    string
	Host   string
}

func (c Client) api(ctx context.Context, endpoint string, out any, args ...string) error {
	if c.Runner == nil {
		c.Runner = gitexec.ExecRunner{}
	}
	if endpoint == "" || !strings.HasPrefix(endpoint, "/") ||
		strings.ContainsAny(endpoint, "\x00\n\r") {
		return errors.New("unsafe GitLab endpoint")
	}
	argv := []string{"api"}
	if c.Host != "" {
		argv = append(argv, "--hostname", c.Host)
	}
	argv = append(argv, endpoint)
	argv = append(argv, args...)
	result, err := c.Runner.Run(ctx, c.Dir, "glab", argv...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(result.Stdout, out); err != nil {
		return fmt.Errorf("decode GitLab response for %s: %w", endpoint, err)
	}
	return nil
}

// apiArray fetches a paginated REST array endpoint via `glab api --paginate`
// and decodes the result with a streaming decoder.
//
// glab's default JSON output mode does not merge paginated REST array
// responses into a single array: it concatenates the raw response bodies
// back-to-back with no separator, so a multi-page array endpoint yields
// "[{...page1...}][{...page2...}]", which is not a valid single JSON document
// and causes json.Unmarshal to fail. This is silent for small repos (one
// page) but breaks discovery on projects with many merge requests.
//
// decodeArrayStream tolerates three shapes so the transport is robust across
// glab versions and output modes: a single JSON array, concatenated JSON
// arrays, and newline-delimited JSON objects.
func apiArray[T any](c Client, ctx context.Context, endpoint string) ([]T, error) {
	if c.Runner == nil {
		c.Runner = gitexec.ExecRunner{}
	}
	if endpoint == "" || !strings.HasPrefix(endpoint, "/") ||
		strings.ContainsAny(endpoint, "\x00\n\r") {
		return nil, errors.New("unsafe GitLab endpoint")
	}
	argv := []string{"api"}
	if c.Host != "" {
		argv = append(argv, "--hostname", c.Host)
	}
	argv = append(argv, endpoint, "--paginate")
	result, err := c.Runner.Run(ctx, c.Dir, "glab", argv...)
	if err != nil {
		return nil, err
	}
	items, err := decodeArrayStream[T](result.Stdout)
	if err != nil {
		return nil, fmt.Errorf("decode GitLab response for %s: %w", endpoint, err)
	}
	return items, nil
}

// decodeArrayStream decodes a sequence of JSON values into a flat slice. Each
// value may be a JSON array (its elements are appended) or a single JSON
// object (appended as one element). This handles glab's concatenated
// multi-page array output as well as NDJSON streams.
func decodeArrayStream[T any](data []byte) ([]T, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	var all []T
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 {
			continue
		}
		switch trimmed[0] {
		case '[':
			var batch []T
			if err := json.Unmarshal(raw, &batch); err != nil {
				return nil, err
			}
			all = append(all, batch...)
		case '{':
			var item T
			if err := json.Unmarshal(raw, &item); err != nil {
				return nil, err
			}
			all = append(all, item)
		default:
			return nil, fmt.Errorf("unexpected JSON token %q at start of array element", trimmed[0])
		}
	}
	return all, nil
}

type Version struct {
	Version string `json:"version"`
}

func (c Client) Version(ctx context.Context) (Version, error) {
	var version Version
	err := c.api(ctx, "/version", &version)
	if err == nil && version.Version == "" {
		err = errors.New("GitLab returned an empty version")
	}
	return version, err
}

type User struct {
	ID       json.Number `json:"id"`
	Username string      `json:"username"`
}

func (c Client) CurrentUser(ctx context.Context) (User, error) {
	var user User
	err := c.api(ctx, "/user", &user)
	return user, err
}

type Project struct {
	ID                       json.Number `json:"id"`
	PathWithNamespace        string      `json:"path_with_namespace"`
	WebURL                   string      `json:"web_url"`
	DefaultBranch            string      `json:"default_branch"`
	OnlyAllowMergeIfPipeline *bool       `json:"only_allow_merge_if_pipeline_succeeds"`
}

func (c Client) Project(ctx context.Context, path string) (Project, error) {
	var project Project
	err := c.api(ctx, "/projects/"+url.PathEscape(path), &project)
	return project, err
}

type DiffRefs struct {
	BaseSHA  string `json:"base_sha"`
	HeadSHA  string `json:"head_sha"`
	StartSHA string `json:"start_sha"`
}

type MRUser struct {
	ID       json.Number `json:"id"`
	Username string      `json:"username"`
}

type MergeRequest struct {
	IID                 int         `json:"iid"`
	State               string      `json:"state"`
	Title               string      `json:"title"`
	SourceProjectID     json.Number `json:"source_project_id"`
	TargetProjectID     json.Number `json:"target_project_id"`
	SourceBranch        string      `json:"source_branch"`
	TargetBranch        string      `json:"target_branch"`
	SHA                 string      `json:"sha"`
	WebURL              string      `json:"web_url"`
	Author              MRUser      `json:"author"`
	DiffRefs            DiffRefs    `json:"diff_refs"`
	HasConflicts        bool        `json:"has_conflicts"`
	DetailedMergeStatus string      `json:"detailed_merge_status"`
	MergeCommitSHA      string      `json:"merge_commit_sha"`
	SquashCommitSHA     string      `json:"squash_commit_sha"`
	MergedAt            string      `json:"merged_at"`
	HeadPipeline        *Pipeline   `json:"head_pipeline"`
	References          interface{} `json:"references,omitempty"`
}

func projectPrefix(projectID string) (string, error) {
	if projectID == "" {
		return "", errors.New("missing project ID")
	}
	if _, err := strconv.ParseUint(projectID, 10, 64); err != nil {
		return "", errors.New("project ID must be decimal")
	}
	return "/projects/" + projectID, nil
}

func (c Client) OpenMergeRequests(ctx context.Context, projectID string) ([]MergeRequest, error) {
	return c.MergeRequests(ctx, projectID, "opened")
}

// MergeRequests returns project merge requests for one explicit provider
// state. "all" is required by discovery because closed and merged lifecycle
// evidence can invalidate an otherwise plausible open chain.
func (c Client) MergeRequests(ctx context.Context, projectID, state string) ([]MergeRequest, error) {
	prefix, err := projectPrefix(projectID)
	if err != nil {
		return nil, err
	}
	switch state {
	case "opened", "closed", "merged", "all":
	default:
		return nil, errors.New("unsupported merge request state")
	}
	endpoint := prefix + "/merge_requests?state=" + state + "&scope=all&per_page=100"
	return apiArray[MergeRequest](c, ctx, endpoint)
}

func (c Client) MergeRequest(ctx context.Context, projectID string, iid int) (MergeRequest, error) {
	prefix, err := projectPrefix(projectID)
	if err != nil {
		return MergeRequest{}, err
	}
	if iid <= 0 {
		return MergeRequest{}, errors.New("MR IID must be positive")
	}
	var request MergeRequest
	err = c.api(ctx, fmt.Sprintf("%s/merge_requests/%d", prefix, iid), &request)
	return request, err
}

func (c Client) UpdateTarget(ctx context.Context, projectID string, iid int, target string) error {
	prefix, err := projectPrefix(projectID)
	if err != nil {
		return err
	}
	if iid <= 0 || target == "" || strings.ContainsAny(target, "\x00\n\r") {
		return errors.New("invalid target update")
	}
	var response struct {
		IID int `json:"iid"`
	}
	return c.api(ctx, fmt.Sprintf("%s/merge_requests/%d", prefix, iid), &response,
		"--method", "PUT", "--field", "target_branch="+target)
}

type Pipeline struct {
	ID     json.Number `json:"id"`
	SHA    string      `json:"sha"`
	Ref    string      `json:"ref"`
	Status string      `json:"status"`
	WebURL string      `json:"web_url"`
	Source string      `json:"source"`
}

// PipelineMergeRequest is the deliberately small association shape returned
// by GitLab's pipeline-to-merge-request endpoint. The complete MR payload is
// not needed to prove the association.
type PipelineMergeRequest struct {
	IID int `json:"iid"`
}

// Commit contains only immutable commit evidence needed to validate a merged
// results pipeline. ParentIDs must be checked exactly by the caller.
type Commit struct {
	ID        string   `json:"id"`
	ParentIDs []string `json:"parent_ids"`
}

type Job struct {
	ID        json.Number `json:"id"`
	Name      string      `json:"name"`
	Status    string      `json:"status"`
	WebURL    string      `json:"web_url"`
	AllowFail bool        `json:"allow_failure"`
}

func validDecimalID(id string) bool {
	if id == "" {
		return false
	}
	_, err := strconv.ParseUint(id, 10, 64)
	return err == nil
}

func fullObjectID(id string) bool {
	if len(id) != 40 && len(id) != 64 {
		return false
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// Pipeline reads the full pipeline resource. Embedded head_pipeline objects
// can omit fields such as source and ref, so they are not sufficient evidence
// for classifying merge-request pipeline kinds.
func (c Client) Pipeline(ctx context.Context, projectID, pipelineID string) (Pipeline, error) {
	prefix, err := projectPrefix(projectID)
	if err != nil {
		return Pipeline{}, err
	}
	if !validDecimalID(pipelineID) {
		return Pipeline{}, errors.New("pipeline ID must be decimal")
	}
	var pipeline Pipeline
	err = c.api(ctx, prefix+"/pipelines/"+pipelineID, &pipeline)
	return pipeline, err
}

func (c Client) PipelineMergeRequests(ctx context.Context, projectID, pipelineID string) ([]PipelineMergeRequest, error) {
	prefix, err := projectPrefix(projectID)
	if err != nil {
		return nil, err
	}
	if !validDecimalID(pipelineID) {
		return nil, errors.New("pipeline ID must be decimal")
	}
	var requests []PipelineMergeRequest
	err = c.api(ctx, prefix+"/pipelines/"+pipelineID+"/merge_requests", &requests)
	return requests, err
}

func (c Client) Commit(ctx context.Context, projectID, oid string) (Commit, error) {
	prefix, err := projectPrefix(projectID)
	if err != nil {
		return Commit{}, err
	}
	if !fullObjectID(oid) {
		return Commit{}, errors.New("commit ID must be a full object ID")
	}
	var commit Commit
	err = c.api(ctx, prefix+"/repository/commits/"+strings.ToLower(oid), &commit)
	return commit, err
}

func (c Client) PipelineJobs(ctx context.Context, projectID, pipelineID string) ([]Job, error) {
	prefix, err := projectPrefix(projectID)
	if err != nil {
		return nil, err
	}
	if !validDecimalID(pipelineID) {
		return nil, errors.New("pipeline ID must be decimal")
	}
	return apiArray[Job](c, ctx, prefix+"/pipelines/"+pipelineID+"/jobs?per_page=100")
}

func (c Client) JobTrace(ctx context.Context, projectID, jobID string) ([]byte, error) {
	prefix, err := projectPrefix(projectID)
	if err != nil {
		return nil, err
	}
	if _, err := strconv.ParseUint(jobID, 10, 64); err != nil {
		return nil, errors.New("job ID must be decimal")
	}
	if c.Runner == nil {
		c.Runner = gitexec.ExecRunner{}
	}
	argv := []string{"api"}
	if c.Host != "" {
		argv = append(argv, "--hostname", c.Host)
	}
	argv = append(argv, prefix+"/jobs/"+jobID+"/trace")
	result, err := c.Runner.Run(ctx, c.Dir, "glab", argv...)
	return result.Stdout, err
}
