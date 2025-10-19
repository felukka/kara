package controller

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/microsoft/azure-devops-go-api/azuredevops/v7/taskagent"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	controllerutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	karav1 "github.com/mahmoudk1000/azdo-runner-operator/api/v1"
	"github.com/mahmoudk1000/azdo-runner-operator/internal/devops"
	"github.com/mahmoudk1000/azdo-runner-operator/internal/runners"
)

const (
	finalizerName     = "kara.felukka.org/finalizer"
	labelJobRequestID = "job-request-id"
)

// RunnerPoolReconciler reconciles a RunnerPool object.
type RunnerPoolReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Runner *runners.Runner

	// AzDoClientFactory creates the Azure DevOps client used for one
	// reconcile pass (the PAT is per pool, so no client is shared).
	// Defaults to devops.NewClient when nil.
	AzDoClientFactory func(organizationURL, personalAccessToken string) (devops.API, error)
}

// poolState is the shared snapshot of Azure DevOps + Kubernetes state for
// one reconcile pass. Everything is fetched once and reused by the
// cleanup/scale steps (the C# original re-fetched job requests and agents
// several times per cycle).
type poolState struct {
	azdoPool    *taskagent.TaskAgentPool
	jobRequests []taskagent.TaskAgentJobRequest
	queuedJobs  int
	queuedByCap map[string]int
	agents      []taskagent.TaskAgent
	allPods     []corev1.Pod
	activePods  []corev1.Pod
	minPods     []corev1.Pod
}

// +kubebuilder:rbac:groups=kara.felukka.org,resources=runnerpools,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kara.felukka.org,resources=runnerpools/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kara.felukka.org,resources=runnerpools/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses,verbs=get;list;watch

// Reconcile drives one poll+scale pass for a RunnerPool and requeues
// after spec.pollIntervalSeconds for the next pass (mirroring the C#
// background polling service).
func (r *RunnerPoolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	pool := &karav1.RunnerPool{}
	if err := r.Get(ctx, req.NamespacedName, pool); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !pool.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(pool, finalizerName) {
			if err := r.finalizePool(ctx, pool); err != nil {
				return ctrl.Result{}, err
			}
			controllerutil.RemoveFinalizer(pool, finalizerName)
			if err := r.Update(ctx, pool); err != nil {
				return ctrl.Result{}, err
			}
			logger.Info("Finalized RunnerPool", "name", pool.Name)
		}
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(pool, finalizerName) {
		controllerutil.AddFinalizer(pool, finalizerName)
		if err := r.Update(ctx, pool); err != nil {
			return ctrl.Result{}, err
		}
	}

	return r.reconcilePool(ctx, pool)
}

// newAzDoClient returns the configured factory or the default one.
func (r *RunnerPoolReconciler) newAzDoClient(organizationURL, pat string) (devops.API, error) {
	if r.AzDoClientFactory != nil {
		return r.AzDoClientFactory(organizationURL, pat)
	}
	c, err := devops.NewClient(organizationURL, pat)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (r *RunnerPoolReconciler) getPersonalAccessToken(ctx context.Context, pool *karav1.RunnerPool) (string, error) {
	secret := &corev1.Secret{}
	key := client.ObjectKey{Namespace: pool.Namespace, Name: pool.Spec.PATSecretName}
	if err := r.Get(ctx, key, secret); err != nil {
		return "", fmt.Errorf("get secret %s/%s: %w", pool.Namespace, pool.Spec.PATSecretName, err)
	}
	token, ok := secret.Data[pool.Spec.PATSecretKey]
	if !ok || len(token) == 0 {
		return "", fmt.Errorf("secret %s has no key %q", pool.Spec.PATSecretName, pool.Spec.PATSecretKey)
	}
	return string(token), nil
}

func pollIntervalSeconds(v int) int {
	if v < 5 {
		return 5
	}
	return v
}

// reconcilePool performs one full poll+scale pass.
func (r *RunnerPoolReconciler) reconcilePool(ctx context.Context, pool *karav1.RunnerPool) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	requeue := time.Duration(pollIntervalSeconds(pool.Spec.PollIntervalSeconds)) * time.Second

	pat, err := r.getPersonalAccessToken(ctx, pool)
	if err != nil {
		logger.Error(err, "Failed to read PAT secret, marking pool disconnected")
		r.setStatusDisconnected(ctx, pool, err.Error())
		return ctrl.Result{RequeueAfter: requeue}, nil
	}

	azdo, err := r.newAzDoClient(pool.Spec.AzURL, pat)
	if err != nil {
		logger.Error(err, "Failed to create Azure DevOps client, marking pool disconnected")
		r.setStatusDisconnected(ctx, pool, err.Error())
		return ctrl.Result{RequeueAfter: requeue}, nil
	}
	defer azdo.Close()

	if err := azdo.TestConnection(ctx); err != nil {
		logger.Error(err, "Azure DevOps connection test failed, marking pool disconnected")
		r.setStatusDisconnected(ctx, pool, err.Error())
		return ctrl.Result{RequeueAfter: requeue}, nil
	}

	azdoPool, err := azdo.GetPool(ctx, pool.Spec.Pool)
	if err != nil {
		logger.Error(err, "Failed to resolve Azure DevOps pool, marking pool disconnected")
		r.setStatusDisconnected(ctx, pool, err.Error())
		return ctrl.Result{RequeueAfter: requeue}, nil
	}

	state, err := r.fetchState(ctx, azdo, pool, azdoPool)
	if err != nil {
		logger.Error(err, "Failed to fetch pool state, marking pool disconnected")
		r.setStatusDisconnected(ctx, pool, err.Error())
		return ctrl.Result{RequeueAfter: requeue}, nil
	}

	logger.Info("Pool state",
		"queuedJobs", state.queuedJobs,
		"agents", len(state.agents),
		"activePods", len(state.activePods),
		"allPods", len(state.allPods))

	// Cleanup and scaling steps log their own errors and continue,
	// matching the C# behavior of never letting one bad agent poison
	// the whole cycle.
	r.cleanupCompletedJobLabels(ctx, pool, state)
	r.cleanupCompletedAgents(ctx, azdo, pool, state)
	r.cleanupIdleAgents(ctx, azdo, pool, state)
	r.ensureMinimumAgents(ctx, azdo, pool, state)
	if state.queuedJobs > 0 {
		r.optimizeMinAgentsForCapabilities(ctx, azdo, pool, state)
	}
	r.ensureMaximumAgentsLimit(ctx, azdo, pool, state)
	if state.queuedJobs > 0 {
		// Fresh pod list after cleanup operations (C# parity).
		if active, err := r.Runner.GetActivePods(ctx, pool); err == nil {
			state.activePods = active
		}
		r.scaleUpForQueuedWork(ctx, pool, state)
	}

	r.updateStatusConnected(ctx, pool, state)
	return ctrl.Result{RequeueAfter: requeue}, nil
}

