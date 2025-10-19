package runners

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	karav1 "github.com/mahmoudk1000/azdo-runner-operator/api/v1"
)

// Label keys used to identify operator-created resources.
const (
	LabelRunnerPool      = "runner-pool"
	LabelManagedBy       = "managed-by"
	LabelApp             = "app"
	LabelMinAgent        = "min-agent"
	LabelCapability      = "capability"
	LabelCapabilityAware = "capability-aware"
	LabelAgentIndex      = "agent-index"
	LabelPvcName         = "pvc-name"

	// LabelManagedByValue identifies resources owned by this operator.
	LabelManagedByValue = "azdo-runner-operator"

	// CapabilityBase is the capability label/image key for agents that
	// have no specific capability requirement.
	CapabilityBase = "base"
)

// Runner manages the lifecycle of agent pods and their PVCs.
type Runner struct {
	Client client.Client
}

// NewRunner creates a new pod service.
func NewRunner(c client.Client) *Runner {
	return &Runner{Client: c}
}

// poolLabels returns the labels shared by every resource of a RunnerPool.
func (r *Runner) poolLabels(pool *karav1.RunnerPool) map[string]string {
	return map[string]string{
		LabelRunnerPool: pool.Name,
		LabelManagedBy:  LabelManagedByValue,
	}
}

// podName returns the deterministic pod (and agent) name for an index.
func (r *Runner) podName(pool *karav1.RunnerPool, index int) string {
	return fmt.Sprintf("%s-agent-%d", pool.Name, index)
}

// PodName returns the deterministic pod (and agent) name for an index.
func (r *Runner) PodName(pool *karav1.RunnerPool, index int) string {
	return r.podName(pool, index)
}

// pvcNameFor returns the PVC name backing one storage entry of one agent.
func (r *Runner) pvcNameFor(pool *karav1.RunnerPool, index int, storage karav1.StorageSpec) string {
	return fmt.Sprintf("%s-agent-%d-%s", pool.Name, index, storage.Name)
}

// podImage resolves the agent image for a capability: capability-aware
// pools map capabilities to images via spec.capabilityImages, everything
// else (and "base") uses the default image.
func (r *Runner) podImage(pool *karav1.RunnerPool, capability string) string {
	if pool.Spec.CapabilityAware && capability != "" && capability != CapabilityBase {
		if image, ok := pool.Spec.CapabilityImages[capability]; ok && image != "" {
			return image
		}
	}
	return pool.Spec.Image
}

// podSecurityContext builds the pod-level security context from the spec.
// The runner user defaults to 1001:1001 (matching the agent images).
func (r *Runner) podSecurityContext(pool *karav1.RunnerPool) *corev1.PodSecurityContext {
	runAsUser := int64(1001)
	if pool.Spec.SecurityContext.RunAsUser != 0 {
		runAsUser = pool.Spec.SecurityContext.RunAsUser
	}
	runAsGroup := int64(1001)
	if pool.Spec.SecurityContext.RunAsGroup != 0 {
		runAsGroup = pool.Spec.SecurityContext.RunAsGroup
	}
	return &corev1.PodSecurityContext{
		RunAsUser:  &runAsUser,
		RunAsGroup: &runAsGroup,
		FSGroup:    &pool.Spec.SecurityContext.FSGroup,
	}
}

// containerSecurityContext builds the agent container's security context.
func (r *Runner) containerSecurityContext(pool *karav1.RunnerPool) *corev1.SecurityContext {
	runAsUser := int64(1001)
	if pool.Spec.SecurityContext.RunAsUser != 0 {
		runAsUser = pool.Spec.SecurityContext.RunAsUser
	}
	runAsGroup := int64(1001)
	if pool.Spec.SecurityContext.RunAsGroup != 0 {
		runAsGroup = pool.Spec.SecurityContext.RunAsGroup
	}
	return &corev1.SecurityContext{
		RunAsUser:  &runAsUser,
		RunAsGroup: &runAsGroup,
		Privileged: &pool.Spec.SecurityContext.Privileged,
	}
}

