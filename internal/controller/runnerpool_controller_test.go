package controller

import (
	"context"
	"fmt"
	"testing"
	"time"

	azuredevops "github.com/microsoft/azure-devops-go-api/azuredevops/v7"
	"github.com/microsoft/azure-devops-go-api/azuredevops/v7/taskagent"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	karav1 "github.com/mahmoudk1000/azdo-runner-operator/api/v1"
	"github.com/mahmoudk1000/azdo-runner-operator/internal/devops"
	"github.com/mahmoudk1000/azdo-runner-operator/internal/runners"
)

// ---------------------------------------------------------------------------
// fake AzDO API
// ---------------------------------------------------------------------------

type fakeAzDo struct {
	testConnErr   error
	pool          *taskagent.TaskAgentPool
	poolErr       error
	agents        []taskagent.TaskAgent
	jobs          []taskagent.TaskAgentJobRequest
	listErr       error
	deletedAgents []int
}

func (f *fakeAzDo) Close()                               {}
func (f *fakeAzDo) TestConnection(context.Context) error { return f.testConnErr }
func (f *fakeAzDo) GetPool(context.Context, string) (*taskagent.TaskAgentPool, error) {
	if f.poolErr != nil {
		return nil, f.poolErr
	}
	return f.pool, nil
}
func (f *fakeAzDo) ListPools(context.Context) (*[]taskagent.TaskAgentPool, error) {
	return &[]taskagent.TaskAgentPool{*f.pool}, nil
}
func (f *fakeAzDo) ListAgents(context.Context, int, bool) (*[]taskagent.TaskAgent, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	deleted := map[int]bool{}
	for _, id := range f.deletedAgents {
		deleted[id] = true
	}
	out := make([]taskagent.TaskAgent, 0, len(f.agents))
	for _, a := range f.agents {
		if a.Id != nil && deleted[*a.Id] {
			continue
		}
		out = append(out, a)
	}
	return &out, nil
}
func (f *fakeAzDo) DeleteAgent(_ context.Context, _ int, agentID int) error {
	f.deletedAgents = append(f.deletedAgents, agentID)
	return nil
}
func (f *fakeAzDo) GetAgentRequestsForPool(context.Context, *taskagent.TaskAgentPool) (*[]taskagent.TaskAgentJobRequest, error) {
	return &f.jobs, nil
}
func (f *fakeAzDo) GetQueuedJobsByCapability(context.Context, *taskagent.TaskAgentPool) (map[string]int, error) {
	return nil, nil
}

var _ devops.API = (*fakeAzDo)(nil)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := karav1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

type testEnv struct {
	t        *testing.T
	ctx      context.Context
	cl       client.Client
	recon    *RunnerPoolReconciler
	azdo     *fakeAzDo
	poolName string
}

func newTestEnv(t *testing.T, mutatePool func(*karav1.RunnerPool), mutateFake func(*fakeAzDo)) *testEnv {
	t.Helper()
	ctx := context.Background()

	azdoFake := &fakeAzDo{
		pool: &taskagent.TaskAgentPool{Id: intPtr(42), Name: strPtr("ci-pool")},
	}
	if mutateFake != nil {
		mutateFake(azdoFake)
	}

	pool := basePool("testpool")
	if mutatePool != nil {
		mutatePool(pool)
	}

	cl := fake.NewClientBuilder().
		WithScheme(newScheme(t)).
		WithObjects(pool).
		WithStatusSubresource(&karav1.RunnerPool{}).
		Build()

	env := &testEnv{
		t:   t,
		ctx: ctx,
		cl:  cl,
		recon: &RunnerPoolReconciler{
			Client: cl,
			Scheme: newScheme(t),
			Runner: runners.NewRunner(cl),
			AzDoClientFactory: func(_, _ string) (devops.API, error) {
				return azdoFake, nil
			},
		},
		azdo:     azdoFake,
		poolName: "testpool",
	}
	return env
}

func basePool(name string) *karav1.RunnerPool {
	return &karav1.RunnerPool{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: types.UID(name + "-uid")},
		Spec: karav1.RunnerPoolSpec{
			AzURL:               "https://dev.azure.com/my-org",
			Pool:                "ci-pool",
			PATSecretName:       "kara-pat",
			PATSecretKey:        "token",
			Image:               "ghcr.io/example/agent:latest",
			ImagePullPolicy:     "IfNotPresent",
			MaxAgents:           5,
			MinAgents:           0,
			TtlIdleSeconds:      300,
			PollIntervalSeconds: 5,
		},
	}
}

