package devops

import (
	"context"
	"log"

	"github.com/microsoft/azure-devops-go-api/azuredevops/v7/taskagent"
)

// PollingService polls Azure DevOps pools to detect queue changes and
// provide pool health metrics.
type PollingService struct {
	client *Client
}

// NewPollingService creates a new polling service.
func NewPollingService(client *Client) *PollingService {
	return &PollingService{
		client: client,
	}
}

// PollResult contains the metrics from a single poll.
type PollResult struct {
	PoolID        int
	PoolName      string
	QueuedJobs    int
	TotalAgents   int
	OnlineAgents  int
	OfflineAgents int
	RunningJobs   int
}

// Poll a specific pool and return its current metrics.
func (ps *PollingService) Poll(ctx context.Context, poolID int) (*PollResult, error) {
	pool, err := ps.client.GetPoolByID(ctx, poolID)
	if err != nil {
		return nil, err
	}
	poolName := "unknown"
	if pool != nil && pool.Name != nil {
		poolName = *pool.Name
	}

	queuedCount, err := ps.client.GetQueuedJobsCount(ctx, pool)
	if err != nil {
		return nil, err
	}

	runningJobs := 0
	jobRequests, err := ps.client.GetAgentRequestsForPool(ctx, pool)
	if err != nil {
		return nil, err
	}
	runningJobs = CountInFlight(*jobRequests)

	totalAgents, online, offline := 0, 0, 0
	agentCounts, err := ps.client.CountAgentsByStatus(ctx, poolID)
	if err != nil {
		return nil, err
	}
	for status, count := range agentCounts {
		totalAgents += count
		switch status {
		case "online":
			online = count
		case "offline":
			offline = count
		}
	}

	log.Printf("Polling pool %s (id=%d): %d queued, %d running, %d online, %d offline agents",
		poolName, poolID, queuedCount, runningJobs, online, offline)

	return &PollResult{
		PoolID:        poolID,
		PoolName:      poolName,
		QueuedJobs:    queuedCount,
		TotalAgents:   totalAgents,
		OnlineAgents:  online,
		OfflineAgents: offline,
		RunningJobs:   runningJobs,
	}, nil
}

// ListActivePools returns all pools that have any activity (queued jobs
// or agents).
func (ps *PollingService) ListActivePools(ctx context.Context) ([]*PollResult, error) {
	pools, err := ps.client.ListPools(ctx)
	if err != nil {
		return nil, err
	}

	var active []*PollResult
	for i := range *pools {
		pool := (*pools)[i]
		if pool.Id == nil {
			continue
		}

		result, err := ps.Poll(ctx, *pool.Id)
		if err != nil {
			log.Printf("Warning: failed to poll pool %d: %v", *pool.Id, err)
			continue
		}

		if result.QueuedJobs > 0 || result.TotalAgents > 0 {
			active = append(active, result)
		}
	}

	return active, nil
}

// GetPoolAgentStats returns detailed agent statistics for a pool, split
// between all registered agents and the operator-managed agents (those
// whose names match the <prefix>-agent-<index> pattern).
func (ps *PollingService) GetPoolAgentStats(ctx context.Context, poolID int, runnerPoolName string) (map[string]int, map[string]int, error) {
	agents, err := ps.client.ListAgents(ctx, poolID, false)
	if err != nil {
		return nil, nil, err
	}

	allAgents := make(map[string]int)
	managedAgents := make(map[string]int)

	for i := range *agents {
		agent := (*agents)[i]
		status := AgentStatus(agent)
		allAgents[status]++

		if IsOperatorManagedAgentName(AgentName(agent), runnerPoolName) {
			managedAgents[status]++
		}
	}

	return allAgents, managedAgents, nil
}

// IsOperatorManagedAgentName reports whether an Azure DevOps agent name
// belongs to this operator: "<runnerPoolName>-agent-<index>" where index
// is numeric (or an 8-char alphanumeric for agents created by older
// releases).
func IsOperatorManagedAgentName(agentName, runnerPoolName string) bool {
	prefix := runnerPoolName + "-agent-"
	if len(agentName) <= len(prefix) || agentName[:len(prefix)] != prefix {
		return false
	}
	indexPart := agentName[len(prefix):]
	if indexPart == "" {
		return false
	}
	if len(indexPart) == 8 && isAlphanumeric(indexPart) {
		return true
	}
	for _, r := range indexPart {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func isAlphanumeric(s string) bool {
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return false
		}
	}
	return len(s) > 0
}

// PoolHealth describes pool health at a point in time.
type PoolHealth struct {
	PoolID    int
	PoolName  string
	Queued    int
	Running   int
	Online    int
	Offline   int
	Healthy   bool
	HasQueued bool
}

// GetPoolHealth returns a health summary for a pool.
func (ps *PollingService) GetPoolHealth(ctx context.Context, pool *taskagent.TaskAgentPool) (*PoolHealth, error) {
	if pool == nil || pool.Id == nil {
		return nil, nil
	}

	result, err := ps.Poll(ctx, *pool.Id)
	if err != nil {
		return nil, err
	}

	health := &PoolHealth{
		PoolID:    result.PoolID,
		PoolName:  result.PoolName,
		Queued:    result.QueuedJobs,
		Online:    result.OnlineAgents,
		Offline:   result.OfflineAgents,
		Running:   result.RunningJobs,
		HasQueued: result.QueuedJobs > 0,
	}

	// A pool is healthy when it can run its queued work: at least one
	// online agent or an empty queue.
	health.Healthy = result.OnlineAgents > 0 || result.QueuedJobs == 0
	return health, nil
}
