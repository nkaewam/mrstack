package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/nkaewam/mrstack/internal/api"
	"github.com/nkaewam/mrstack/internal/cli"
	"github.com/nkaewam/mrstack/internal/gitlab"
)

func (h *Handler) ciLogs(ctx context.Context, inv cli.Invocation) (cli.Result, error) {
	seen := map[string]bool{}
	for _, id := range inv.JobIDs {
		if seen[id] {
			return cli.Result{}, cli.Invalid("invalid_arguments", "--job values must be unique")
		}
		seen[id] = true
	}
	rc, err := h.repository(ctx, inv.Globals.Remote, true)
	if err != nil {
		return cli.Result{}, err
	}
	jobs, err := rc.client.PipelineJobs(ctx, rc.project.ID.String(), inv.PipelineID)
	if err != nil {
		return cli.Result{}, classifyGlab("verify pipeline jobs", err)
	}
	byID := make(map[string]gitlab.Job, len(jobs))
	for _, job := range jobs {
		byID[job.ID.String()] = job
	}
	traces := make([][]byte, len(inv.JobIDs))
	for i, id := range inv.JobIDs {
		if _, ok := byID[id]; !ok {
			return cli.Result{}, cli.Invalid("invalid_selector",
				fmt.Sprintf("job %s does not belong to pipeline %s", id, inv.PipelineID))
		}
		trace, traceErr := rc.client.JobTrace(ctx, rc.project.ID.String(), id)
		if traceErr != nil {
			return cli.Result{}, classifyGlab("read job trace", traceErr)
		}
		traces[i] = trace
	}
	bounded, err := gitlab.BoundLogs(inv.JobIDs, traces, int(inv.MaxBytes))
	if err != nil {
		return cli.Result{}, cli.Internal("cannot apply CI log budget", err)
	}
	logs := make([]api.LogEntry, len(bounded))
	var human strings.Builder
	for i, log := range bounded {
		job := byID[log.JobID]
		total := log.SourceBytes
		logs[i] = api.LogEntry{
			PipelineID: inv.PipelineID, JobID: log.JobID, JobName: job.Name,
			Status: job.Status, Text: log.Text, ReturnedBytes: log.ReturnedBytes,
			TotalBytes: &total, Truncated: log.Truncated,
			InvalidUTF8Replaced: log.InvalidUTF8Replaced,
		}
		fmt.Fprintf(&human, "== job %s: %s (%s) ==\n%s", log.JobID, job.Name, job.Status, log.Text)
		if !strings.HasSuffix(log.Text, "\n") {
			human.WriteByte('\n')
		}
		if log.Truncated {
			human.WriteString("[trace tail truncated]\n")
		}
	}
	env, _, err := h.envelope(inv.Name)
	if err != nil {
		return cli.Result{}, cli.Internal("cannot create response envelope", err)
	}
	env.Data["log_request"] = api.LogRequest{
		PipelineID: inv.PipelineID, JobIDs: append([]string(nil), inv.JobIDs...),
	}
	env.Data["log_budget"] = api.LogBudget{
		RequestedBytes: int(inv.MaxBytes), EffectiveBytes: int(inv.MaxBytes),
		HardMaxBytes: gitlab.MaxLogBudget, Allocation: "equal_per_job_tail",
	}
	env.Data["logs"] = logs
	return result(env, strings.TrimSuffix(human.String(), "\n"))
}
