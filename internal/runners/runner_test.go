package runners

import (
	"context"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	karav1 "github.com/mahmoudk1000/azdo-runner-operator/api/v1"
)

func testPool() *karav1.RunnerPool {
	return &karav1.RunnerPool{
		ObjectMeta: metav1.ObjectMeta{Name: "testpool", Namespace: "default", UID: "uid-1"},
		Spec: karav1.RunnerPoolSpec{
			AzURL:          "https://dev.azure.com/org",
			Pool:           "ci-pool",
			PATSecretName:  "kara-pat",
			PATSecretKey:   "token",
			Image:          "ghcr.io/example/agent:latest",
			MaxAgents:      5,
			TtlIdleSeconds: 300,
		},
	}
}

func newTestRunner(t *testing.T, pool *karav1.RunnerPool) *Runner {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := karav1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return NewRunner(fake.NewClientBuilder().WithScheme(s).Build())
}

func envMap(envs []corev1.EnvVar) map[string]corev1.EnvVar {
	m := make(map[string]corev1.EnvVar, len(envs))
	for _, e := range envs {
		m[e.Name] = e
	}
	return m
}

func TestNaming(t *testing.T) {
	pool := testPool()
	r := NewRunner(nil)

	if got := r.PodName(pool, 3); got != "testpool-agent-3" {
		t.Fatalf("PodName = %q", got)
	}
	if got := r.pvcNameFor(pool, 3, karav1.StorageSpec{Name: "workspace"}); got != "testpool-agent-3-workspace" {
		t.Fatalf("pvcNameFor = %q", got)
	}

	if got := r.GetAgentIndex(pool, "testpool-agent-7"); got != 7 {
		t.Fatalf("GetAgentIndex = %d", got)
	}
	if got := r.GetAgentIndex(pool, "other-agent-7"); got != -1 {
		t.Fatalf("GetAgentIndex(other) = %d", got)
	}
	if got := r.GetAgentIndex(pool, "testpool-agent-abc"); got != -1 {
		t.Fatalf("GetAgentIndex(non-numeric) = %d", got)
	}
}

func TestPodImage(t *testing.T) {
	pool := testPool()
	pool.Spec.CapabilityAware = true
	pool.Spec.CapabilityImages = map[string]string{"docker": "ghcr.io/example/agent:docker"}
	r := NewRunner(nil)

	if got := r.podImage(pool, "docker"); got != "ghcr.io/example/agent:docker" {
		t.Fatalf("capability image = %q", got)
	}
	if got := r.podImage(pool, "base"); got != "ghcr.io/example/agent:latest" {
		t.Fatalf("base image = %q", got)
	}
	if got := r.podImage(pool, "unknown"); got != "ghcr.io/example/agent:latest" {
		t.Fatalf("unknown capability should fall back to default image, got %q", got)
	}

	// Capability mapping only applies to capability-aware pools.
	plain := testPool()
	if got := r.podImage(plain, "docker"); got != "ghcr.io/example/agent:latest" {
		t.Fatalf("non-capability-aware image = %q", got)
	}
}

func TestEnvVars(t *testing.T) {
	pool := testPool()
	pool.Spec.CapabilityAware = true
	pool.Spec.Capabilities = map[string]string{"RunDays": "Mon-Fri"}
	pool.Spec.ExtraEnv = []karav1.ExtraEnv{
		{Name: "PLAIN", Value: strPtrV("x")},
		{Name: "FROM_SECRET"},
	}
	pool.Spec.ExtraEnv[1].ValueFrom = &karav1.ValueFromSource{
		SecretKeyRef: &karav1.SecretKeyRef{Name: "extra-secret", Key: "k"},
	}
	r := NewRunner(nil)

	m := envMap(r.envVars(pool, "testpool-agent-0", "docker"))

	if m["AZP_URL"].Value != "https://dev.azure.com/org" {
		t.Fatalf("AZP_URL = %q", m["AZP_URL"].Value)
	}
	if m["AZP_POOL"].Value != "ci-pool" {
		t.Fatalf("AZP_POOL = %q", m["AZP_POOL"].Value)
	}
	if m["AZP_AGENT_NAME"].Value != "testpool-agent-0" {
		t.Fatalf("AZP_AGENT_NAME = %q", m["AZP_AGENT_NAME"].Value)
	}
	if m["AZP_TOKEN"].ValueFrom == nil || m["AZP_TOKEN"].ValueFrom.SecretKeyRef == nil ||
		m["AZP_TOKEN"].ValueFrom.SecretKeyRef.Name != "kara-pat" {
		t.Fatalf("AZP_TOKEN must come from the PAT secret: %+v", m["AZP_TOKEN"].ValueFrom)
	}
	if m["AZP_CAPABILITY"].Value != "docker" {
		t.Fatalf("AZP_CAPABILITY = %q", m["AZP_CAPABILITY"].Value)
	}
	if m["Agent.RunDays"].Value != "Mon-Fri" {
		t.Fatalf("Agent.RunDays = %q", m["Agent.RunDays"].Value)
	}
	if m["PLAIN"].Value != "x" {
		t.Fatalf("PLAIN = %q", m["PLAIN"].Value)
	}
	if m["FROM_SECRET"].ValueFrom == nil || m["FROM_SECRET"].ValueFrom.SecretKeyRef == nil ||
		m["FROM_SECRET"].ValueFrom.SecretKeyRef.Name != "extra-secret" {
		t.Fatalf("FROM_SECRET valueFrom = %+v", m["FROM_SECRET"].ValueFrom)
	}
}