// envVars builds the agent environment, including extra envs from spec.
func (r *Runner) envVars(pool *karav1.RunnerPool, podName string, capability string) []corev1.EnvVar {
	envs := []corev1.EnvVar{
		{Name: "AZP_URL", Value: pool.Spec.AzURL},
		{Name: "AZP_POOL", Value: pool.Spec.Pool},
		{
			Name: "AZP_TOKEN",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: pool.Spec.PATSecretName},
					Key:                  pool.Spec.PATSecretKey,
				},
			},
		},
		{Name: "AZP_AGENT_NAME", Value: podName},
	}

	if capability != "" {
		envs = append(envs, corev1.EnvVar{Name: "AZP_CAPABILITY", Value: capability})
	}

	// Register user defined capabilities with the agent as
	// Agent.<name> environment variables (the agent picks them up when
	// it configures itself).
	if pool.Spec.CapabilityAware {
		for name, value := range pool.Spec.Capabilities {
			envs = append(envs, corev1.EnvVar{Name: "Agent." + name, Value: value})
		}
	}

	for _, e := range pool.Spec.ExtraEnv {
		env := corev1.EnvVar{Name: e.Name}
		if e.Value != nil {
			env.Value = *e.Value
		}
		if e.ValueFrom != nil {
			source := &corev1.EnvVarSource{}
			if e.ValueFrom.SecretKeyRef != nil {
				source.SecretKeyRef = &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: e.ValueFrom.SecretKeyRef.Name},
					Key:                  e.ValueFrom.SecretKeyRef.Key,
				}
			}
			if e.ValueFrom.ConfigMapKeyRef != nil {
				key := e.ValueFrom.ConfigMapKeyRef.Key
				if key == "" {
					key = e.Name
				}
				source.ConfigMapKeyRef = &corev1.ConfigMapKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: e.ValueFrom.ConfigMapKeyRef.Name},
					Key:                  key,
				}
			}
			env.ValueFrom = source
		}
		envs = append(envs, env)
	}

	return envs
}

// volumesAndMounts builds the pod volumes (PVCs, workspace fallback, cert
// trust store) and the matching container mounts.
func (r *Runner) volumesAndMounts(ctx context.Context, pool *karav1.RunnerPool, index int) ([]corev1.Volume, []corev1.VolumeMount, error) {
	volumes := make([]corev1.Volume, 0, len(pool.Spec.Storage)+len(pool.Spec.CertTrustStore)+1)
	mounts := make([]corev1.VolumeMount, 0, len(pool.Spec.Storage)+len(pool.Spec.CertTrustStore))

	// Workspace: use the storage entry mounted at /workspace when present,
	// otherwise an ephemeral emptyDir.
	hasWorkspace := false
	for _, s := range pool.Spec.Storage {
		if s.MountPath == "/workspace" {
			hasWorkspace = true
			break
		}
	}
	if !hasWorkspace {
		volumes = append(volumes, corev1.Volume{
			Name:         "workspace",
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		})
		mounts = append(mounts, corev1.VolumeMount{Name: "workspace", MountPath: "/workspace"})
	}

	for i, s := range pool.Spec.Storage {
		volumeName := fmt.Sprintf("pvc-%d", i)
		pvcName := s.ClaimName
		if pvcName == "" {
			pvcName = r.pvcNameFor(pool, index, s)
		}
		volumes = append(volumes, corev1.Volume{
			Name: volumeName,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvcName},
			},
		})
		mounts = append(mounts, corev1.VolumeMount{Name: volumeName, MountPath: s.MountPath})
	}

	for _, cert := range pool.Spec.CertTrustStore {
		volumeName := "cert-" + cert.SecretName
		volumes = append(volumes, corev1.Volume{
			Name: volumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: cert.SecretName},
			},
		})
		mounts = append(mounts, corev1.VolumeMount{
			Name:      volumeName,
			MountPath: fmt.Sprintf("/etc/ssl/certs/%s.crt", cert.SecretName),
			SubPath:   "tls.crt",
		})
	}

	return volumes, mounts, nil
}

// initContainer builds the permission-fixing init container. It is only
// created when the pod has PVC-backed mounts (emptyDir workspaces need no
// ownership fixups) and the init container image is configured.
func (r *Runner) initContainer(pool *karav1.RunnerPool) *corev1.Container {
	if len(pool.Spec.Storage) == 0 {
		return nil
	}

	image := pool.Spec.InitContainerSpec.Image
	if image == "" {
		image = "busybox:latest"
	}

	mountPaths := make([]string, 0, len(pool.Spec.Storage))
	for _, s := range pool.Spec.Storage {
		mountPaths = append(mountPaths, s.MountPath)
	}

	runAsUser := pool.Spec.SecurityContext.RunAsUser
	if runAsUser == 0 {
		runAsUser = 1001
	}
	runAsGroup := pool.Spec.SecurityContext.RunAsGroup
	if runAsGroup == 0 {
		runAsGroup = 1001
	}

	script := "echo 'Init container: Setting up permissions for runner user'\n"
	script += fmt.Sprintf("echo 'Target UID: %d, GID: %d'\n", runAsUser, runAsGroup)
	for _, p := range mountPaths {
		script += fmt.Sprintf("echo 'Adjusting permissions for %s'\n", p)
		script += fmt.Sprintf("chown -R %d:%d %s && chmod -R u+rwX %s\n", runAsUser, runAsGroup, p, p)
	}
	script += "echo 'Init container: Permission setup completed'"

	return &corev1.Container{
		Name:    "fix-permissions",
		Image:   image,
		Command: []string{"/bin/sh", "-c"},
		Args:    []string{script},
	}
}

