package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/nkaewam/mrstack/internal/api"
	"github.com/nkaewam/mrstack/internal/cli"
	"github.com/nkaewam/mrstack/internal/journal"
)

func (h *Handler) historyShow(ctx context.Context, inv cli.Invocation) (cli.Result, error) {
	rc, stackID, j, err := h.historyContext(ctx, inv)
	if err != nil {
		return cli.Result{}, err
	}
	defer j.Close()
	page, err := j.History(ctx, stackID, inv.Limit, inv.Cursor)
	if errors.Is(err, journal.ErrNotFound) {
		return cli.Result{}, cli.Invalid("invalid_selector", "tracked stack history was not found")
	}
	if err != nil {
		if inv.Cursor != "" {
			return cli.Result{}, cli.Invalid("invalid_selector", "history cursor is invalid")
		}
		return cli.Result{}, cli.Unavailable("journal_unavailable", "cannot read stack history", false)
	}
	if err := historyPageBelongsToProject(page, rc.fetch.Host, rc.fetch.Project); err != nil {
		return cli.Result{}, err
	}
	data, err := convertHistoryPage(page)
	if err != nil {
		return cli.Result{}, cli.Internal("stored history payload is invalid", err)
	}
	env, _, err := h.envelope(inv.Name)
	if err != nil {
		return cli.Result{}, cli.Internal("cannot create response envelope", err)
	}
	env.Data["history"] = data
	return result(env, fmt.Sprintf("History for stack %s: %d observations", stackID, len(data.Observations)))
}

func (h *Handler) historyAlias(ctx context.Context, inv cli.Invocation) (cli.Result, error) {
	rc, err := h.repository(ctx, inv.Globals.Remote, false)
	if err != nil {
		return cli.Result{}, err
	}
	j, err := h.openJournal(rc.repo.Dir)
	if err != nil {
		return cli.Result{}, cli.Unavailable("journal_unavailable", "cannot open history journal", false)
	}
	defer j.Close()
	page, err := j.History(ctx, inv.Selector.StackID, 1, "")
	if errors.Is(err, journal.ErrNotFound) {
		return cli.Result{}, cli.Invalid("invalid_selector", "tracked stack history was not found")
	}
	if err != nil {
		return cli.Result{}, cli.Unavailable("journal_unavailable", "cannot read stack alias", false)
	}
	if err := historyPageBelongsToProject(page, rc.fetch.Host, rc.fetch.Project); err != nil {
		return cli.Result{}, err
	}
	previous := page.Alias
	var next *string
	if !inv.ClearAlias {
		value := inv.Alias
		next = &value
	}
	if err := j.SetAlias(ctx, inv.Selector.StackID, next); err != nil {
		return cli.Result{}, cli.Unavailable("journal_unavailable", "cannot update stack alias", false)
	}
	data := api.HistoryAliasData{
		StackID: inv.Selector.StackID, PreviousAlias: previous, Alias: next,
	}
	env, _, err := h.envelope(inv.Name)
	if err != nil {
		return cli.Result{}, cli.Internal("cannot create response envelope", err)
	}
	env.Data["history_alias"] = data
	return result(env, fmt.Sprintf("Alias updated for stack %s", inv.Selector.StackID))
}