func (e *testEnv) patSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "kara-pat", Namespace: "default"},
		Data:       map[string][]byte{"token": []byte("pat")},
	}
}

func (e *testEnv) reconcile() {
	e.t.Helper()
	_, err := e.recon.Reconcile(e.ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: e.poolName}})
	if err != nil {
		e.t.Fatalf("reconcile: %v", err)
	}
}

func strPtr(s string) *string    { return &s }
func intPtr(i int) *int          { return &i }
func uint64Ptr(u uint64) *uint64 { return &u }

func agent(name string, status string, id int, finishedAgo time.Duration) taskagent.TaskAgent {
	st := taskagent.TaskAgentStatus(status)
	a := taskagent.TaskAgent{
		Id:     intPtr(id),
		Name:   strPtr(name),
		Status: &st,
	}
	if finishedAgo > 0 {
		a.LastCompletedRequest = &taskagent.TaskAgentJobRequest{FinishTime: &azuredevops.Time{Time: time.Now().Add(-finishedAgo)}}
	}
	return a
}

func timePtr(t time.Time) *time.Time { return &t }
func ptrStatus(s string) *taskagent.TaskAgentStatus {
	v := taskagent.TaskAgentStatus(s)
	return &v
}

func job(id uint64, result *string, reservedAgentID *int, demands ...string) taskagent.TaskAgentJobRequest {
	j := taskagent.TaskAgentJobRequest{RequestId: uint64Ptr(id)}
	if result != nil {
		r := taskagent.TaskResult(*result)
		j.Result = &r
	}
	if reservedAgentID != nil {
		j.ReservedAgent = &taskagent.TaskAgentReference{Id: reservedAgentID}
	}
	if len(demands) > 0 {
		var d []interface{}
		for _, x := range demands {
			d = append(d, x)
		}
		j.Demands = &d
	}
	return j
}

func (e *testEnv) getPool() *karav1.RunnerPool {
	e.t.Helper()
	pool := &karav1.RunnerPool{}
	if err := e.cl.Get(e.ctx, types.NamespacedName{Namespace: "default", Name: "testpool"}, pool); err != nil {
		e.t.Fatalf("get pool: %v", err)
	}
	return pool
}

func (e *testEnv) pods() []corev1.Pod {
	e.t.Helper()
	list := &corev1.PodList{}
	if err := e.cl.List(e.ctx, list, client.InNamespace("default")); err != nil {
		e.t.Fatalf("list pods: %v", err)
	}
	return list.Items
}

func (e *testEnv) podNamed(name string) (*corev1.Pod, error) {
	e.t.Helper()
	pod := &corev1.Pod{}
	err := e.cl.Get(e.ctx, types.NamespacedName{Namespace: "default", Name: name}, pod)
	if err != nil {
		return nil, err
	}
	return pod, nil
}

func makePod(name string, phase corev1.PodPhase, age time.Duration, labels map[string]string) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "default",
			CreationTimestamp: metav1.Time{Time: time.Now().Add(-age)},
			Labels:            labels,
		},
		Status: corev1.PodStatus{Phase: phase},
	}
	return pod
}

// ---------------------------------------------------------------------------
// tests
// ---------------------------------------------------------------------------

func TestReconcileAddsFinalizer(t *testing.T) {
	env := newTestEnv(t, nil, nil)
	if err := env.cl.Create(env.ctx, env.patSecret()); err != nil {
		t.Fatal(err)
	}
	env.reconcile()

	pool := env.getPool()
	if !hasFinalizer(pool) {
		t.Fatal("finalizer not added")
	}
	if pool.Status.ConnectionStatus != "Connected" {
		t.Fatalf("expected Connected, got %q (lastError=%q)", pool.Status.ConnectionStatus, pool.Status.LastError)
	}
}

func TestReconcileMissingSecret(t *testing.T) {
	env := newTestEnv(t, nil, nil) // no secret created
	env.reconcile()

	pool := env.getPool()
	if pool.Status.ConnectionStatus != "Disconnected" {
		t.Fatalf("expected Disconnected, got %q", pool.Status.ConnectionStatus)
	}
	if pool.Status.LastError == "" {
		t.Fatal("expected lastError to be set")
	}
}