// CreatePod creates an agent pod for the given index.
//
// capability is the capability label of the agent ("base" when it has
// none). extraLabels are merged into the pod labels (e.g. the
// job-request-id that spawned the agent). imageOverride, when non-empty,
// wins over the capability image resolution.
func (r *Runner) CreatePod(ctx context.Context, pool *karav1.RunnerPool, index int, isMinAgent bool, capability string, imageOverride string, extraLabels map[string]string) (*corev1.Pod, error) {
	logger := logf.Log.WithName("runners").WithValues("pool", pool.Name, "agent", index)
	if capability == "" {
		capability = CapabilityBase
	}

	// Make sure the PVCs backing the pod exist.
	if err := r.EnsurePVCs(ctx, pool, index); err != nil {
		return nil, fmt.Errorf("create pod %s: ensure PVCs: %w", r.podName(pool, index), err)
	}

	podName := r.podName(pool, index)
	labels := r.poolLabels(pool)
	labels[LabelApp] = "azdo-runner"
	labels[LabelMinAgent] = strconv.FormatBool(isMinAgent)
	labels[LabelCapability] = capability
	labels[LabelCapabilityAware] = strconv.FormatBool(pool.Spec.CapabilityAware)
	labels[LabelAgentIndex] = strconv.Itoa(index)
	for k, v := range extraLabels {
		labels[k] = v
	}

	image := r.podImage(pool, capability)
	if imageOverride != "" {
		image = imageOverride
	}

	envs := r.envVars(pool, podName, capability)

	volumes, mounts, err := r.volumesAndMounts(ctx, pool, index)
	if err != nil {
		return nil, err
	}

	pullPolicy := corev1.PullIfNotPresent
	if pool.Spec.ImagePullPolicy != "" {
		pullPolicy = corev1.PullPolicy(pool.Spec.ImagePullPolicy)
	}

	// One-shot agents: ttlIdleSeconds == 0 and not a min agent.
	args := []string{}
	if !isMinAgent && pool.Spec.TtlIdleSeconds == 0 {
		args = append(args, "--once")
	}

	terminationGracePeriodSeconds := int64(30)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: pool.Namespace,
			Labels:    labels,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: karav1.GroupVersion.String(),
					Kind:       "RunnerPool",
					Name:       pool.Name,
					UID:        pool.UID,
					Controller: func() *bool { b := true; return &b }(),
				},
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy:                 corev1.RestartPolicyNever,
			TerminationGracePeriodSeconds: &terminationGracePeriodSeconds,
			InitContainers:                make([]corev1.Container, 0),
			Containers: []corev1.Container{
				{
					Name:            "runner",
					Image:           image,
					ImagePullPolicy: pullPolicy,
					Args:            args,
					Env:             envs,
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("256Mi"),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("2"),
							corev1.ResourceMemory: resource.MustParse("4Gi"),
						},
					},
					VolumeMounts: mounts,
					Lifecycle: &corev1.Lifecycle{
						PreStop: &corev1.LifecycleHandler{
							Exec: &corev1.ExecAction{
								Command: []string{
									"/bin/bash", "-c",
									"echo 'PreStop hook triggered'; pkill -TERM Agent.Listener || true; sleep 5",
								},
							},
						},
					},
				},
			},
			Volumes: volumes,
		},
	}

	if sc := r.podSecurityContext(pool); sc != nil {
		pod.Spec.SecurityContext = sc
		pod.Spec.Containers[0].SecurityContext = r.containerSecurityContext(pool)
	}

	if ic := r.initContainer(pool); ic != nil {
		// The init container runs as root so it can chown the mounts.
		runAsRoot := int64(0)
		ic.SecurityContext = &corev1.SecurityContext{RunAsUser: &runAsRoot}
		for _, m := range mounts {
			ic.VolumeMounts = append(ic.VolumeMounts, m)
		}
		pod.Spec.InitContainers = append(pod.Spec.InitContainers, *ic)
	}

	logger.Info("Creating agent pod", "pod", podName, "capability", capability, "isMinAgent", isMinAgent)
	if err := r.Client.Create(ctx, pod); err != nil {
		return nil, fmt.Errorf("create pod %s: %w", podName, err)
	}
	return pod, nil
}