func TestCreatePodOneShotAndLabels(t *testing.T) {
	ctx := context.Background()

	// TTL=0 non-min agent gets --once.
	pool := testPool()
	pool.Spec.TtlIdleSeconds = 0
	r := newTestRunner(t, pool)

	pod, err := r.CreatePod(ctx, pool, 0, false, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	args := pod.Spec.Containers[0].Args
	if len(args) != 1 || args[0] != "--once" {
		t.Fatalf("expected --once args, got %v", args)
	}
	if pod.Labels[LabelMinAgent] != "false" {
		t.Fatalf("min-agent label = %q", pod.Labels[LabelMinAgent])
	}
	if pod.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Fatalf("restart policy = %q", pod.Spec.RestartPolicy)
	}
	if len(pod.OwnerReferences) != 1 || pod.OwnerReferences[0].Name != "testpool" {
		t.Fatalf("owner references = %+v", pod.OwnerReferences)
	}

	// Min agent never gets --once, even with TTL=0.
	pool2 := testPool()
	pool2.Spec.TtlIdleSeconds = 0
	r2 := newTestRunner(t, pool2)
	minPod, err := r2.CreatePod(ctx, pool2, 1, true, "", "", map[string]string{"job-request-id": "42"})
	if err != nil {
		t.Fatal(err)
	}
	if len(minPod.Spec.Containers[0].Args) != 0 {
		t.Fatalf("min agent must not get --once, got %v", minPod.Spec.Containers[0].Args)
	}
	if minPod.Labels[LabelMinAgent] != "true" {
		t.Fatalf("min-agent label = %q", minPod.Labels[LabelMinAgent])
	}
	if minPod.Labels["job-request-id"] != "42" {
		t.Fatalf("extra label not applied: %v", minPod.Labels)
	}
	if minPod.Spec.Containers[0].Lifecycle == nil || minPod.Spec.Containers[0].Lifecycle.PreStop == nil {
		t.Fatal("expected preStop hook on the runner container")
	}
}

func TestCreatePodDefaultSecurityContext(t *testing.T) {
	ctx := context.Background()
	pool := testPool()
	r := newTestRunner(t, pool)

	pod, err := r.CreatePod(ctx, pool, 0, true, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if pod.Spec.SecurityContext == nil || pod.Spec.SecurityContext.RunAsUser == nil || *pod.Spec.SecurityContext.RunAsUser != 1001 {
		t.Fatalf("expected default runAsUser 1001, got %+v", pod.Spec.SecurityContext)
	}

	// Custom security context overrides the default.
	pool2 := testPool()
	pool2.Spec.SecurityContext = karav1.SecurityContext{RunAsUser: 42, Privileged: true}
	r2 := newTestRunner(t, pool2)
	pod2, err := r2.CreatePod(ctx, pool2, 0, true, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if *pod2.Spec.SecurityContext.RunAsUser != 42 {
		t.Fatalf("runAsUser = %d", *pod2.Spec.SecurityContext.RunAsUser)
	}
	sc := pod2.Spec.Containers[0].SecurityContext
	if sc == nil || sc.Privileged == nil || !*sc.Privileged {
		t.Fatalf("privileged not applied: %+v", sc)
	}
}

func TestGetNextAvailableAgentIndex(t *testing.T) {
	ctx := context.Background()
	pool := testPool()
	r := newTestRunner(t, pool)

	if index, err := r.GetNextAvailableAgentIndex(ctx, pool); err != nil || index != 0 {
		t.Fatalf("empty pool: index=%d err=%v", index, err)
	}

	// Take indexes 0 and 1 (one completed pod still counts as occupying
	// its index until it is garbage-collected).
	for _, tc := range []struct {
		index int
		phase corev1.PodPhase
	}{{0, corev1.PodRunning}, {1, corev1.PodSucceeded}} {
		p := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("testpool-agent-%d", tc.index),
				Namespace: "default",
				Labels:    map[string]string{LabelRunnerPool: "testpool"},
			},
			Status: corev1.PodStatus{Phase: tc.phase},
		}
		if err := r.Client.Create(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if index, err := r.GetNextAvailableAgentIndex(ctx, pool); err != nil || index != 2 {
		t.Fatalf("indexes 0,1 taken: index=%d err=%v", index, err)
	}

	// Max agents reached (5 active) → -1.
	for i := 2; i < 5; i++ {
		p := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("testpool-agent-%d", i),
				Namespace: "default",
				Labels:    map[string]string{LabelRunnerPool: "testpool"},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		}
		if err := r.Client.Create(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if index, err := r.GetNextAvailableAgentIndex(ctx, pool); err != nil || index != -1 {
		t.Fatalf("at max: index=%d err=%v", index, err)
	}
}

func strPtrV(s string) *string { return &s }
