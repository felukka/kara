package devops

import (
	"context"
	"fmt"
	"strings"

	"github.com/microsoft/azure-devops-go-api/azuredevops/v7/taskagent"
)

// ListAgents returns the agents registered in the given pool.
// When includeLastCompleted is true the response includes each agent's
// most recent completed request, which the operator uses to derive the
// agent's LastActive time for idle-TTL decisions.
func (c *Client) ListAgents(ctx context.Context, poolID int, includeLastCompleted bool) (*[]taskagent.TaskAgent, error) {
	agents, err := c.taskAgentClient.GetAgents(ctx, taskagent.GetAgentsArgs{
		PoolId:                      &poolID,
		IncludeLastCompletedRequest: &includeLastCompleted,
	})
	if err != nil {
		return nil, fmt.Errorf("azure devops: failed to list agents in pool id %d: %w", poolID, err)
	}

	if agents == nil {
		empty := []taskagent.TaskAgent{}
		return &empty, nil
	}

	return agents, nil
}

// GetAgent returns a single agent by ID.
func (c *Client) GetAgent(ctx context.Context, poolID, agentID int) (*taskagent.TaskAgent, error) {
	agent, err := c.taskAgentClient.GetAgent(ctx, taskagent.GetAgentArgs{
		PoolId:  &poolID,
		AgentId: &agentID,
	})
	if err != nil {
		return nil, fmt.Errorf("azure devops: failed to get agent %d in pool %d: %w", agentID, poolID, err)
	}

	return agent, nil
}

// DeleteAgent unregisters an agent from its pool.
func (c *Client) DeleteAgent(ctx context.Context, poolID, agentID int) error {
	err := c.taskAgentClient.DeleteAgent(ctx, taskagent.DeleteAgentArgs{
		PoolId:  &poolID,
		AgentId: &agentID,
	})
	if err != nil {
		return fmt.Errorf("azure devops: failed to delete agent %d in pool id %d: %w", agentID, poolID, err)
	}

	return nil
}

// CountAgentsByStatus counts pool agents per status (online, offline, running).
func (c *Client) CountAgentsByStatus(ctx context.Context, poolID int) (map[string]int, error) {
	registeredAgents, err := c.taskAgentClient.GetAgents(ctx, taskagent.GetAgentsArgs{
		PoolId: &poolID,
	})
	if err != nil {
		return nil, fmt.Errorf("azure devops: failed to list agents in pool id %d: %w", poolID, err)
	}

	statusCount := make(map[string]int)
	if registeredAgents != nil {
		for _, agent := range *registeredAgents {
			if agent.Status != nil {
				statusCount[strings.ToLower(string(*agent.Status))]++
			}
		}
	}

	return statusCount, nil
}

// AgentName returns the agent's name, or "" when unset.
func AgentName(agent taskagent.TaskAgent) string {
	if agent.Name == nil {
		return ""
	}
	return *agent.Name
}

// AgentID returns the agent's ID, or 0 when unset.
func AgentID(agent taskagent.TaskAgent) int {
	if agent.Id == nil {
		return 0
	}
	return *agent.Id
}

// AgentStatus returns the agent's status lower-cased (online/offline/running).
func AgentStatus(agent taskagent.TaskAgent) string {
	if agent.Status == nil {
		return ""
	}
	return strings.ToLower(string(*agent.Status))
}