func TestReconcileConnectionFailure(t *testing.T) {
	env := newTestEnv(t, nil, func(f *fakeAzDo) {
		f.testConnErr = fmt.Errorf("401 unauthorized")
	})
	if err := env.cl.Create(env.ctx, env.patSecret()); err != nil {
		t.Fatal(err)
	}
	env.reconcile()

	pool := env.getPool()
	if pool.Status.ConnectionStatus != "Disconnected" {
		t.Fatalf("expected Disconnected, got %q", pool.Status.ConnectionStatus)
	}
	if pool.Status.LastError == "" {
		t.Fatal("expected lastError to be set")
	}
}

func TestReconcileCreatesMinimumAgents(t *testing.T) {
	env := newTestEnv(t, func(p *karav1.RunnerPool) { p.Spec.MinAgents = 2 }, nil)
	if err := env.cl.Create(env.ctx, env.patSecret()); err != nil {
		t.Fatal(err)
	}
	env.reconcile()

	pods := env.pods()
	if len(pods) != 2 {
		t.Fatalf("expected 2 min agent pods, got %d", len(pods))
	}
	for _, p := range pods {
		if p.Labels[runners.LabelMinAgent] != "true" {
			t.Errorf("pod %s missing min-agent=true label", p.Name)
		}
		if p.Labels[runners.LabelRunnerPool] != "testpool" {
			t.Errorf("pod %s missing runner-pool label", p.Name)
		}
	}
}

func TestReconcileScaleUpForQueuedJob(t *testing.T) {
	env := newTestEnv(t, nil, func(f *fakeAzDo) {
		f.jobs = []taskagent.TaskAgentJobRequest{job(101, nil, nil)}
	})
	if err := env.cl.Create(env.ctx, env.patSecret()); err != nil {
		t.Fatal(err)
	}
	env.reconcile()

	pods := env.pods()
	if len(pods) != 1 {
		t.Fatalf("expected 1 spawned pod, got %d", len(pods))
	}
	if got := pods[0].Labels["job-request-id"]; got != "101" {
		t.Fatalf("expected job-request-id=101, got %q", got)
	}
	pool := env.getPool()
	if pool.Status.QueuedJobs != 1 {
		t.Fatalf("expected status.queuedJobs=1, got %d", pool.Status.QueuedJobs)
	}
	if pool.Status.AgentsSummary != "0/5" {
		t.Fatalf("expected agentsSummary 0/5, got %q", pool.Status.AgentsSummary)
	}
	if pool.Status.OrganizationName != "my-org" {
		t.Fatalf("expected organizationName my-org, got %q", pool.Status.OrganizationName)
	}
}

func TestReconcileCapabilityAwareSpawn(t *testing.T) {
	env := newTestEnv(t, func(p *karav1.RunnerPool) {
		p.Spec.CapabilityAware = true
		p.Spec.CapabilityImages = map[string]string{"docker": "ghcr.io/example/agent:docker"}
	}, func(f *fakeAzDo) {
		f.jobs = []taskagent.TaskAgentJobRequest{job(202, nil, nil, "docker")}
	})
	if err := env.cl.Create(env.ctx, env.patSecret()); err != nil {
		t.Fatal(err)
	}
	env.reconcile()

	pods := env.pods()
	if len(pods) != 1 {
		t.Fatalf("expected 1 pod, got %d", len(pods))
	}
	if img := pods[0].Spec.Containers[0].Image; img != "ghcr.io/example/agent:docker" {
		t.Fatalf("expected capability image, got %q", img)
	}
	if cap := pods[0].Labels[runners.LabelCapability]; cap != "docker" {
		t.Fatalf("expected capability label docker, got %q", cap)
	}
}

func TestReconcileDoesNotSpawnForReservedJob(t *testing.T) {
	// A job already reserved to any agent is in flight; no spawn.
	env := newTestEnv(t, nil, func(f *fakeAzDo) {
		f.jobs = []taskagent.TaskAgentJobRequest{job(303, nil, intPtr(99))}
	})
	if err := env.cl.Create(env.ctx, env.patSecret()); err != nil {
		t.Fatal(err)
	}
	env.reconcile()

	if pods := env.pods(); len(pods) != 0 {
		t.Fatalf("expected no pods, got %d", len(pods))
	}
}