func (h *Handler) historyPrune(ctx context.Context, inv cli.Invocation) (cli.Result, error) {
	rc, err := h.repository(ctx, inv.Globals.Remote, false)
	if err != nil {
		return cli.Result{}, err
	}
	j, err := h.openJournal(rc.repo.Dir)
	if err != nil {
		return cli.Result{}, cli.Unavailable("journal_unavailable", "cannot open history journal", false)
	}
	defer j.Close()
	if inv.Selector.StackID != "" {
		page, pageErr := j.History(ctx, inv.Selector.StackID, 1, "")
		if errors.Is(pageErr, journal.ErrNotFound) {
			return cli.Result{}, cli.Invalid("invalid_selector", "tracked stack history was not found")
		}
		if pageErr != nil {
			return cli.Result{}, cli.Unavailable("journal_unavailable", "cannot inspect stack history", false)
		}
		if err := historyPageBelongsToProject(page, rc.fetch.Host, rc.fetch.Project); err != nil {
			return cli.Result{}, err
		}
	}
	before, err := h.historyCutoff(inv.Before)
	if err != nil {
		return cli.Result{}, cli.Invalid("invalid_arguments",
			"--before must be an RFC 3339 timestamp or duration")
	}
	var deleted int64
	var targets []string
	if inv.Selector.StackID != "" {
		targets = []string{inv.Selector.StackID}
	} else {
		tracked, trackedErr := j.TrackedStacks(ctx, rc.fetch.Host+"/"+rc.fetch.Project)
		if trackedErr != nil {
			return cli.Result{}, cli.Unavailable("journal_unavailable",
				"cannot enumerate project history", false)
		}
		for _, stack := range tracked {
			targets = append(targets, stack.StackID)
		}
	}
	for _, target := range targets {
		removed, pruneErr := j.PruneObservations(ctx, before, target)
		if pruneErr != nil {
			return cli.Result{}, cli.Unavailable("journal_unavailable", "cannot prune stack history", false)
		}
		deleted += removed
	}
	var stackID *string
	preserved := 0
	if inv.Selector.StackID != "" {
		value := inv.Selector.StackID
		stackID = &value
	}
	for _, target := range targets {
		count, countErr := j.ObservationCount(ctx, target)
		if countErr != nil {
			return cli.Result{}, cli.Unavailable("journal_unavailable",
				"cannot count preserved history", false)
		}
		preserved += count
	}
	data := api.HistoryPruneData{
		StackID: stackID, Before: before.UTC().Format(time.RFC3339Nano),
		DeletedObservations: int(deleted), DeletedEvidence: 0, PreservedRecords: preserved,
	}
	env, _, err := h.envelope(inv.Name)
	if err != nil {
		return cli.Result{}, cli.Internal("cannot create response envelope", err)
	}
	env.Data["history_prune"] = data
	return result(env, fmt.Sprintf("Pruned %d historical observations", deleted))
}

func (h *Handler) historyContext(ctx context.Context, inv cli.Invocation) (
	repositoryContext, string, *journal.Journal, error) {
	rc, err := h.repository(ctx, inv.Globals.Remote, false)
	if err != nil {
		return repositoryContext{}, "", nil, err
	}
	j, err := h.openJournal(rc.repo.Dir)
	if err != nil {
		return repositoryContext{}, "", nil,
			cli.Unavailable("journal_unavailable", "cannot open history journal", false)
	}
	stackID := inv.Selector.StackID
	if stackID == "" {
		selector := inv.Selector.Value
		if selector == "" {
			selector, err = rc.repo.CurrentBranch(ctx)
			if err != nil {
				j.Close()
				return repositoryContext{}, "", nil,
					cli.Invalid("invalid_selector", "detached HEAD requires an explicit history selector")
			}
		}
		stackID, err = h.resolveHistoricalSelector(ctx, rc, j, selector)
		if err != nil {
			j.Close()
			return repositoryContext{}, "", nil, err
		}
	}
	return rc, stackID, j, nil
}

func (h *Handler) resolveHistoricalSelector(ctx context.Context, rc repositoryContext,
	j *journal.Journal, selector string) (string, error) {
	tracked, err := j.TrackedStacks(ctx, rc.fetch.Host+"/"+rc.fetch.Project)
	if err != nil {
		return "", cli.Unavailable("journal_unavailable", "cannot enumerate stored history", false)
	}
	iid, isIID := decimalIID(selector)
	candidates := map[string]bool{}
	for _, trackedStack := range tracked {
		page, pageErr := j.History(ctx, trackedStack.StackID, 1, "")
		if pageErr != nil || len(page.Records) == 0 {
			continue
		}
		var env api.Envelope
		if json.Unmarshal(page.Records[0].Payload, &env) != nil || env.Stack == nil {
			continue
		}
		for _, member := range env.Stack.Members {
			if isIID && member.IID == iid || !isIID && member.SourceBranch == selector {
				candidates[trackedStack.StackID] = true
			}
		}
	}
	if len(candidates) == 0 {
		return "", cli.Invalid("invalid_selector", "no stored history matches the selector")
	}
	if len(candidates) > 1 {
		return "", cli.Invalid("invalid_selector", "history selector matches multiple tracked stacks; use --stack")
	}
	for stackID := range candidates {
		return stackID, nil
	}
	panic("unreachable")
}