// fetchState pulls the Azure DevOps + Kubernetes state shared by every
// cleanup/scale step of this pass.
func (r *RunnerPoolReconciler) fetchState(ctx context.Context, azdo devops.API, pool *karav1.RunnerPool, azdoPool *taskagent.TaskAgentPool) (*poolState, error) {
	jobRequests, err := azdo.GetAgentRequestsForPool(ctx, azdoPool)
	if err != nil {
		return nil, fmt.Errorf("get job requests: %w", err)
	}
	agents, err := azdo.ListAgents(ctx, *azdoPool.Id, true)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	allPods, err := r.Runner.GetAllRunnerPods(ctx, pool)
	if err != nil {
		return nil, fmt.Errorf("list runner pods: %w", err)
	}
	activePods, err := r.Runner.GetActivePods(ctx, pool)
	if err != nil {
		return nil, fmt.Errorf("list active pods: %w", err)
	}
	minPods, err := r.Runner.GetMinAgentPods(ctx, pool)
	if err != nil {
		return nil, fmt.Errorf("list min agent pods: %w", err)
	}

	return &poolState{
		azdoPool:    azdoPool,
		jobRequests: *jobRequests,
		queuedJobs:  devops.CountQueued(*jobRequests),
		queuedByCap: devops.CountQueuedByCapability(*jobRequests),
		agents:      *agents,
		allPods:     allPods,
		activePods:  activePods,
		minPods:     minPods,
	}, nil
}

// ---------------------------------------------------------------------------
// poolState helpers
// ---------------------------------------------------------------------------

// jobRequestID returns the build request ID of a job request as a string.
func jobRequestID(j taskagent.TaskAgentJobRequest) string {
	if j.RequestId == nil {
		return ""
	}
	return strconv.FormatUint(*j.RequestId, 10)
}

// agentByName finds an agent by its name.
func (s *poolState) agentByName(name string) *taskagent.TaskAgent {
	for i := range s.agents {
		if devops.AgentName(s.agents[i]) == name {
			return &s.agents[i]
		}
	}
	return nil
}

// jobIsActive reports whether the job with the given build request ID
// still exists and has no terminal result.
func (s *poolState) jobIsActive(jobID string) bool {
	for _, j := range s.jobRequests {
		if jobRequestID(j) == jobID && devops.IsQueued(j) {
			return true
		}
	}
	return false
}

// agentIsBusy reports whether the agent is reserved for a job that has
// not completed yet.
func (s *poolState) agentIsBusy(agentID int) bool {
	for _, j := range s.jobRequests {
		if !devops.IsQueued(j) {
			continue
		}
		if j.ReservedAgent != nil && j.ReservedAgent.Id != nil && *j.ReservedAgent.Id == agentID {
			return true
		}
	}
	return false
}

// agentHasActiveJob combines the two C# checks: the agent's reserved
// job, and the pod's job-request-id label pointing at a live job.
func (s *poolState) agentHasActiveJob(pod *corev1.Pod, agent *taskagent.TaskAgent) bool {
	if agent != nil && s.agentIsBusy(devops.AgentID(*agent)) {
		return true
	}
	if pod != nil {
		if jobID := pod.Labels[labelJobRequestID]; jobID != "" && s.jobIsActive(jobID) {
			return true
		}
	}
	return false
}

// completedPodProtected reports whether a completed (Succeeded/Failed)
// pod must not be deleted yet: only when the corresponding agent is
// alive (online/running) and still holds an active job. A pod whose
// agent is already offline (or gone) is stuck, not working: deleting it
// lets the stuck-job/offline cleanup re-queue the work (this is what the
// C# ErrorPodCleanupService's unconditional Failed-pod sweep provided).
func (s *poolState) completedPodProtected(pod *corev1.Pod, agent *taskagent.TaskAgent) bool {
	live := false
	if agent != nil {
		st := devops.AgentStatus(*agent)
		live = st == "online" || st == "running"
	}
	if !live {
		return false
	}
	return s.agentHasActiveJob(pod, agent)
}

// unregisterAgent removes an agent from Azure DevOps when it belongs to
// this operator. Failures are logged, never fatal (C# parity).
func (r *RunnerPoolReconciler) unregisterAgent(ctx context.Context, azdo devops.API, pool *karav1.RunnerPool, s *poolState, agentName string) {
	logger := log.FromContext(ctx)
	agent := s.agentByName(agentName)
	if agent == nil {
		logger.Info("No corresponding Azure DevOps agent found, proceeding with pod deletion only", "agent", agentName)
		return
	}
	if !devops.IsOperatorManagedAgentName(agentName, pool.Name) {
		logger.Info("Skipping Azure DevOps unregistration - agent is not operator managed", "agent", agentName)
		return
	}
	if err := azdo.DeleteAgent(ctx, *s.azdoPool.Id, devops.AgentID(*agent)); err != nil {
		logger.Error(err, "Failed to unregister agent from Azure DevOps (continuing with pod deletion)", "agent", agentName)
		return
	}
	logger.Info("Unregistered agent from Azure DevOps", "agent", agentName, "id", devops.AgentID(*agent))
}

// podAge returns how long the pod has been running.
func podAge(pod corev1.Pod) time.Duration {
	return time.Since(pod.CreationTimestamp.Time)
}

// ---------------------------------------------------------------------------
// 1. Job label cleanup (C# CleanupCompletedJobLabelsAsync)
// ---------------------------------------------------------------------------

