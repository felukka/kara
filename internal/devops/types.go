package devops

import "time"

// Pool represents an Azure DevOps agent pool
// This corresponds to the pool structure returned by Azure DevOps REST API
type Pool struct {
	// ID is the unique identifier for the pool
	ID int `json:"id"`

	// Name is the display name of the pool
	Name string `json:"name"`

	// IsHosted indicates whether this is a Microsoft-hosted pool
	IsHosted bool `json:"isHosted"`

	// PoolType describes the type of pool (automation, deployment, etc.)
	PoolType string `json:"poolType"`

	// Size is the number of agents in the pool
	Size int `json:"size"`
}

// Agent represents an Azure DevOps build agent
// An agent is a compute resource that runs jobs from Azure Pipelines
type Agent struct {
	// ID is the unique identifier for the agent
	ID int `json:"id"`

	// Name is the name of the agent (e.g., "my-pool-agent-0")
	Name string `json:"name"`

	// Version is the agent software version
	Version string `json:"version"`

	// Status indicates the current state: "online", "offline", "running"
	Status string `json:"status"`

	// Enabled indicates whether the agent is enabled to accept jobs
	Enabled bool `json:"enabled"`

	// CreatedOn is when the agent was first registered
	CreatedOn *time.Time `json:"createdOn,omitempty"`

	// LastActive is the last time the agent was active
	LastActive *time.Time `json:"lastActive,omitempty"`
}

// JobRequest represents a queued job in Azure DevOps
// These are pipeline jobs waiting to be assigned to an agent
type JobRequest struct {
	// RequestID is the unique identifier for the job request
	RequestID int `json:"requestId"`

	// QueueTime is when the job was queued
	QueueTime time.Time `json:"queueTime"`

	// AssignTime is when the job was assigned to an agent (if assigned)
	AssignTime *time.Time `json:"assignTime,omitempty"`

	// ReceiveTime is when the agent received the job
	ReceiveTime *time.Time `json:"receiveTime,omitempty"`

	// FinishTime is when the job completed
	FinishTime *time.Time `json:"finishTime,omitempty"`

	// Result is the job result: "succeeded", "failed", "canceled", null if not finished
	Result *string `json:"result,omitempty"`

	// AgentID is the ID of the agent running this job (if assigned)
	AgentID *int `json:"reservedAgent,omitempty"`

	// Demands are the capabilities required by this job
	// Example: ["Agent.Version -gtVersion 2.0", "java"]
	Demands []string `json:"demands,omitempty"`

	// Definition contains information about the pipeline definition
	Definition *JobDefinition `json:"definition,omitempty"`
}

// JobDefinition contains information about the pipeline that created the job
type JobDefinition struct {
	// ID is the pipeline definition ID
	ID int `json:"id"`

	// Name is the pipeline name
	Name string `json:"name"`
}

// PoolsResponse is the response structure when listing pools
// Azure DevOps API returns paginated results in this format
type PoolsResponse struct {
	// Count is the number of pools in this response
	Count int `json:"count"`

	// Value is the array of pools
	Value []Pool `json:"value"`
}

// AgentsResponse is the response structure when listing agents
type AgentsResponse struct {
	// Count is the number of agents in this response
	Count int `json:"count"`

	// Value is the array of agents
	Value []Agent `json:"value"`
}

// JobRequestsResponse is the response structure when listing job requests
type JobRequestsResponse struct {
	// Count is the number of job requests in this response
	Count int `json:"count"`

	// Value is the array of job requests
	Value []JobRequest `json:"value"`
}