func historyPageBelongsToProject(page journal.HistoryPage, host, project string) error {
	if len(page.Records) == 0 {
		return cli.Invalid("invalid_selector", "tracked stack has no stored observations")
	}
	var env api.Envelope
	if err := json.Unmarshal(page.Records[0].Payload, &env); err != nil || env.Stack == nil {
		return cli.Internal("stored history payload is invalid", err)
	}
	if env.Stack.Project.Host != host || env.Stack.Project.PathWithNamespace != project {
		return cli.Invalid("invalid_selector", "tracked stack belongs to a different GitLab project")
	}
	return nil
}

func convertHistoryPage(page journal.HistoryPage) (api.HistoryData, error) {
	data := api.HistoryData{
		StackID: page.StackID, Alias: page.Alias, Observations: []api.HistoryObservation{},
		FindingIntervals: []api.FindingInterval{},
	}
	if page.NextCursor != "" {
		value := page.NextCursor
		data.NextCursor = &value
	}
	intervals := map[string]api.FindingInterval{}
	for _, record := range page.Records {
		var env api.Envelope
		if err := json.Unmarshal(record.Payload, &env); err != nil {
			return api.HistoryData{}, err
		}
		disposition := api.Disposition(record.Disposition)
		observation := api.HistoryObservation{
			ObservationID: record.ObservationID,
			ObservedAt:    record.ObservedAt.UTC().Format(time.RFC3339Nano),
			SnapshotID:    record.SnapshotID, Disposition: disposition,
			MemberIIDs: []int{}, FindingCodes: []string{},
		}
		if env.Stack != nil {
			observation.MemberIIDs = memberIIDs(env.Stack.Members)
		}
		for _, finding := range env.Findings {
			observation.FindingCodes = append(observation.FindingCodes, finding.Code)
			if _, exists := intervals[finding.FindingID]; !exists {
				intervals[finding.FindingID] = api.FindingInterval{
					FindingID: finding.FindingID, Code: finding.Code,
					FirstSeenAt: finding.FirstSeenAt,
					LastSeenAt:  record.LastSeenAt.UTC().Format(time.RFC3339Nano),
				}
			}
		}
		sort.Strings(observation.FindingCodes)
		data.Observations = append(data.Observations, observation)
	}
	ids := make([]string, 0, len(intervals))
	for id := range intervals {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		data.FindingIntervals = append(data.FindingIntervals, intervals[id])
	}
	return data, nil
}

var historyDurationPart = regexp.MustCompile(`([1-9][0-9]*)(ns|us|µs|ms|s|m|h|d|w)`)

func (h *Handler) historyCutoff(value string) (time.Time, error) {
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed.UTC(), nil
	}
	var duration time.Duration
	offset := 0
	for _, match := range historyDurationPart.FindAllStringSubmatchIndex(value, -1) {
		if match[0] != offset {
			return time.Time{}, errors.New("invalid duration")
		}
		n, err := strconv.ParseInt(value[match[2]:match[3]], 10, 64)
		if err != nil {
			return time.Time{}, err
		}
		unit := value[match[4]:match[5]]
		var multiplier time.Duration
		switch unit {
		case "d":
			multiplier = 24 * time.Hour
		case "w":
			multiplier = 7 * 24 * time.Hour
		default:
			multiplier, err = time.ParseDuration("1" + unit)
			if err != nil {
				return time.Time{}, err
			}
		}
		duration += time.Duration(n) * multiplier
		offset = match[1]
	}
	if offset != len(value) || duration <= 0 {
		return time.Time{}, errors.New("invalid duration")
	}
	now := h.Now
	if now == nil {
		now = time.Now
	}
	return now().Add(-duration).UTC(), nil
}
