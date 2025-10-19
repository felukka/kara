package devops

import (
	"context"
	"fmt"
	"strings"

	"github.com/microsoft/azure-devops-go-api/azuredevops/v7"
	"github.com/microsoft/azure-devops-go-api/azuredevops/v7/taskagent"
)

// Client wraps the Azure DevOps REST client with the operations the
// operator needs to manage an agent pool.
type Client struct {
	client          azuredevops.Client
	connection      *azuredevops.Connection
	taskAgentClient taskagent.Client
	organizationURL string
}

// NewClient creates a PAT-authenticated Azure DevOps client for the given
// organization URL.
func NewClient(organizationURL, personalAccessToken string) (*Client, error) {
	if strings.TrimSpace(organizationURL) == "" {
		return nil, fmt.Errorf("azure devops: organization URL is required")
	}
	if strings.TrimSpace(personalAccessToken) == "" {
		return nil, fmt.Errorf("azure devops: personal access token is required")
	}

	azdoConnection := azuredevops.NewPatConnection(organizationURL, personalAccessToken)

	azdoClient := azuredevops.NewClient(azdoConnection, organizationURL)
	azdoTaskAgentClient, err := taskagent.NewClient(context.Background(), azdoConnection)
	if err != nil {
		return nil, fmt.Errorf("azure devops: failed to create task agent client: %w", err)
	}

	return &Client{
		client:          *azdoClient,
		connection:      azdoConnection,
		taskAgentClient: azdoTaskAgentClient,
		organizationURL: strings.TrimSuffix(organizationURL, "/"),
	}, nil
}

// Close releases the client connection.
func (c *Client) Close() {
	c.connection = nil
}

// API is the subset of Azure DevOps operations the operator relies on.
// It exists so unit tests can substitute a fake implementation.
type API interface {
	// Close releases the underlying HTTP resources (no-op in fakes).
	Close()
	// TestConnection verifies the PAT can reach the organization.
	TestConnection(ctx context.Context) error
	// GetPool resolves a pool by name (case-insensitive).
	GetPool(ctx context.Context, poolName string) (*taskagent.TaskAgentPool, error)
	// ListPools returns every pool visible to the PAT.
	ListPools(ctx context.Context) (*[]taskagent.TaskAgentPool, error)
	// ListAgents returns the agents of a pool. When includeLastCompleted
	// is true each agent also carries its last completed request (used to
	// derive LastActive for TTL decisions).
	ListAgents(ctx context.Context, poolID int, includeLastCompleted bool) (*[]taskagent.TaskAgent, error)
	// DeleteAgent unregisters an agent from a pool.
	DeleteAgent(ctx context.Context, poolID, agentID int) error
	// GetAgentRequestsForPool returns every job request of the pool
	// (queued and finished).
	GetAgentRequestsForPool(ctx context.Context, pool *taskagent.TaskAgentPool) (*[]taskagent.TaskAgentJobRequest, error)
	// GetQueuedJobsByCapability counts queued jobs per demanded capability.
	GetQueuedJobsByCapability(ctx context.Context, pool *taskagent.TaskAgentPool) (map[string]int, error)
}

var _ API = (*Client)(nil)
