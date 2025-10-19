/*
Copyright 2025 mahmoudk1000.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RunnerPoolSpec defines the desired state of a RunnerPool.
type RunnerPoolSpec struct {
	// AzURL is the Azure DevOps organization URL.
	// Examples:
	//   Cloud:      https://dev.azure.com/my-org
	//   On-prem:    https://tfs.company.com/tfs/CollectionName
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^https?://[a-zA-Z0-9.-]+(:[0-9]{1,5})?(/.*)?$`
	AzURL string `json:"azUrl"`

	// Pool is the Azure DevOps agent pool name.
	// +kubebuilder:default="default"
	Pool string `json:"pool"`

	// PATSecretName is the name of the Kubernetes secret holding the
	// Azure DevOps personal access token.
	//
	// +kubebuilder:validation:Required
	PATSecretName string `json:"patSecretName"`

	// PATSecretKey is the key inside PATSecretName that holds the token.
	// +kubebuilder:default="token"
	PATSecretKey string `json:"patSecretKey"`

	// Image is the container image used for runner agent pods.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[^\s]+$`
	Image string `json:"image"`

	// ImagePullPolicy for agent pods.
	// +kubebuilder:default="IfNotPresent"
	// +kubebuilder:validation:Enum=Always;Never;IfNotPresent
	ImagePullPolicy string `json:"imagePullPolicy,omitempty"`

	// MaxAgents is the maximum number of agents that may run for this pool.
	// +kubebuilder:default=5
	// +kubebuilder:validation:Minimum=1
	MaxAgents int `json:"maxAgents,omitempty"`

	// MinAgents is the minimum number of agents that are always kept running.
	// +kubebuilder:default=0
	// +kubebuilder:validation:Minimum=0
	MinAgents int `json:"minAgents,omitempty"`

	// TtlIdleSeconds controls when agents are removed after they stop
	// working. 0 means "immediately" (one-shot agents: a completed agent is
	// deleted right away and idle agents are deleted as soon as the queue
	// is empty). Positive values keep agents around for that many seconds
	// so they can pick up follow-up jobs.
	//
	// +kubebuilder:default=300
	// +kubebuilder:validation:Minimum=0
	TtlIdleSeconds int `json:"ttlIdleSeconds,omitempty"`

	// PollIntervalSeconds is how often the operator polls Azure DevOps for
	// queue state. Must be at least 5 seconds.
	//
	// +kubebuilder:default=5
	// +kubebuilder:validation:Minimum=5
	PollIntervalSeconds int `json:"pollIntervalSeconds,omitempty"`

	// CapabilityAware enables per-capability autoscaling.
	// When true, the operator inspects queued jobs for required capabilities
	// and deploys agents with matching images from CapabilityImages.
	CapabilityAware bool `json:"capabilityAware,omitempty"`

	// Capabilities are registered with the agent as default
	// capabilities (e.g. {"os": "linux"}). They are passed to the agent
	// as Agent.<name> environment variables so the agent registers them
	// with Azure DevOps. In non-capability-aware mode these are ignored.
	Capabilities map[string]string `json:"capabilities,omitempty"`

	// CapabilityImages maps capability names to agent container images.
	// When capabilityAware is true, the operator creates agents with specific
	// images based on what capabilities the jobs in the queue demand.
	// Example: capabilityImages: {docker: "my-registry/agent:docker", kubernetes: "my-registry/agent:k8s"}
	CapabilityImages map[string]string `json:"capabilityImages,omitempty"`

	// InitContainerSpec configures an init container that fixes volume
	// permissions before the agent starts (it runs as root and chowns the
	// mounted PVCs to the agent user).
	InitContainerSpec InitContainerSpec `json:"initContainerSpec,omitempty"`

	// SecurityContext controls the user the agent runs as.
	SecurityContext SecurityContext `json:"securityContext,omitempty"`

	// CertTrustStore lists TLS secrets to mount into the agent as trusted
	// certificates at /etc/ssl/certs/<secretName>.crt (the secret must
	// contain a tls.crt key).
	CertTrustStore []CertTrustStore `json:"certTrustStore,omitempty"`

	// ExtraEnv holds additional environment variables for the agent container.
	ExtraEnv []ExtraEnv `json:"extraEnv,omitempty"`

	// Storage configures persistent volumes mounted into agent pods.
	Storage []StorageSpec `json:"storage,omitempty"`
}

// InitContainerSpec configures the permission-fixing init container.
type InitContainerSpec struct {
	// Image used for the init container.
	Image string `json:"image,omitempty"`
}

// SecurityContext defines the user/group the agent runs as.
type SecurityContext struct {
	// +kubebuilder:default=1001
	// +kubebuilder:validation:Minimum=0
	RunAsUser int64 `json:"runAsUser,omitempty"`

	// +kubebuilder:default=1001
	// +kubebuilder:validation:Minimum=0
	RunAsGroup int64 `json:"runAsGroup,omitempty"`

	// +kubebuilder:validation:Minimum=0
	FSGroup int64 `json:"fsGroup,omitempty"`

	// Privileged runs the agent container in privileged mode.
	Privileged bool `json:"privileged,omitempty"`
}

// CertTrustStore references a TLS secret whose tls.crt key is mounted
// into the agent as a trusted certificate.
type CertTrustStore struct {
	// SecretName is the name of the TLS secret in the RunnerPool namespace.
	SecretName string `json:"secretName"`
}

// ExtraEnv is an extra environment variable for the agent container.
// Exactly one of Value or ValueFrom must be set.
type ExtraEnv struct {
	// +kubebuilder:validation:Pattern=`^[A-Za-z_][A-Za-z0-9_]*$`
	Name string `json:"name"`

	Value *string `json:"value,omitempty"`

	ValueFrom *ValueFromSource `json:"valueFrom,omitempty"`
}

// ValueFromSource selects the value from a secret or config map.
type ValueFromSource struct {
	SecretKeyRef    *SecretKeyRef    `json:"secretKeyRef,omitempty"`
	ConfigMapKeyRef *ConfigMapKeyRef `json:"configMapKeyRef,omitempty"`
}

// SecretKeyRef selects a key from a secret.
type SecretKeyRef struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// ConfigMapKeyRef selects a key from a config map.
// The key defaults to the environment variable name when omitted.
type ConfigMapKeyRef struct {
	Name string `json:"name"`
	Key  string `json:"key,omitempty"`
}

// StorageSpec configures a persistent volume for agent pods.
type StorageSpec struct {
	// Name is the logical name of the volume. The PVC backing it is named
	// <pool>-agent-<index>-<name>.
	Name string `json:"name"`

	// MountPath is where the volume is mounted inside the agent container.
	// Must be an absolute path.
	// +kubebuilder:validation:Pattern=`^\/[a-zA-Z0-9\/._-]+$`
	MountPath string `json:"mountPath"`

	// Size is the requested storage size (e.g. "10Gi", "500Mi").
	// Required when CreatePvc is true.
	Size string `json:"size,omitempty"`

	// StorageClass is the storage class used when the operator creates the PVC.
	StorageClass string `json:"storageClass,omitempty"`

	// ClaimName references an existing PVC to mount instead of creating one.
	ClaimName string `json:"claimName,omitempty"`

	// CreatePvc tells the operator to create the backing PVC when it does
	// not exist yet. Existing PVCs with matching labels are reused.
	//
	// +kubebuilder:default=true
	CreatePvc bool `json:"createPvc,omitempty"`

	// Optional lets pod creation proceed when the PVC cannot be created.
	Optional bool `json:"optional,omitempty"`

	// DeleteWithAgent deletes the PVC when its agent is removed.
	// Ignored when CreatePvc is false.
	DeleteWithAgent bool `json:"deleteWithAgent,omitempty"`
}

// ConditionType represents the possible condition types for a RunnerPool.
type ConditionType string

const (
	// ConditionTypeReady indicates the RunnerPool is connected and agents are managed.
	ConditionTypeReady ConditionType = "Ready"
	// ConditionTypeError indicates the RunnerPool has a connection or reconciliation error.
	ConditionTypeError ConditionType = "Error"
)

// AgentStatus describes a single Azure DevOps agent of this pool.
type AgentStatus struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	// Status is the agent state as reported by Azure DevOps
	// (online, offline, running).
	Status string `json:"status"`

	CreatedAt  *metav1.Time `json:"createdAt,omitempty"`
	LastActive *metav1.Time `json:"lastActive,omitempty"`
}

// AgentIndexInfo tracks what the operator knows about one agent index.
type AgentIndexInfo struct {
	PodName    string       `json:"podName"`
	Status     string       `json:"status"`
	IsMinAgent bool         `json:"isMinAgent"`
	CreatedAt  *metav1.Time `json:"createdAt,omitempty"`
	PvcNames   []string     `json:"pvcNames,omitempty"`
}

// RunnerPoolStatus defines the observed state of a RunnerPool.
type RunnerPoolStatus struct {
	// Conditions represent the latest available observations of the
	// RunnerPool state.
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ConnectionStatus is Connected, Disconnected or Error.
	ConnectionStatus string `json:"connectionStatus,omitempty"`

	// OrganizationName is the Azure DevOps organization derived from spec.azUrl.
	OrganizationName string `json:"organizationName,omitempty"`

	// PoolName is the Azure DevOps pool this RunnerPool manages.
	PoolName string `json:"poolName,omitempty"`

	// Active is true when the pool is connected and being reconciled.
	Active bool `json:"active,omitempty"`

	// QueuedJobs is the number of jobs currently waiting in the pool queue.
	QueuedJobs int `json:"queuedJobs,omitempty"`

	// RunningAgents is the number of operator-managed agents registered
	// in Azure DevOps.
	RunningAgents int `json:"runningAgents,omitempty"`

	// AgentsSummary is "<agents>/<maxAgents>" for a quick kubectl glance.
	AgentsSummary string `json:"agentsSummary,omitempty"`

	// CurrentAgentIndex is the next agent index that would be allocated.
	CurrentAgentIndex int `json:"currentAgentIndex,omitempty"`

	LastPolled *metav1.Time `json:"lastPolled,omitempty"`
	LastError  string       `json:"lastError,omitempty"`

	// Agents is the list of operator-managed agents in the pool.
	Agents []AgentStatus `json:"agents,omitempty"`

	// AgentIndexes maps each in-use agent index (as a string) to its pod
	// and PVCs.
	AgentIndexes map[string]AgentIndexInfo `json:"agentIndexes,omitempty"`

	// QueuedJobsByCap counts queued jobs per demanded capability.
	QueuedJobsByCap map[string]int `json:"queuedJobsByCap,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.connectionStatus`
// +kubebuilder:printcolumn:name="Pool",type=string,JSONPath=`.spec.pool`
// +kubebuilder:printcolumn:name="Organization",type=string,JSONPath=`.status.organizationName`
// +kubebuilder:printcolumn:name="Queued",type=integer,JSONPath=`.status.queuedJobs`
// +kubebuilder:printcolumn:name="Agents",type=string,JSONPath=`.status.agentsSummary`
// +kubebuilder:printcolumn:name="Running",type=integer,JSONPath=`.status.runningAgents`

// RunnerPool is the Schema for the runnerpools API.
type RunnerPool struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RunnerPoolSpec   `json:"spec,omitempty"`
	Status RunnerPoolStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RunnerPoolList contains a list of RunnerPool.
type RunnerPoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RunnerPool `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RunnerPool{}, &RunnerPoolList{})
}
