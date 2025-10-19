package devops

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/microsoft/azure-devops-go-api/azuredevops/v7/taskagent"
)

// parseJobRequests decodes a job-requests API response. Depending on the
// server build the endpoint returns either a raw JSON array or an
// envelope object of the form {"count": N, "value": [...]}.
func parseJobRequests(body []byte) ([]taskagent.TaskAgentJobRequest, error) {
	trimmed := strings.TrimSpace(string(body))
	switch {
	case strings.HasPrefix(trimmed, "{"):
		var envelope struct {
			Count int                             `json:"count"`
			Value []taskagent.TaskAgentJobRequest `json:"value"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			return nil, fmt.Errorf("azure devops: failed to parse job requests envelope: %w", err)
		}
		if envelope.Value == nil {
			return []taskagent.TaskAgentJobRequest{}, nil
		}
		return envelope.Value, nil
	case strings.HasPrefix(trimmed, "["):
		var raw []taskagent.TaskAgentJobRequest
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, fmt.Errorf("azure devops: failed to parse job requests array: %w", err)
		}
		return raw, nil
	default:
		return []taskagent.TaskAgentJobRequest{}, nil
	}
}

// GetAgentRequestsForPool returns every job request (queued and finished)
// currently reported for the pool.
//
// It uses the collection scoped distributedtask jobrequests API
// (/{org}/_apis/distributedtask/pools/{poolId}/jobrequests), which works
// for both organization scoped and project scoped pools without needing
// the owning project.
func (c *Client) GetAgentRequestsForPool(ctx context.Context, pool *taskagent.TaskAgentPool) (*[]taskagent.TaskAgentJobRequest, error) {
	if pool == nil || pool.Id == nil {
		return nil, fmt.Errorf("azure devops: a resolved pool (with ID) is required to list job requests")
	}

	url := fmt.Sprintf("%s/_apis/distributedtask/pools/%d/jobrequests?api-version=7.0", c.organizationURL, *pool.Id)
	jobs, err := c.fetchJobRequests(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("azure devops: failed to get job requests for pool %d: %w", *pool.Id, err)
	}
	return jobs, nil
}

// fetchJobRequests performs a job-requests request and parses the
// response.
func (c *Client) fetchJobRequests(ctx context.Context, url string) (*[]taskagent.TaskAgentJobRequest, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("azure devops: failed to create job requests request: %w", err)
	}

	resp, err := c.client.SendRequest(req)
	if err != nil {
		return nil, fmt.Errorf("azure devops: job requests request failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // best effort

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return nil, fmt.Errorf("azure devops: job requests request failed with status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("azure devops: failed to read job requests response: %w", err)
	}

	jobs, err := parseJobRequests(body)
	if err != nil {
		return nil, err
	}
	return &jobs, nil
}

// IsQueued reports whether a job request still needs to run, i.e. it has
// no terminal result yet.
func IsQueued(job taskagent.TaskAgentJobRequest) bool {
	return job.Result == nil
}

// QueuedJobs filters a job-request list down to jobs still queued.
func QueuedJobs(jobs []taskagent.TaskAgentJobRequest) []taskagent.TaskAgentJobRequest {
	out := make([]taskagent.TaskAgentJobRequest, 0, len(jobs))
	for _, job := range jobs {
		if IsQueued(job) {
			out = append(out, job)
		}
	}
	return out
}

// CountQueued counts jobs in a job-request list that still need to run.
func CountQueued(jobs []taskagent.TaskAgentJobRequest) int {
	count := 0
	for _, job := range jobs {
		if IsQueued(job) {
			count++
		}
	}
	return count
}

// JobDemandStrings returns the raw demand strings of a job request (each
// demand is a capability name, or a "name value" expression).
func JobDemandStrings(job taskagent.TaskAgentJobRequest) []string {
	if job.Demands == nil {
		return nil
	}
	out := make([]string, 0, len(*job.Demands))
	for _, d := range *job.Demands {
		if s, ok := d.(string); ok {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// FirstDemand returns the first demand of a job, trimmed and lower-cased,
// or "" when the job has no demands.
func FirstDemand(job taskagent.TaskAgentJobRequest) string {
	for _, d := range JobDemandStrings(job) {
		return strings.ToLower(d)
	}
	return ""
}

// QueuedJobForDemand reports whether a queued job demands the given
// capability (the demand may be a bare capability name or a
// "capability value" expression).
func QueuedJobForDemand(job taskagent.TaskAgentJobRequest, demand string) bool {
	if demand == "" || !IsQueued(job) {
		return false
	}
	demandLower := strings.ToLower(demand)
	for _, d := range JobDemandStrings(job) {
		d = strings.ToLower(d)
		if d == demandLower || strings.HasPrefix(d, demandLower+" ") {
			return true
		}
	}
	return false
}

// CountQueuedByCapability counts queued jobs per demanded capability.
// A job with multiple demands counts once per demand.
func CountQueuedByCapability(jobs []taskagent.TaskAgentJobRequest) map[string]int {
	counts := make(map[string]int)
	for _, job := range jobs {
		if !IsQueued(job) {
			continue
		}
		for _, d := range JobDemandStrings(job) {
			counts[strings.ToLower(d)]++
		}
	}
	return counts
}

// CountInFlight counts jobs that are currently reserved to an agent
// (queued or in progress).
func CountInFlight(jobs []taskagent.TaskAgentJobRequest) int {
	count := 0
	for _, job := range jobs {
		if IsQueued(job) && job.ReservedAgent != nil && job.ReservedAgent.Id != nil {
			count++
		}
	}
	return count
}

// GetQueuedJobsCount returns the number of jobs in the pool queue that
// still need to run (no terminal result).
func (c *Client) GetQueuedJobsCount(ctx context.Context, pool *taskagent.TaskAgentPool) (int, error) {
	jobs, err := c.GetAgentRequestsForPool(ctx, pool)
	if err != nil {
		return 0, err
	}
	return CountQueued(*jobs), nil
}

// GetQueuedJobsByCapability counts queued jobs per demanded capability.
func (c *Client) GetQueuedJobsByCapability(ctx context.Context, pool *taskagent.TaskAgentPool) (map[string]int, error) {
	jobs, err := c.GetAgentRequestsForPool(ctx, pool)
	if err != nil {
		return nil, err
	}
	return CountQueuedByCapability(*jobs), nil
}