// DeletePod removes an agent pod.
func (r *Runner) DeletePod(ctx context.Context, pool *karav1.RunnerPool, index int) error {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      r.podName(pool, index),
			Namespace: pool.Namespace,
		},
	}
	if err := r.Client.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete pod %s: %w", pod.Name, err)
	}
	return nil
}

// DeletePodByName removes an agent pod by name.
func (r *Runner) DeletePodByName(ctx context.Context, pool *karav1.RunnerPool, podName string) error {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: pool.Namespace,
		},
	}
	if err := r.Client.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete pod %s: %w", podName, err)
	}
	return nil
}

// GetActivePods lists pods of the pool that are Running or Pending.
func (r *Runner) GetActivePods(ctx context.Context, pool *karav1.RunnerPool) ([]corev1.Pod, error) {
	logger := logf.Log.WithName("runners")
	podList := &corev1.PodList{}
	if err := r.Client.List(ctx, podList,
		client.InNamespace(pool.Namespace),
		client.MatchingLabels{LabelRunnerPool: pool.Name},
	); err != nil {
		return nil, fmt.Errorf("list pods for %s: %w", pool.Name, err)
	}

	active := make([]corev1.Pod, 0, len(podList.Items))
	for _, p := range podList.Items {
		if p.Status.Phase == corev1.PodRunning || p.Status.Phase == corev1.PodPending {
			active = append(active, p)
		}
	}
	logger.V(1).Info("Listed active pods", "count", len(active))
	return active, nil
}

// GetAllRunnerPods lists every pod of the pool (any phase).
func (r *Runner) GetAllRunnerPods(ctx context.Context, pool *karav1.RunnerPool) ([]corev1.Pod, error) {
	podList := &corev1.PodList{}
	if err := r.Client.List(ctx, podList,
		client.InNamespace(pool.Namespace),
		client.MatchingLabels{LabelRunnerPool: pool.Name},
	); err != nil {
		return nil, fmt.Errorf("list pods for %s: %w", pool.Name, err)
	}
	return podList.Items, nil
}

// GetMinAgentPods lists min-agent pods that are Running or Pending.
func (r *Runner) GetMinAgentPods(ctx context.Context, pool *karav1.RunnerPool) ([]corev1.Pod, error) {
	podList := &corev1.PodList{}
	if err := r.Client.List(ctx, podList,
		client.InNamespace(pool.Namespace),
		client.MatchingLabels{
			LabelRunnerPool: pool.Name,
			LabelMinAgent:   "true",
		},
	); err != nil {
		return nil, fmt.Errorf("list min agent pods for %s: %w", pool.Name, err)
	}

	minPods := make([]corev1.Pod, 0, len(podList.Items))
	for _, p := range podList.Items {
		if p.Status.Phase == corev1.PodRunning || p.Status.Phase == corev1.PodPending {
			minPods = append(minPods, p)
		}
	}
	return minPods, nil
}

// GetAgentIndex extracts the numeric agent index from a pod name,
// or -1 when the pod does not follow the naming convention.
func (r *Runner) GetAgentIndex(pool *karav1.RunnerPool, podName string) int {
	prefix := r.podName(pool, 0)
	// prefix ends with "-agent-0"; strip the trailing "0".
	prefix = prefix[:len(prefix)-1]
	if !strings.HasPrefix(podName, prefix) {
		return -1
	}
	index, err := strconv.Atoi(podName[len(prefix):])
	if err != nil {
		return -1
	}
	return index
}