func (r *RunnerPoolReconciler) cleanupCompletedJobLabels(ctx context.Context, pool *karav1.RunnerPool, s *poolState) {
	logger := log.FromContext(ctx)

	for i := range s.allPods {
		pod := s.allPods[i]
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		jobID, hasLabel := pod.Labels[labelJobRequestID]
		if !hasLabel || jobID == "" {
			continue
		}

		job := &taskagent.TaskAgentJobRequest{}
		found := false
		for _, j := range s.jobRequests {
			if jobRequestID(j) == jobID {
				*job = j
				found = true
				break
			}
		}

		// Clear the label when the job completed or no longer exists,
		// so the agent can be reused for another job.
		if (!found || job.Result != nil) && r != nil {
			if err := r.Runner.RemovePodLabel(ctx, pool, pod.Name, labelJobRequestID); err != nil {
				logger.Error(err, "Failed to clear job-request-id label", "pod", pod.Name)
				continue
			}
			if found {
				logger.Info("Cleared job-request-id label from pod, job completed", "pod", pod.Name, "job", jobID, "result", string(*job.Result))
			} else {
				logger.Info("Cleared job-request-id label from pod, job no longer exists", "pod", pod.Name, "job", jobID)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 2. Completed agent/pod cleanup (C# CleanupCompletedAgentsAsync)
// ---------------------------------------------------------------------------

func (r *RunnerPoolReconciler) cleanupCompletedAgents(ctx context.Context, azdo devops.API, pool *karav1.RunnerPool, s *poolState) {
	logger := log.FromContext(ctx)
	ttl := pool.Spec.TtlIdleSeconds

	for i := range s.allPods {
		completedPod := s.allPods[i]
		if completedPod.Status.Phase != corev1.PodSucceeded && completedPod.Status.Phase != corev1.PodFailed {
			continue
		}

		agent := s.agentByName(completedPod.Name)

		// Safety: never delete a pod whose live agent still has an active job.
		if s.completedPodProtected(&completedPod, agent) {
			logger.Info("PROTECTED: not cleaning up completed pod because agent still has an active job",
				"pod", completedPod.Name, "phase", string(completedPod.Status.Phase))
			continue
		}

		shouldCleanup := false
		reason := ""
		if ttl == 0 {
			shouldCleanup = true
			reason = "TTL=0 (immediate cleanup after completion)"
		} else if !completedPod.CreationTimestamp.IsZero() {
			if time.Now().After(completedPod.CreationTimestamp.Add(time.Duration(ttl) * time.Second)) {
				shouldCleanup = true
				reason = fmt.Sprintf("pod in %s state for more than %ds", completedPod.Status.Phase, ttl)
			} else {
				logger.V(1).Info("Keeping completed pod within TTL", "pod", completedPod.Name, "ttl", ttl)
			}
		}

		if !shouldCleanup {
			continue
		}

		if agent != nil {
			r.unregisterAgent(ctx, azdo, pool, s, devops.AgentName(*agent))
		} else {
			logger.Info("No corresponding Azure DevOps agent found for completed pod, deleting pod only", "pod", completedPod.Name)
		}

		// Delete the pod (and its deleteWithAgent PVCs).
		index := r.Runner.GetAgentIndex(pool, completedPod.Name)
		if err := r.deleteAgent(ctx, pool, index, completedPod.Name); err != nil {
			logger.Error(err, "Failed to clean up completed agent pod", "pod", completedPod.Name)
			continue
		}
		logger.Info("Deleted completed agent pod after TTL", "pod", completedPod.Name, "reason", reason)
	}

	// Clean up offline operator-managed agents without active pods.
	for i := range s.agents {
		agent := s.agents[i]
		if devops.AgentStatus(agent) != "offline" {
			continue
		}
		if !devops.IsOperatorManagedAgentName(devops.AgentName(agent), pool.Name) {
			continue
		}
		if s.hasActivePodFor(devops.AgentName(agent)) {
			continue
		}

		// An offline operator-managed agent without an active pod is not
		// making progress (its heartbeat is gone). Unregister it so Azure
		// DevOps can re-queue any job it held and the pool does not
		// accumulate zombie agents. C# unregistered all of these; the
		// job check below only feeds the log message.
		if stuck := s.busyJobOf(devops.AgentID(agent)); stuck != nil {
			logger.Info("Unregistering offline agent that held a job (job will be re-queued by Azure DevOps)",
				"agent", devops.AgentName(agent), "job", jobRequestID(*stuck))
		} else {
			logger.Info("Cleaning up offline agent with no active pod", "agent", devops.AgentName(agent))
		}
		r.unregisterAgent(ctx, azdo, pool, s, devops.AgentName(agent))
	}
}

// busyJobOf returns the first unfinished job reserved for the agent.
func (s *poolState) busyJobOf(agentID int) *taskagent.TaskAgentJobRequest {
	for i := range s.jobRequests {
		j := s.jobRequests[i]
		if !devops.IsQueued(j) {
			continue
		}
		if j.ReservedAgent != nil && j.ReservedAgent.Id != nil && *j.ReservedAgent.Id == agentID {
			return &s.jobRequests[i]
		}
	}
	return nil
}

// hasActivePodFor reports whether a pod exists (any phase) for the agent
// name. Completed pods count too: they are handled by the completed-agent
// cleanup pass, so the offline-agent sweep must not touch their agent.
func (s *poolState) hasActivePodFor(agentName string) bool {
	for i := range s.allPods {
		if s.allPods[i].Name == agentName {
			return true
		}
	}
	return false
}

// deleteAgent deletes an agent pod by index (and its deleteWithAgent
// PVCs), falling back to deletion by name when the index cannot be
// parsed.
func (r *RunnerPoolReconciler) deleteAgent(ctx context.Context, pool *karav1.RunnerPool, index int, podName string) error {
	if index >= 0 {
		return r.Runner.DeleteAgentWithResources(ctx, pool, index)
	}
	return r.Runner.DeletePodByName(ctx, pool, podName)
}

// ---------------------------------------------------------------------------
// 3. Idle agent cleanup (C# CleanupIdleAgentsAsync)
// ---------------------------------------------------------------------------

func (r *RunnerPoolReconciler) cleanupIdleAgents(ctx context.Context, azdo devops.API, pool *karav1.RunnerPool, s *poolState) {
	logger := log.FromContext(ctx)
	ttl := pool.Spec.TtlIdleSeconds
	if ttl < 0 {
		ttl = 0
	}

	minAgentNames := map[string]bool{}
	for i := range s.minPods {
		minAgentNames[s.minPods[i].Name] = true
	}

	for i := range s.allPods {
		pod := s.allPods[i]
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		if minAgentNames[pod.Name] {
			continue
		}

		// Grace period: allow time for registration.
		if podAge(pod) < 2*time.Minute {
			logger.V(1).Info("Skipping idle cleanup, pod in registration grace period", "pod", pod.Name)
			continue
		}

		agent := s.agentByName(pod.Name)

		// Never kill an agent that has an active job.
		if s.agentHasActiveJob(&pod, agent) {
			logger.Info("PROTECTED: not cleaning up idle agent, it has an active job", "pod", pod.Name)
			continue
		}

		shouldCleanup := false
		reason := ""

		if ttl == 0 {
			if s.queuedJobs == 0 {
				shouldCleanup = true
				reason = "no queued jobs and TTL=0 (immediate cleanup)"
			}
		} else {
			// Prefer the agent's LastActive time (finish time of its last
			// completed job), fall back to the pod creation time.
			var idleSince time.Time
			if agent != nil && agent.LastCompletedRequest != nil && agent.LastCompletedRequest.FinishTime != nil {
				idleSince = agent.LastCompletedRequest.FinishTime.Time
			} else if !pod.CreationTimestamp.IsZero() {
				idleSince = pod.CreationTimestamp.Time
			}
			if !idleSince.IsZero() && time.Since(idleSince) > time.Duration(ttl)*time.Second {
				shouldCleanup = true
				reason = fmt.Sprintf("idle for more than %ds (since %s)", ttl, idleSince.UTC().Format(time.RFC3339))
			}
		}

		if !shouldCleanup {
			continue
		}

		logger.Info("Cleaning up idle agent", "pod", pod.Name, "reason", reason)
		if agent != nil {
			r.unregisterAgent(ctx, azdo, pool, s, devops.AgentName(*agent))
		}
		index := r.Runner.GetAgentIndex(pool, pod.Name)
		if err := r.deleteAgent(ctx, pool, index, pod.Name); err != nil {
			logger.Error(err, "Failed to clean up idle agent pod", "pod", pod.Name)
			continue
		}
		logger.Info("Cleaned up idle agent pod", "pod", pod.Name)
	}
}

// ---------------------------------------------------------------------------
// 4. Minimum agents (C# EnsureMinimumAgentsAsync)
// ---------------------------------------------------------------------------

func (r *RunnerPoolReconciler) ensureMinimumAgents(ctx context.Context, azdo devops.API, pool *karav1.RunnerPool, s *poolState) {
	logger := log.FromContext(ctx)

	required := pool.Spec.MinAgents
	if required < 0 {
		required = 0
	}
	max := pool.Spec.MaxAgents
	if max < 0 {
		max = 0
	}
	if required > max {
		logger.Info("MinAgents exceeds MaxAgents, adjusting MinAgents to MaxAgents", "minAgents", pool.Spec.MinAgents, "maxAgents", max)
		required = max
	}

	current := len(s.minPods)

	if required == 0 {
		if current > 0 {
			logger.Info("Removing all minimum agents (MinAgents=0)", "current", current)
			r.removeExcessMinimumAgents(ctx, azdo, pool, s, current)
		}
		return
	}

	needed := required - current
	if needed > 0 {
		logger.Info("Creating minimum agents", "needed", needed, "current", current, "required", required)
		for i := 0; i < needed; i++ {
			if err := r.spawnAgent(ctx, pool, s, true, runners.CapabilityBase, nil); err != nil {
				logger.Error(err, "Failed to create minimum agent")
			}
		}
	} else if needed < 0 {
		logger.Info("Removing excess minimum agents", "excess", -needed, "current", current, "required", required)
		r.removeExcessMinimumAgents(ctx, azdo, pool, s, -needed)
	}
}

func (r *RunnerPoolReconciler) removeExcessMinimumAgents(ctx context.Context, azdo devops.API, pool *karav1.RunnerPool, s *poolState, countToRemove int) {
	logger := log.FromContext(ctx)

	// Oldest first.
	sorted := append([]corev1.Pod(nil), s.minPods...)
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].CreationTimestamp.Before(&sorted[i].CreationTimestamp) {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	limit := countToRemove
	if limit > len(sorted) {
		limit = len(sorted)
	}
	for i := 0; i < limit; i++ {
		pod := sorted[i]

		agent := s.agentByName(pod.Name)
		if s.agentHasActiveJob(&pod, agent) {
			logger.Info("Skipping removal of minimum agent, it is running a job", "pod", pod.Name)
			continue
		}
		// 3 minute grace period for min agents (C# parity).
		if podAge(pod) < 3*time.Minute {
			logger.Info("Skipping removal of minimum agent, registration grace period", "pod", pod.Name)
			continue
		}

		if agent != nil {
			r.unregisterAgent(ctx, azdo, pool, s, devops.AgentName(*agent))
		}
		index := r.Runner.GetAgentIndex(pool, pod.Name)
		if err := r.deleteAgent(ctx, pool, index, pod.Name); err != nil {
			logger.Error(err, "Failed to remove excess minimum agent pod", "pod", pod.Name)
			continue
		}
		logger.Info("Removed excess minimum agent pod", "pod", pod.Name)
	}
}

// ---------------------------------------------------------------------------
// 5. Capability-aware min agent optimization (C# OptimizeMinAgentsForCapabilitiesAsync)
// ---------------------------------------------------------------------------

func (r *RunnerPoolReconciler) optimizeMinAgentsForCapabilities(ctx context.Context, azdo devops.API, pool *karav1.RunnerPool, s *poolState) {
	logger := log.FromContext(ctx)

	if !pool.Spec.CapabilityAware || pool.Spec.MinAgents <= 0 {
		return
	}
	if len(s.minPods) == 0 || len(s.queuedByCap) == 0 {
		return
	}

	// Required capabilities: queued jobs grouped by first demand, only
	// for capabilities that have a configured image.
	requiredCapabilities := map[string]int{}
	for _, job := range devops.QueuedJobs(s.jobRequests) {
		capability := devops.FirstDemand(job)
		if capability == "" {
			continue
		}
		if _, ok := pool.Spec.CapabilityImages[capability]; !ok {
			continue
		}
		requiredCapabilities[capability]++
	}
	if len(requiredCapabilities) == 0 {
		return
	}

	currentCapabilities := map[string]bool{}
	for i := range s.minPods {
		cap := s.minPods[i].Labels[runners.LabelCapability]
		if cap == "" {
			cap = runners.CapabilityBase
		}
		currentCapabilities[cap] = true
	}

	var missing []string
	for capability := range requiredCapabilities {
		if !currentCapabilities[capability] {
			missing = append(missing, capability)
		}
	}
	if len(missing) == 0 {
		return
	}

	logger.Info("Optimizing minimum agents for capabilities",
		"required", requiredCapabilities, "current", currentCapabilities, "missing", missing)

	// Replace base min agents with capability-specific ones, oldest first.
	var baseAgents []corev1.Pod
	for i := range s.minPods {
		cap := s.minPods[i].Labels[runners.LabelCapability]
		if cap == "" || cap == runners.CapabilityBase {
			baseAgents = append(baseAgents, s.minPods[i])
		}
	}
	for i := 0; i < len(baseAgents); i++ {
		for j := i + 1; j < len(baseAgents); j++ {
			if baseAgents[j].CreationTimestamp.Before(&baseAgents[i].CreationTimestamp) {
				baseAgents[i], baseAgents[j] = baseAgents[j], baseAgents[i]
			}
		}
	}

	replacements := len(missing)
	if len(baseAgents) < replacements {
		replacements = len(baseAgents)
	}
	if replacements == 0 {
		logger.Info("No base minimum agents available to replace with capability-specific agents")
		return
	}

	logger.Info("Replacing base minimum agents with capability-specific agents", "replacements", replacements)
	for i := 0; i < replacements; i++ {
		capabilityToAdd := missing[i]

		if err := r.spawnAgent(ctx, pool, s, true, capabilityToAdd, nil); err != nil {
			logger.Error(err, "Failed to create capability-specific minimum agent", "capability", capabilityToAdd)
			continue
		}
		logger.Info("Created capability-specific minimum agent", "capability", capabilityToAdd)

		baseAgentToRemove := baseAgents[i]
		agent := s.agentByName(baseAgentToRemove.Name)
		if agent != nil {
			r.unregisterAgent(ctx, azdo, pool, s, devops.AgentName(*agent))
		}
		index := r.Runner.GetAgentIndex(pool, baseAgentToRemove.Name)
		if err := r.deleteAgent(ctx, pool, index, baseAgentToRemove.Name); err != nil {
			logger.Error(err, "Failed to remove base minimum agent", "pod", baseAgentToRemove.Name)
			continue
		}
		logger.Info("Replaced base minimum agent with capability-specific agent",
			"pod", baseAgentToRemove.Name, "capability", capabilityToAdd)
	}
}

// ---------------------------------------------------------------------------
// 6. Maximum agents (C# EnsureMaximumAgentsLimitAsync)
// ---------------------------------------------------------------------------

func (r *RunnerPoolReconciler) ensureMaximumAgentsLimit(ctx context.Context, azdo devops.API, pool *karav1.RunnerPool, s *poolState) {
	logger := log.FromContext(ctx)

	max := pool.Spec.MaxAgents
	if max < 0 {
		max = 0
	}
	if len(s.activePods) <= max {
		return
	}

	excess := len(s.activePods) - max
	logger.Info("Removing excess agents to respect MaxAgents limit", "excess", excess, "active", len(s.activePods), "max", max)

	minAgentNames := map[string]bool{}
	for i := range s.minPods {
		minAgentNames[s.minPods[i].Name] = true
	}

	var nonMin, min []corev1.Pod
	for i := range s.activePods {
		if minAgentNames[s.activePods[i].Name] {
			min = append(min, s.activePods[i])
		} else {
			nonMin = append(nonMin, s.activePods[i])
		}
	}

	// Non-min agents first, oldest first; then min agents if still over.
	nonMin = sortByCreation(nonMin)
	min = sortByCreation(min)

	toRemove := append([]corev1.Pod(nil), nonMin...)
	if len(toRemove) > excess {
		toRemove = toRemove[:excess]
	}
	if len(toRemove) < excess {
		remaining := excess - len(toRemove)
		logger.Info("Need to remove minimum agents to respect MaxAgents limit", "remaining", remaining)
		if remaining > len(min) {
			remaining = len(min)
		}
		toRemove = append(toRemove, min[:remaining]...)
	}

	r.removeExcessAgents(ctx, azdo, pool, s, toRemove)
}

func sortByCreation(pods []corev1.Pod) []corev1.Pod {
	for i := 0; i < len(pods); i++ {
		for j := i + 1; j < len(pods); j++ {
			if pods[j].CreationTimestamp.Before(&pods[i].CreationTimestamp) {
				pods[i], pods[j] = pods[j], pods[i]
			}
		}
	}
	return pods
}

func (r *RunnerPoolReconciler) removeExcessAgents(ctx context.Context, azdo devops.API, pool *karav1.RunnerPool, s *poolState, toRemove []corev1.Pod) {
	logger := log.FromContext(ctx)

	for i := range toRemove {
		pod := toRemove[i]

		// 2 minute registration grace period.
		if podAge(pod) < 2*time.Minute {
			logger.Info("Skipping removal for MaxAgents compliance, registration grace period", "pod", pod.Name)
			continue
		}

		jobID := pod.Labels[labelJobRequestID]
		if jobID != "" && s.jobIsActive(jobID) {
			logger.Info("Skipping removal, pod linked to an active or queued job", "pod", pod.Name, "job", jobID)
			continue
		}

		agent := s.agentByName(pod.Name)
		if agent != nil && s.agentIsBusy(devops.AgentID(*agent)) {
			logger.Info("Skipping removal, agent is running a job", "pod", pod.Name)
			continue
		}

		if agent != nil {
			agentType := "regular"
			if pod.Labels[runners.LabelMinAgent] == "true" {
				agentType = "minimum"
			}
			logger.Info("Unregistering excess agent for MaxAgents compliance", "pod", pod.Name, "type", agentType)
			r.unregisterAgent(ctx, azdo, pool, s, devops.AgentName(*agent))
		}

		index := r.Runner.GetAgentIndex(pool, pod.Name)
		if err := r.deleteAgent(ctx, pool, index, pod.Name); err != nil {
			logger.Error(err, "Failed to remove excess agent pod", "pod", pod.Name)
			continue
		}
		logger.Info("Removed excess agent pod for MaxAgents compliance", "pod", pod.Name)
	}
}

// ---------------------------------------------------------------------------
// 7. Scale up (C# ScaleUpForQueuedWorkAsync)
// ---------------------------------------------------------------------------

func (r *RunnerPoolReconciler) scaleUpForQueuedWork(ctx context.Context, pool *karav1.RunnerPool, s *poolState) {
	logger := log.FromContext(ctx)

	var operatorManagedAgents []taskagent.TaskAgent
	for i := range s.agents {
		if devops.IsOperatorManagedAgentName(devops.AgentName(s.agents[i]), pool.Name) {
			operatorManagedAgents = append(operatorManagedAgents, s.agents[i])
		}
	}

	// Jobs not reserved to any agent and not already backed by a pod
	// with their job-request-id label. Jobs already reserved to an agent
	// (operator or external) are in flight; if that agent dies, the
	// offline/stuck cleanup re-queues the work.
	var jobsWithoutAgentOrPod []taskagent.TaskAgentJobRequest
	for _, job := range s.jobRequests {
		if !devops.IsQueued(job) {
			continue
		}
		if job.ReservedAgent != nil && job.ReservedAgent.Id != nil {
			continue
		}
		if s.hasPodForJob(jobRequestID(job)) {
			continue
		}
		jobsWithoutAgentOrPod = append(jobsWithoutAgentOrPod, job)
	}

	// Try to reuse idle agents first.
	var jobsToSpawn []taskagent.TaskAgentJobRequest
	for i := range jobsWithoutAgentOrPod {
		if !r.tryReuseIdleAgent(ctx, pool, s, jobsWithoutAgentOrPod[i]) {
			jobsToSpawn = append(jobsToSpawn, jobsWithoutAgentOrPod[i])
		}
	}

	totalAgentCount := len(operatorManagedAgents) + len(s.allPods)
	availableSlots := pool.Spec.MaxAgents - totalAgentCount
	if availableSlots < 0 {
		availableSlots = 0
	}
	if len(jobsToSpawn) > availableSlots {
		jobsToSpawn = jobsToSpawn[:availableSlots]
	}

	if len(jobsToSpawn) > 0 {
		logger.Info("PENDING WORK DETECTED: spawning agents for unassigned queued jobs",
			"needed", len(jobsToSpawn), "unassigned", len(jobsWithoutAgentOrPod), "maxAgents", pool.Spec.MaxAgents, "total", totalAgentCount)

		for i := range jobsToSpawn {
			job := jobsToSpawn[i]
			if s.hasPodForJob(jobRequestID(job)) {
				logger.Info("Pod already exists for job, skipping spawn", "job", jobRequestID(job))
				continue
			}
			extraLabels := map[string]string{labelJobRequestID: jobRequestID(job)}
			if pool.Spec.CapabilityAware {
				r.spawnCapabilityAwareAgent(ctx, pool, s, job, extraLabels)
			} else {
				if err := r.spawnAgent(ctx, pool, s, false, runners.CapabilityBase, extraLabels); err != nil {
					logger.Error(err, "Failed to spawn agent for job", "job", jobRequestID(job))
				}
			}
		}
	} else if len(jobsWithoutAgentOrPod) == 0 {
		logger.V(1).Info("No new agents needed: all queued jobs are already assigned or pods are starting up")
	} else {
		logger.Info("All unassigned jobs were handled by reusing idle agents", "jobs", len(jobsWithoutAgentOrPod))
	}
}

// hasPodForJob reports whether any pod carries the job-request-id label of the job.
func (s *poolState) hasPodForJob(jobID string) bool {
	if jobID == "" {
		return false
	}
	for i := range s.allPods {
		if s.allPods[i].Labels[labelJobRequestID] == jobID {
			return true
		}
	}
	return false
}

// capabilityForJob resolves the capability a job requires: the first
// demand that matches a configured capability image (case-sensitive
// first, then case-insensitive), else base.
func (r *RunnerPoolReconciler) capabilityForJob(pool *karav1.RunnerPool, job taskagent.TaskAgentJobRequest) string {
	if !pool.Spec.CapabilityAware {
		return runners.CapabilityBase
	}
	for _, demand := range devops.JobDemandStrings(job) {
		if image, ok := pool.Spec.CapabilityImages[demand]; ok && image != "" {
			return demand
		}
	}
	for _, demand := range devops.JobDemandStrings(job) {
		for key, image := range pool.Spec.CapabilityImages {
			if image != "" && strings.EqualFold(key, demand) {
				return key
			}
		}
	}
	return runners.CapabilityBase
}

// tryReuseIdleAgent tries to attach the job to an existing idle agent
// (C# TryReuseIdleAgentAsync).
func (r *RunnerPoolReconciler) tryReuseIdleAgent(ctx context.Context, pool *karav1.RunnerPool, s *poolState, job taskagent.TaskAgentJobRequest) bool {
	logger := log.FromContext(ctx)

	// Reuse is only meaningful with a grace period.
	if pool.Spec.TtlIdleSeconds <= 0 {
		return false
	}

	idleThreshold := time.Now().Add(-time.Duration(pool.Spec.TtlIdleSeconds) * time.Second)

	minAgentNames := map[string]bool{}
	for i := range s.minPods {
		minAgentNames[s.minPods[i].Name] = true
	}

	// Eligible idle agents: online/running, operator-managed, not min
	// agents, within the idle window, not busy.
	var eligibleNames []string
	for i := range s.agents {
		agent := s.agents[i]
		status := devops.AgentStatus(agent)
		if status != "online" && status != "running" {
			continue
		}
		if !devops.IsOperatorManagedAgentName(devops.AgentName(agent), pool.Name) {
			continue
		}
		if minAgentNames[devops.AgentName(agent)] {
			continue
		}
		if agent.LastCompletedRequest != nil && agent.LastCompletedRequest.FinishTime != nil {
			if agent.LastCompletedRequest.FinishTime.Time.Before(idleThreshold) {
				continue
			}
		}
		if s.agentIsBusy(devops.AgentID(agent)) {
			continue
		}
		eligibleNames = append(eligibleNames, devops.AgentName(agent))
	}

	eligibleSet := map[string]bool{}
	for _, n := range eligibleNames {
		eligibleSet[n] = true
	}

	// Eligible idle pods: running, match an eligible agent, no active
	// job-request-id label.
	var eligiblePods []corev1.Pod
	for i := range s.allPods {
		pod := s.allPods[i]
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		if !eligibleSet[pod.Name] {
			continue
		}
		if jobID := pod.Labels[labelJobRequestID]; jobID != "" && s.jobIsActive(jobID) {
			continue
		}
		eligiblePods = append(eligiblePods, pod)
	}

	requiredCapability := r.capabilityForJob(pool, job)

	reusable := -1
	if pool.Spec.CapabilityAware {
		for i := range eligiblePods {
			if eligiblePods[i].Labels[runners.LabelCapability] == requiredCapability {
				reusable = i
				break
			}
		}
	} else if len(eligiblePods) > 0 {
		reusable = 0
	}

	if reusable < 0 {
		return false
	}

	pod := eligiblePods[reusable]
	if err := r.Runner.UpdatePodLabel(ctx, pool, pod.Name, map[string]string{labelJobRequestID: jobRequestID(job)}); err != nil {
		logger.Error(err, "Failed to set job-request-id label on reused pod", "pod", pod.Name)
		return false
	}
	logger.Info("Reused idle agent for job", "pod", pod.Name, "job", jobRequestID(job), "capability", requiredCapability)
	return true
}

// spawnCapabilityAwareAgent spawns an agent with the capability image
// matching the job demands (C# SpawnCapabilityAwareAgentsFromJobDemands).
func (r *RunnerPoolReconciler) spawnCapabilityAwareAgent(ctx context.Context, pool *karav1.RunnerPool, s *poolState, job taskagent.TaskAgentJobRequest, extraLabels map[string]string) {
	logger := log.FromContext(ctx)
	capability := r.capabilityForJob(pool, job)
	if capability == runners.CapabilityBase {
		logger.V(1).Info("No capability image matched job demands, spawning base agent", "job", jobRequestID(job))
	}
	if err := r.spawnAgent(ctx, pool, s, false, capability, extraLabels); err != nil {
		logger.Error(err, "Failed to spawn capability-aware agent", "job", jobRequestID(job), "capability", capability)
	} else {
		logger.Info("Spawned agent with capability", "capability", capability, "job", jobRequestID(job))
	}
}

// spawnAgent allocates the next index and creates an agent pod.
func (r *RunnerPoolReconciler) spawnAgent(ctx context.Context, pool *karav1.RunnerPool, s *poolState, isMinAgent bool, capability string, extraLabels map[string]string) error {
	logger := log.FromContext(ctx)

	index, err := r.Runner.GetNextAvailableAgentIndex(ctx, pool)
	if err != nil {
		return fmt.Errorf("get next agent index: %w", err)
	}
	if index < 0 {
		logger.V(1).Info("Max agents reached, cannot spawn", "maxAgents", pool.Spec.MaxAgents)
		return nil
	}

	_, err = r.Runner.CreatePod(ctx, pool, index, isMinAgent, capability, "", extraLabels)
	if err != nil {
		return fmt.Errorf("create agent pod: %w", err)
	}
	logger.Info("Spawned agent pod", "pod", r.Runner.PodName(pool, index), "isMinAgent", isMinAgent, "capability", capability)
	return nil
}

// ---------------------------------------------------------------------------
// Finalizer (C# AzDORunnerFinalizer + AzDORunnerController.DeletedAsync)
// ---------------------------------------------------------------------------

func (r *RunnerPoolReconciler) finalizePool(ctx context.Context, pool *karav1.RunnerPool) error {
	logger := log.FromContext(ctx)
	logger.Info("Finalizing RunnerPool", "name", pool.Name)

	// Best effort: unregister all operator-managed agents from Azure
	// DevOps so they do not linger as offline agents. (The C# original
	// leaked these; the pool is unregistered from its polling loop before
	// the finalizer runs, so nothing would ever clean them up.)
	pat, err := r.getPersonalAccessToken(ctx, pool)
	if err == nil {
		if azdo, err := r.newAzDoClient(pool.Spec.AzURL, pat); err == nil {
			defer azdo.Close()
			azdoPool, err := azdo.GetPool(ctx, pool.Spec.Pool)
			if err == nil {
				agents, err := azdo.ListAgents(ctx, *azdoPool.Id, false)
				if err == nil {
					for i := range *agents {
						agent := (*agents)[i]
						if devops.IsOperatorManagedAgentName(devops.AgentName(agent), pool.Name) {
							if derr := azdo.DeleteAgent(ctx, *azdoPool.Id, devops.AgentID(agent)); derr != nil {
								logger.Error(derr, "Failed to unregister agent during finalization", "agent", devops.AgentName(agent))
							} else {
								logger.Info("Unregistered agent during finalization", "agent", devops.AgentName(agent))
							}
						}
					}
				} else {
					logger.Error(err, "Failed to list agents during finalization")
				}
			} else {
				logger.Error(err, "Failed to resolve pool during finalization")
			}
		} else {
			logger.Error(err, "Failed to create Azure DevOps client during finalization")
		}
	} else {
		logger.Error(err, "Failed to read PAT secret during finalization, skipping AzDO agent unregistration")
	}

	// Delete all pods (any phase).
	pods, err := r.Runner.GetAllRunnerPods(ctx, pool)
	if err != nil {
		return fmt.Errorf("list pods during finalization: %w", err)
	}
	for i := range pods {
		if err := r.Runner.DeletePodByName(ctx, pool, pods[i].Name); err != nil {
			logger.Error(err, "Failed to delete pod during finalization", "pod", pods[i].Name)
		}
	}

	// Delete operator-created PVCs.
	if err := r.Runner.DeletePoolPVCs(ctx, pool); err != nil {
		logger.Error(err, "Failed to delete PVCs during finalization")
	}

	return nil
}

// ---------------------------------------------------------------------------
// Status updates (C# UpdateEntityStatus)
// ---------------------------------------------------------------------------

// setStatusDisconnected marks the pool disconnected with the error.
func (r *RunnerPoolReconciler) setStatusDisconnected(ctx context.Context, pool *karav1.RunnerPool, lastError string) {
	logger := log.FromContext(ctx)
	fresh, err := r.refreshPool(ctx, pool)
	if err != nil {
		logger.V(1).Info("Failed to refresh RunnerPool for status update", "error", err.Error())
		return
	}

	fresh.Status.ConnectionStatus = "Disconnected"
	fresh.Status.Active = false
	fresh.Status.QueuedJobs = 0
	fresh.Status.LastError = lastError
	fresh.Status.LastPolled = &metav1.Time{Time: time.Now()}
	fresh.Status.OrganizationName = devops.ExtractOrganizationName(pool.Spec.AzURL)
	fresh.Status.PoolName = pool.Spec.Pool
	fresh.Status.AgentsSummary = fmt.Sprintf("%d/%d", 0, pool.Spec.MaxAgents)
	setCondition(fresh, karav1.ConditionTypeError, metav1.ConditionTrue, "Disconnected",
		fmt.Sprintf("Failed to connect to Azure DevOps: %s", lastError))

	if err := r.Status().Update(ctx, fresh); err != nil {
		logger.V(1).Info("Failed to update disconnected status", "error", err.Error())
	}
}

// updateStatusConnected records a successful pass.
func (r *RunnerPoolReconciler) updateStatusConnected(ctx context.Context, pool *karav1.RunnerPool, s *poolState) {
	logger := log.FromContext(ctx)
	fresh, err := r.refreshPool(ctx, pool)
	if err != nil {
		logger.V(1).Info("Failed to refresh RunnerPool for status update", "error", err.Error())
		return
	}

	var operatorManagedAgents []taskagent.TaskAgent
	for i := range s.agents {
		if devops.IsOperatorManagedAgentName(devops.AgentName(s.agents[i]), pool.Name) {
			operatorManagedAgents = append(operatorManagedAgents, s.agents[i])
		}
	}

	running, pending, starting := 0, 0, 0
	for i := range s.allPods {
		switch s.allPods[i].Status.Phase {
		case corev1.PodRunning:
			running++
		case corev1.PodPending:
			pending++
			for j := range s.allPods[i].Status.ContainerStatuses {
				cs := s.allPods[i].Status.ContainerStatuses[j]
				if cs.State.Waiting != nil && cs.State.Waiting.Reason == "ContainerCreating" {
					starting++
					break
				}
			}
		}
	}
	activePods := running + pending

	available, offline := 0, 0
	for i := range operatorManagedAgents {
		switch devops.AgentStatus(operatorManagedAgents[i]) {
		case "online", "idle", "running":
			available++
		case "offline":
			offline++
		}
	}

	fresh.Status.ConnectionStatus = "Connected"
	fresh.Status.Active = true
	fresh.Status.QueuedJobs = s.queuedJobs
	fresh.Status.RunningAgents = len(operatorManagedAgents)
	fresh.Status.LastPolled = &metav1.Time{Time: time.Now()}
	fresh.Status.LastError = ""
	fresh.Status.OrganizationName = devops.ExtractOrganizationName(pool.Spec.AzURL)
	fresh.Status.PoolName = pool.Spec.Pool
	fresh.Status.AgentsSummary = fmt.Sprintf("%d/%d", len(operatorManagedAgents), pool.Spec.MaxAgents)
	fresh.Status.QueuedJobsByCap = s.queuedByCap
	fresh.Status.CurrentAgentIndex = r.currentAgentIndex(ctx, pool)

	agents := make([]karav1.AgentStatus, 0, len(operatorManagedAgents))
	for i := range operatorManagedAgents {
		a := operatorManagedAgents[i]
		st := karav1.AgentStatus{
			ID:     devops.AgentID(a),
			Name:   devops.AgentName(a),
			Status: devops.AgentStatus(a),
		}
		if a.CreatedOn != nil {
			t := metav1.Time{Time: a.CreatedOn.Time}
			st.CreatedAt = &t
		}
		if a.LastCompletedRequest != nil && a.LastCompletedRequest.FinishTime != nil {
			t := metav1.Time{Time: a.LastCompletedRequest.FinishTime.Time}
			st.LastActive = &t
		}
		agents = append(agents, st)
	}
	fresh.Status.Agents = agents
	fresh.Status.AgentIndexes = r.buildAgentIndexes(ctx, pool)

	msg := fmt.Sprintf("Pool has %d operator-managed agents (%d available, %d offline), %d pods (%d running, %d pending)",
		len(operatorManagedAgents), available, offline, activePods, running, pending)
	if starting > 0 {
		msg += fmt.Sprintf(", %d starting", starting)
	}
	msg += fmt.Sprintf(", %d queued jobs", s.queuedJobs)
	setCondition(fresh, karav1.ConditionTypeReady, metav1.ConditionTrue, "Reconciled", msg)

	if err := r.Status().Update(ctx, fresh); err != nil {
		logger.V(1).Info("Failed to update RunnerPool status", "error", err.Error())
	}
}

// refreshPool re-fetches the RunnerPool so the status is written against
// the latest version (C# parity).
func (r *RunnerPoolReconciler) refreshPool(ctx context.Context, pool *karav1.RunnerPool) (*karav1.RunnerPool, error) {
	fresh := &karav1.RunnerPool{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(pool), fresh); err != nil {
		return nil, err
	}
	return fresh, nil
}

// currentAgentIndex returns the next index that would be allocated.
func (r *RunnerPoolReconciler) currentAgentIndex(ctx context.Context, pool *karav1.RunnerPool) int {
	index, err := r.Runner.GetNextAvailableAgentIndex(ctx, pool)
	if err != nil || index < 0 {
		return 0
	}
	return index
}

// buildAgentIndexes maps every in-use agent index (as a string key) to
// its pod and PVCs.
func (r *RunnerPoolReconciler) buildAgentIndexes(ctx context.Context, pool *karav1.RunnerPool) map[string]karav1.AgentIndexInfo {
	indexes := map[string]karav1.AgentIndexInfo{}
	pods, err := r.Runner.GetAllRunnerPods(ctx, pool)
	if err != nil {
		return indexes
	}
	for i := range pods {
		pod := pods[i]
		index := r.Runner.GetAgentIndex(pool, pod.Name)
		if index < 0 {
			continue
		}
		info := karav1.AgentIndexInfo{
			PodName:    pod.Name,
			Status:     string(pod.Status.Phase),
			IsMinAgent: pod.Labels[runners.LabelMinAgent] == "true",
		}
		t := metav1.Time{Time: pod.CreationTimestamp.Time}
		info.CreatedAt = &t
		if pvcNames, err := r.Runner.ListAgentPVCs(ctx, pool, index); err == nil {
			info.PvcNames = pvcNames
		}
		indexes[strconv.Itoa(index)] = info
	}
	return indexes
}

// setCondition adds or updates a status condition.
func setCondition(pool *karav1.RunnerPool, condType karav1.ConditionType, status metav1.ConditionStatus, reason, message string) {
	condition := metav1.Condition{
		Type:               string(condType),
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Time{Time: time.Now()},
		ObservedGeneration: pool.Generation,
	}
	// Clear the opposite condition so only the current state is shown.
	keep := make([]metav1.Condition, 0, len(pool.Status.Conditions))
	for _, c := range pool.Status.Conditions {
		if c.Type == string(condType) {
			if c.Status == status {
				condition.LastTransitionTime = c.LastTransitionTime
			}
			continue
		}
		if c.Type != string(oppositeCondition(condType)) {
			keep = append(keep, c)
		}
	}
	pool.Status.Conditions = append(keep, condition)
}

func oppositeCondition(c karav1.ConditionType) karav1.ConditionType {
	if c == karav1.ConditionTypeReady {
		return karav1.ConditionTypeError
	}
	return karav1.ConditionTypeReady
}

// SetupWithManager sets up the controller with the Manager.
func (r *RunnerPoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&karav1.RunnerPool{}).
		Owns(&corev1.Pod{}).
		Complete(r)
}

// Ensure the controller satisfies reconcile.Reconciler at compile time.
var _ reconcile.Reconciler = (*RunnerPoolReconciler)(nil)