func TestReconcileCompletesStatus(t *testing.T) {
	env := newTestEnv(t, nil, func(f *fakeAzDo) {
		f.agents = []taskagent.TaskAgent{
			agent("testpool-agent-0", "online", 7, 0),
			agent("external-agent", "offline", 8, 0),
		}
	})
	if err := env.cl.Create(env.ctx, env.patSecret()); err != nil {
		t.Fatal(err)
	}
	env.reconcile()

	pool := env.getPool()
	// Only operator-managed agents count.
	if pool.Status.RunningAgents != 1 {
		t.Fatalf("expected 1 operator-managed agent, got %d", pool.Status.RunningAgents)
	}
	if len(pool.Status.Agents) != 1 || pool.Status.Agents[0].Name != "testpool-agent-0" {
		t.Fatalf("unexpected agents status: %+v", pool.Status.Agents)
	}
}

func TestReconcileCleansUpCompletedPod(t *testing.T) {
	env := newTestEnv(t, nil, func(f *fakeAzDo) {
		f.agents = []taskagent.TaskAgent{agent("testpool-agent-0", "offline", 1, time.Hour)}
	})
	if err := env.cl.Create(env.ctx, env.patSecret()); err != nil {
		t.Fatal(err)
	}
	completed := makePod("testpool-agent-0", corev1.PodSucceeded, time.Hour,
		map[string]string{runners.LabelRunnerPool: "testpool"})
	if err := env.cl.Create(env.ctx, completed); err != nil {
		t.Fatal(err)
	}
	env.reconcile()

	if pods := env.pods(); len(pods) != 0 {
		t.Fatalf("expected completed pod to be deleted, got %d pods", len(pods))
	}
	if len(env.azdo.deletedAgents) != 1 || env.azdo.deletedAgents[0] != 1 {
		t.Fatalf("expected agent 1 to be unregistered, got %v", env.azdo.deletedAgents)
	}
}

func TestReconcileKeepsCompletedPodWithinTTL(t *testing.T) {
	env := newTestEnv(t, func(p *karav1.RunnerPool) { p.Spec.TtlIdleSeconds = 3600 }, func(f *fakeAzDo) {
		f.agents = []taskagent.TaskAgent{agent("testpool-agent-0", "offline", 1, time.Hour)}
	})
	if err := env.cl.Create(env.ctx, env.patSecret()); err != nil {
		t.Fatal(err)
	}
	completed := makePod("testpool-agent-0", corev1.PodSucceeded, time.Minute,
		map[string]string{runners.LabelRunnerPool: "testpool"})
	if err := env.cl.Create(env.ctx, completed); err != nil {
		t.Fatal(err)
	}
	env.reconcile()

	if pods := env.pods(); len(pods) != 1 {
		t.Fatalf("expected pod to be kept within TTL, got %d pods", len(pods))
	}
}

func TestReconcileCleansUpIdleAgent(t *testing.T) {
	env := newTestEnv(t, nil, func(f *fakeAzDo) {
		f.agents = []taskagent.TaskAgent{agent("testpool-agent-0", "online", 1, time.Hour)}
	})
	if err := env.cl.Create(env.ctx, env.patSecret()); err != nil {
		t.Fatal(err)
	}
	idle := makePod("testpool-agent-0", corev1.PodRunning, time.Hour,
		map[string]string{runners.LabelRunnerPool: "testpool"})
	if err := env.cl.Create(env.ctx, idle); err != nil {
		t.Fatal(err)
	}
	env.reconcile()

	if pods := env.pods(); len(pods) != 0 {
		t.Fatalf("expected idle pod to be deleted, got %d pods", len(pods))
	}
	if len(env.azdo.deletedAgents) != 1 {
		t.Fatalf("expected agent to be unregistered, got %v", env.azdo.deletedAgents)
	}
}

func TestReconcileKeepsIdleAgentWhileJobIsPending(t *testing.T) {
	env := newTestEnv(t, nil, func(f *fakeAzDo) {
		f.agents = []taskagent.TaskAgent{agent("testpool-agent-0", "online", 1, time.Hour)}
		// The pod's labeled job is still active (no result) → protected.
		f.jobs = []taskagent.TaskAgentJobRequest{job(404, nil, nil)}
	})
	if err := env.cl.Create(env.ctx, env.patSecret()); err != nil {
		t.Fatal(err)
	}
	idle := makePod("testpool-agent-0", corev1.PodRunning, time.Hour,
		map[string]string{runners.LabelRunnerPool: "testpool", "job-request-id": "404"})
	if err := env.cl.Create(env.ctx, idle); err != nil {
		t.Fatal(err)
	}
	env.reconcile()

	if pods := env.pods(); len(pods) != 1 {
		t.Fatalf("expected pod to be protected, got %d pods", len(pods))
	}
}