// GetNextAvailableAgentIndex finds the lowest free index for a new agent
// pod. It returns -1 when maxAgents active pods already exist.
func (r *Runner) GetNextAvailableAgentIndex(ctx context.Context, pool *karav1.RunnerPool) (int, error) {
	logger := logf.Log.WithName("runners")

	allPods, err := r.GetAllRunnerPods(ctx, pool)
	if err != nil {
		return -1, fmt.Errorf("get all pods for %s: %w", pool.Name, err)
	}

	activeCount := 0
	inUse := make(map[int]bool)
	for _, p := range allPods {
		if p.Status.Phase == corev1.PodRunning || p.Status.Phase == corev1.PodPending {
			activeCount++
		}
		// Any existing pod (including completed ones that have not been
		// garbage-collected yet, and pods created earlier in this very
		// reconcile pass, whose phase is still empty) occupies its index.
		if index := r.GetAgentIndex(pool, p.Name); index >= 0 {
			inUse[index] = true
		}
	}

	if activeCount >= pool.Spec.MaxAgents {
		logger.V(1).Info("Max agents reached", "maxAgents", pool.Spec.MaxAgents)
		return -1, nil
	}

	for i := 0; i < pool.Spec.MaxAgents; i++ {
		if !inUse[i] {
			return i, nil
		}
	}

	return -1, nil
}

// UpdatePodLabel merges labels into a pod (add or update).
func (r *Runner) UpdatePodLabel(ctx context.Context, pool *karav1.RunnerPool, podName string, labels map[string]string) error {
	pod := &corev1.Pod{}
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: pool.Namespace, Name: podName}, pod); err != nil {
		return fmt.Errorf("get pod %s: %w", podName, err)
	}

	if pod.Labels == nil {
		pod.Labels = make(map[string]string)
	}
	for k, v := range labels {
		pod.Labels[k] = v
	}

	if err := r.Client.Update(ctx, pod); err != nil {
		return fmt.Errorf("update labels on pod %s: %w", podName, err)
	}
	return nil
}

// RemovePodLabel deletes a label from a pod when present.
func (r *Runner) RemovePodLabel(ctx context.Context, pool *karav1.RunnerPool, podName string, label string) error {
	pod := &corev1.Pod{}
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: pool.Namespace, Name: podName}, pod); err != nil {
		return fmt.Errorf("get pod %s: %w", podName, err)
	}
	if _, exists := pod.Labels[label]; !exists {
		return nil
	}
	delete(pod.Labels, label)
	if err := r.Client.Update(ctx, pod); err != nil {
		return fmt.Errorf("remove label %s from pod %s: %w", label, podName, err)
	}
	return nil
}

// EnsurePVCs makes sure the PVCs backing one agent exist. Existing PVCs
// with the pool's labels are reused; mismatched labels are an error
// (unless the storage entry is optional).
func (r *Runner) EnsurePVCs(ctx context.Context, pool *karav1.RunnerPool, index int) error {
	logger := logf.Log.WithName("runners").WithValues("pool", pool.Name, "agent", index)

	for _, s := range pool.Spec.Storage {
		if s.ClaimName != "" || !s.CreatePvc {
			continue
		}

		pvcName := r.pvcNameFor(pool, index, s)

		existing := &corev1.PersistentVolumeClaim{}
		err := r.Client.Get(ctx, types.NamespacedName{Namespace: pool.Namespace, Name: pvcName}, existing)
		if err == nil {
			// Reuse only when the PVC is labeled for this pool.
			if existing.Labels[LabelRunnerPool] == pool.Name && existing.Labels[LabelPvcName] == s.Name {
				logger.V(1).Info("Reusing existing PVC", "pvc", pvcName)
				continue
			}
			return fmt.Errorf("PVC %s exists but is not managed by this pool", pvcName)
		}
		if !apierrors.IsNotFound(err) {
			if s.Optional {
				logger.Info("Skipping PVC creation (optional, not found)", "pvc", pvcName, "reason", err.Error())
				continue
			}
			return fmt.Errorf("get PVC %s: %w", pvcName, err)
		}

		quantity, err := resource.ParseQuantity(s.Size)
		if err != nil {
			return fmt.Errorf("invalid storage size %q for %s: %w", s.Size, s.Name, err)
		}

		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      pvcName,
				Namespace: pool.Namespace,
				Labels: map[string]string{
					LabelRunnerPool: pool.Name,
					LabelPvcName:    s.Name,
					LabelAgentIndex: strconv.Itoa(index),
				},
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: karav1.GroupVersion.String(),
						Kind:       "RunnerPool",
						Name:       pool.Name,
						UID:        pool.UID,
					},
				},
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: quantity,
					},
				},
			},
		}
		if s.StorageClass != "" {
			pvc.Spec.StorageClassName = &s.StorageClass
		}

		logger.Info("Creating PVC", "pvc", pvcName, "size", s.Size)
		if err := r.Client.Create(ctx, pvc); err != nil {
			if s.Optional && apierrors.IsAlreadyExists(err) {
				logger.Info("PVC already exists (optional)", "pvc", pvcName)
				continue
			}
			return fmt.Errorf("create PVC %s: %w", pvcName, err)
		}
	}

	return nil
}

