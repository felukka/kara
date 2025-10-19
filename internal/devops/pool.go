package devops

import (
	"context"
	"fmt"
	"strings"

	"github.com/microsoft/azure-devops-go-api/azuredevops/v7/taskagent"
)

// ListPools returns every agent pool visible to the PAT.
func (c *Client) ListPools(ctx context.Context) (*[]taskagent.TaskAgentPool, error) {
	pools, err := c.taskAgentClient.GetAgentPools(ctx, taskagent.GetAgentPoolsArgs{})
	if err != nil {
		return nil, fmt.Errorf("azure devops: failed to list agent pools: %w", err)
	}

	if pools == nil {
		empty := []taskagent.TaskAgentPool{}
		return &empty, nil
	}

	return pools, nil
}

// GetPool resolves an agent pool by name. The name lookup is case
// insensitive and never panics on a missing pool.
func (c *Client) GetPool(ctx context.Context, poolName string) (*taskagent.TaskAgentPool, error) {
	// Fast path: exact name filter.
	pools, err := c.taskAgentClient.GetAgentPools(ctx, taskagent.GetAgentPoolsArgs{
		PoolName: &poolName,
	})
	if err == nil && pools != nil {
		for i := range *pools {
			if (*pools)[i].Name != nil && strings.EqualFold(*(*pools)[i].Name, poolName) {
				return &(*pools)[i], nil
			}
		}
	} else if err != nil {
		// The filtered lookup can fail on some servers; fall through to
		// listing all pools.
	}

	all, err := c.ListPools(ctx)
	if err != nil {
		return nil, fmt.Errorf("azure devops: failed to get agent pool %q: %w", poolName, err)
	}

	for i := range *all {
		if (*all)[i].Name != nil && strings.EqualFold(*(*all)[i].Name, poolName) {
			return &(*all)[i], nil
		}
	}

	available := make([]string, 0, len(*all))
	for i := range *all {
		if (*all)[i].Name != nil {
			available = append(available, *(*all)[i].Name)
		}
	}

	return nil, fmt.Errorf(
		"azure devops: no pool found with name %q (available pools: %s)",
		poolName,
		strings.Join(available, ", "),
	)
}

// GetPoolByID returns an agent pool by its numeric ID.
func (c *Client) GetPoolByID(ctx context.Context, poolID int) (*taskagent.TaskAgentPool, error) {
	pool, err := c.taskAgentClient.GetAgentPool(ctx, taskagent.GetAgentPoolArgs{
		PoolId: &poolID,
	})
	if err != nil {
		return nil, fmt.Errorf("azure devops: failed to get agent pool id %d: %w", poolID, err)
	}

	return pool, nil
}