func TestReconcileEnforcesMaxAgents(t *testing.T) {
	// Large TTL so idle cleanup does not fire before max enforcement.
	env := newTestEnv(t, func(p *karav1.RunnerPool) {
		p.Spec.MaxAgents = 1
		p.Spec.TtlIdleSeconds = 7200
	}, nil)
	if err := env.cl.Create(env.ctx, env.patSecret()); err != nil {
		t.Fatal(err)
	}
	older := makePod("testpool-agent-0", corev1.PodRunning, 30*time.Minute,
		map[string]string{runners.LabelRunnerPool: "testpool"})
	newer := makePod("testpool-agent-1", corev1.PodRunning, 10*time.Minute,
		map[string]string{runners.LabelRunnerPool: "testpool"})
	if err := env.cl.Create(env.ctx, older); err != nil {
		t.Fatal(err)
	}
	if err := env.cl.Create(env.ctx, newer); err != nil {
		t.Fatal(err)
	}
	env.reconcile()

	pods := env.pods()
	if len(pods) != 1 {
		t.Fatalf("expected 1 pod after max enforcement, got %d", len(pods))
	}
	if pods[0].Name != "testpool-agent-1" {
		t.Fatalf("expected the oldest pod (agent-0) to be removed, remaining: %s", pods[0].Name)
	}
}

func TestFinalizerCleansUpPodsPVCsAndAgents(t *testing.T) {
	env := newTestEnv(t, func(p *karav1.RunnerPool) {
		p.Spec.MinAgents = 1
		p.Spec.Storage = []karav1.StorageSpec{
			{Name: "workspace", MountPath: "/work", CreatePvc: true, Size: "1Gi"},
		}
	}, func(f *fakeAzDo) {
		f.agents = []taskagent.TaskAgent{agent("testpool-agent-0", "offline", 1, 0)}
	})
	if err := env.cl.Create(env.ctx, env.patSecret()); err != nil {
		t.Fatal(err)
	}

	// First reconcile adds the finalizer and creates a min agent.
	if _, err := env.recon.Reconcile(env.ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "testpool"}}); err != nil {
		t.Fatal(err)
	}

	// Ensure a pod + PVC exist.
	pods := env.pods()
	if len(pods) != 1 {
		t.Fatalf("expected 1 pod after first reconcile, got %d", len(pods))
	}
	// Ensure the PVC exists (created by the first reconcile via EnsurePVCs).
	pvcs := &corev1.PersistentVolumeClaimList{}
	if err := env.cl.List(env.ctx, pvcs, client.InNamespace("default")); err != nil {
		t.Fatal(err)
	}
	if len(pvcs.Items) != 1 || pvcs.Items[0].Name != "testpool-agent-0-workspace" {
		t.Fatalf("expected 1 workspace PVC after first reconcile, got %d", len(pvcs.Items))
	}

	// Delete the RunnerPool (finalizer holds it).
	pool := env.getPool()
	if err := env.cl.Delete(env.ctx, pool); err != nil {
		t.Fatal(err)
	}
	pool = env.getPool()
	if pool.DeletionTimestamp.IsZero() {
		t.Fatal("expected deletion timestamp to be set (finalizer)")
	}

	// Reconcile runs the finalizer.
	if _, err := env.recon.Reconcile(env.ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "testpool"}}); err != nil {
		t.Fatal(err)
	}

	if err := env.cl.Get(env.ctx, types.NamespacedName{Namespace: "default", Name: "testpool"}, &karav1.RunnerPool{}); err == nil {
		t.Fatal("expected RunnerPool to be gone after finalization")
	}
	if pods := env.pods(); len(pods) != 0 {
		t.Fatalf("expected pods deleted, got %d", len(pods))
	}
	pvcs = &corev1.PersistentVolumeClaimList{}
	if err := env.cl.List(env.ctx, pvcs, client.InNamespace("default")); err != nil {
		t.Fatal(err)
	}
	if len(pvcs.Items) != 0 {
		t.Fatalf("expected PVCs deleted, got %d", len(pvcs.Items))
	}
	if len(env.azdo.deletedAgents) != 1 || env.azdo.deletedAgents[0] != 1 {
		t.Fatalf("expected agent unregistered during finalization, got %v", env.azdo.deletedAgents)
	}
}

func hasFinalizer(pool *karav1.RunnerPool) bool {
	for _, f := range pool.Finalizers {
		if f == finalizerName {
			return true
		}
	}
	return false
}