// DeleteAgentPVC removes the PVCs of one agent that are flagged
// deleteWithAgent.
func (r *Runner) DeleteAgentPVC(ctx context.Context, pool *karav1.RunnerPool, index int) error {
	logger := logf.Log.WithName("runners").WithValues("pool", pool.Name, "agent", index)
	for _, s := range pool.Spec.Storage {
		if !s.DeleteWithAgent || s.ClaimName != "" || !s.CreatePvc {
			continue
		}
		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      r.pvcNameFor(pool, index, s),
				Namespace: pool.Namespace,
			},
		}
		if err := r.Client.Delete(ctx, pvc); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete PVC %s: %w", pvc.Name, err)
		} else if err == nil {
			logger.Info("Deleted PVC for agent", "pvc", pvc.Name)
		}
	}
	return nil
}

// DeleteAgentWithResources removes an agent pod together with its
// deleteWithAgent PVCs.
func (r *Runner) DeleteAgentWithResources(ctx context.Context, pool *karav1.RunnerPool, index int) error {
	if err := r.DeletePod(ctx, pool, index); err != nil {
		return err
	}
	return r.DeleteAgentPVC(ctx, pool, index)
}

// ListAgentPVCs lists the PVCs of one agent index (any storage entry).
func (r *Runner) ListAgentPVCs(ctx context.Context, pool *karav1.RunnerPool, index int) ([]string, error) {
	pvcList := &corev1.PersistentVolumeClaimList{}
	if err := r.Client.List(ctx, pvcList,
		client.InNamespace(pool.Namespace),
		client.MatchingLabels{
			LabelRunnerPool: pool.Name,
			LabelAgentIndex: strconv.Itoa(index),
		},
	); err != nil {
		return nil, fmt.Errorf("list PVCs for agent %d: %w", index, err)
	}
	names := make([]string, 0, len(pvcList.Items))
	for _, pvc := range pvcList.Items {
		names = append(names, pvc.Name)
	}
	return names, nil
}

// DeletePoolPVCs deletes every PVC the operator created for the pool
// (used by the finalizer).
func (r *Runner) DeletePoolPVCs(ctx context.Context, pool *karav1.RunnerPool) error {
	pvcList := &corev1.PersistentVolumeClaimList{}
	if err := r.Client.List(ctx, pvcList,
		client.InNamespace(pool.Namespace),
		client.MatchingLabels{LabelRunnerPool: pool.Name},
	); err != nil {
		return fmt.Errorf("list PVCs for pool %s: %w", pool.Name, err)
	}
	var firstErr error
	for i := range pvcList.Items {
		pvc := pvcList.Items[i]
		// Only delete PVCs that the operator would recreate
		// (i.e. createPvc entries), never user-provided claims.
		owned := false
		for _, s := range pool.Spec.Storage {
			if s.CreatePvc && s.ClaimName == "" && strings.HasPrefix(pvc.Name, pool.Name+"-agent-") && strings.HasSuffix(pvc.Name, "-"+s.Name) {
				owned = true
				break
			}
		}
		if !owned {
			continue
		}
		if err := r.Client.Delete(ctx, &pvc); err != nil && !apierrors.IsNotFound(err) && firstErr == nil {
			firstErr = fmt.Errorf("delete PVC %s: %w", pvc.Name, err)
		}
	}
	return firstErr
}

// GetDefaultStorageClass returns the cluster default storage class
// (StorageClassProvisioner annotation = kubernetes.io/default-class, or
// the one with the annotation set to "true"), or an error when none is
// configured.
func (r *Runner) GetDefaultStorageClass(ctx context.Context) (*storagev1.StorageClass, error) {
	list := &storagev1.StorageClassList{}
	if err := r.Client.List(ctx, list); err != nil {
		return nil, fmt.Errorf("list storage classes: %w", err)
	}
	for i := range list.Items {
		sc := &list.Items[i]
		if sc.Annotations != nil && sc.Annotations["storageclass.kubernetes.io/is-default-class"] == "true" {
			return sc, nil
		}
	}
	return nil, fmt.Errorf("no default storage class found")
}
