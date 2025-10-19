package v1

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	karav1 "github.com/mahmoudk1000/azdo-runner-operator/api/v1"
)

var _ = Describe("RunnerPool Webhook", func() {
	var (
		ctx       context.Context
		obj       *karav1.RunnerPool
		oldObj    *karav1.RunnerPool
		validator RunnerPoolCustomValidator
		defaulter RunnerPoolCustomDefaulter
	)

	BeforeEach(func() {
		ctx = context.Background()
		obj = validRunnerPool()
		oldObj = validRunnerPool()
		validator = RunnerPoolCustomValidator{}
		defaulter = RunnerPoolCustomDefaulter{}
	})

	Context("When creating RunnerPool under the Defaulting Webhook", func() {
		It("adds the managed-by label when missing", func() {
			Expect(defaulter.Default(ctx, obj)).To(Succeed())
			Expect(obj.Labels).To(HaveKeyWithValue("managed-by", "azdo-runner-operator"))
		})

		It("keeps an existing managed-by label", func() {
			obj.Labels = map[string]string{"managed-by": "custom"}
			Expect(defaulter.Default(ctx, obj)).To(Succeed())
			Expect(obj.Labels).To(HaveKeyWithValue("managed-by", "custom"))
		})

		It("defaults the pool name to 'default'", func() {
			obj.Spec.Pool = ""
			Expect(defaulter.Default(ctx, obj)).To(Succeed())
			Expect(obj.Spec.Pool).To(Equal("default"))
		})

		It("defaults the agent image", func() {
			obj.Spec.Image = ""
			Expect(defaulter.Default(ctx, obj)).To(Succeed())
			Expect(obj.Spec.Image).To(Equal("ghcr.io/mahmoudk1000/kara/agent:latest"))
		})

		It("defaults the image pull policy", func() {
			obj.Spec.ImagePullPolicy = ""
			Expect(defaulter.Default(ctx, obj)).To(Succeed())
			Expect(obj.Spec.ImagePullPolicy).To(Equal("IfNotPresent"))
		})

		It("replaces an unset TTL (0) with 300 seconds", func() {
			obj.Spec.TtlIdleSeconds = 0
			Expect(defaulter.Default(ctx, obj)).To(Succeed())
			Expect(obj.Spec.TtlIdleSeconds).To(Equal(300))
		})

		It("defaults maxAgents to 5 when both min and max are zero", func() {
			obj.Spec.MinAgents = 0
			obj.Spec.MaxAgents = 0
			Expect(defaulter.Default(ctx, obj)).To(Succeed())
			Expect(obj.Spec.MaxAgents).To(Equal(5))
			Expect(obj.Spec.MinAgents).To(Equal(0))
		})

		It("defaults maxAgents to at least minAgents when only max is zero", func() {
			obj.Spec.MinAgents = 3
			obj.Spec.MaxAgents = 0
			Expect(defaulter.Default(ctx, obj)).To(Succeed())
			Expect(obj.Spec.MaxAgents).To(Equal(3))
		})

		It("defaults the size of created PVCs to 1Gi", func() {
			obj.Spec.Storage = []karav1.StorageSpec{
				{Name: "workspace", MountPath: "/work", CreatePvc: true},
			}
			Expect(defaulter.Default(ctx, obj)).To(Succeed())
			Expect(obj.Spec.Storage[0].Size).To(Equal("1Gi"))
		})

		It("leaves an explicit storage size untouched", func() {
			obj.Spec.Storage = []karav1.StorageSpec{
				{Name: "workspace", MountPath: "/work", CreatePvc: true, Size: "10Gi"},
			}
			Expect(defaulter.Default(ctx, obj)).To(Succeed())
			Expect(obj.Spec.Storage[0].Size).To(Equal("10Gi"))
		})

		It("applies defaults end-to-end against the live API server", func() {
			// The webhook server started in BeforeSuite rewrites the object.
			pool := &karav1.RunnerPool{
				ObjectMeta: metav1.ObjectMeta{Name: "e2e-defaulted", Namespace: "default"},
				Spec: karav1.RunnerPoolSpec{
					AzURL:         "https://dev.azure.com/my-org",
					PATSecretName: "my-pat",
				},
			}
			Expect(k8sClient.Create(ctx, pool)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, pool) }()

			Expect(pool.Labels).To(HaveKeyWithValue("managed-by", "azdo-runner-operator"))
			Expect(pool.Spec.Pool).To(Equal("default"))
			Expect(pool.Spec.Image).To(Equal("ghcr.io/mahmoudk1000/kara/agent:latest"))
			Expect(pool.Spec.ImagePullPolicy).To(Equal("IfNotPresent"))
			Expect(pool.Spec.TtlIdleSeconds).To(Equal(300))
			Expect(pool.Spec.MaxAgents).To(Equal(5))
		})
	})

	Context("When creating or updating RunnerPool under the Validating Webhook", func() {
		It("admits a fully valid spec", func() {
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
			_, err = validator.ValidateUpdate(ctx, oldObj, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("rejects a missing azUrl", func() {
			obj.Spec.AzURL = ""
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("azUrl"))
		})

		It("rejects a non-HTTP(S) azUrl", func() {
			obj.Spec.AzURL = "ftp://dev.azure.com/my-org"
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("azUrl"))
		})

		It("accepts an http azUrl (on-prem)", func() {
			obj.Spec.AzURL = "http://tfs.company.com/tfs/Collection"
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("rejects a missing patSecretName", func() {
			obj.Spec.PATSecretName = ""
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("patSecretName"))
		})

		It("rejects an image with spaces", func() {
			obj.Spec.Image = "my image:latest"
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("image"))
		})

		It("rejects an invalid imagePullPolicy", func() {
			obj.Spec.ImagePullPolicy = "Sometimes"
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("imagePullPolicy"))
		})

		It("rejects a negative ttlIdleSeconds", func() {
			obj.Spec.TtlIdleSeconds = -1
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("ttlIdleSeconds"))
		})

		It("rejects a negative minAgents", func() {
			obj.Spec.MinAgents = -1
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("minAgents"))
		})

		It("rejects maxAgents below 1", func() {
			obj.Spec.MaxAgents = 0
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("maxAgents"))
		})

		It("rejects minAgents greater than maxAgents", func() {
			obj.Spec.MinAgents = 10
			obj.Spec.MaxAgents = 5
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("cannot be greater than"))
		})

		It("rejects a pollIntervalSeconds below 5", func() {
			obj.Spec.PollIntervalSeconds = 1
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("pollIntervalSeconds"))
		})

		It("rejects an extra env var without a name", func() {
			obj.Spec.ExtraEnv = []karav1.ExtraEnv{{Value: ptr("x")}}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("non-empty name"))
		})

		It("rejects duplicate extra env var names", func() {
			obj.Spec.ExtraEnv = []karav1.ExtraEnv{
				{Name: "FOO", Value: ptr("1")},
				{Name: "FOO", Value: ptr("2")},
			}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("FOO"))
		})

		It("rejects an invalid extra env var name", func() {
			obj.Spec.ExtraEnv = []karav1.ExtraEnv{{Name: "9FOO", Value: ptr("1")}}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("9FOO"))
		})

		It("rejects an extra env var with neither value nor valueFrom", func() {
			obj.Spec.ExtraEnv = []karav1.ExtraEnv{{Name: "FOO"}}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("value or valueFrom"))
		})

		It("rejects an extra env var with both value and valueFrom", func() {
			obj.Spec.ExtraEnv = []karav1.ExtraEnv{{
				Name:      "FOO",
				Value:     ptr("1"),
				ValueFrom: &karav1.ValueFromSource{ConfigMapKeyRef: &karav1.ConfigMapKeyRef{Name: "cm", Key: "k"}},
			}}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("both value and valueFrom"))
		})

		It("accepts an extra env var backed by a secret key", func() {
			obj.Spec.ExtraEnv = []karav1.ExtraEnv{{
				Name:      "FOO",
				ValueFrom: &karav1.ValueFromSource{SecretKeyRef: &karav1.SecretKeyRef{Name: "sec", Key: "k"}},
			}}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("rejects a storage entry without a name", func() {
			obj.Spec.Storage = []karav1.StorageSpec{{MountPath: "/work"}}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("non-empty name"))
		})

		It("rejects duplicate storage names", func() {
			obj.Spec.Storage = []karav1.StorageSpec{
				{Name: "vol", MountPath: "/a", Size: "1Gi"},
				{Name: "vol", MountPath: "/b", Size: "1Gi"},
			}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("duplicate storage name"))
		})

		It("rejects an invalid (non-RFC1123) storage name", func() {
			obj.Spec.Storage = []karav1.StorageSpec{{Name: "Vol", MountPath: "/a", Size: "1Gi"}}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("RFC 1123"))
		})

		It("rejects a storage entry without a mount path", func() {
			obj.Spec.Storage = []karav1.StorageSpec{{Name: "vol", Size: "1Gi"}}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("mountPath"))
		})

		It("rejects duplicate mount paths", func() {
			obj.Spec.Storage = []karav1.StorageSpec{
				{Name: "vol-a", MountPath: "/work", Size: "1Gi"},
				{Name: "vol-b", MountPath: "/work", Size: "1Gi"},
			}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("duplicate mount path"))
		})

		It("rejects a relative mount path", func() {
			obj.Spec.Storage = []karav1.StorageSpec{{Name: "vol", MountPath: "work", Size: "1Gi"}}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("absolute path"))
		})

		It("rejects a created PVC without a size", func() {
			obj.Spec.Storage = []karav1.StorageSpec{{Name: "vol", MountPath: "/work", CreatePvc: true}}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("size"))
		})

		It("rejects an invalid storage quantity", func() {
			obj.Spec.Storage = []karav1.StorageSpec{{Name: "vol", MountPath: "/work", CreatePvc: true, Size: "1TB"}}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("invalid size"))
		})

		It("accepts an externally-provided PVC (claimName, no create)", func() {
			obj.Spec.Storage = []karav1.StorageSpec{
				{Name: "vol", MountPath: "/work", CreatePvc: false, ClaimName: "existing-pvc"},
			}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("rejects deleteWithAgent on a PVC the operator does not create", func() {
			obj.Spec.Storage = []karav1.StorageSpec{
				{Name: "vol", MountPath: "/work", CreatePvc: false, DeleteWithAgent: true},
			}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("createPvc=false but deleteWithAgent=true"))
		})

		It("rejects a cert trust store entry without a secret name", func() {
			obj.Spec.CertTrustStore = []karav1.CertTrustStore{{}}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("secretName"))
		})

		It("rejects duplicate cert trust store secret names", func() {
			obj.Spec.CertTrustStore = []karav1.CertTrustStore{
				{SecretName: "ca"},
				{SecretName: "ca"},
			}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("duplicate secret name"))
		})

		It("rejects an invalid cert trust store secret name", func() {
			obj.Spec.CertTrustStore = []karav1.CertTrustStore{{SecretName: "CA"}}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("RFC 1123"))
		})

		It("rejects an invalid spec against the live API server", func() {
			bad := &karav1.RunnerPool{
				ObjectMeta: metav1.ObjectMeta{Name: "e2e-invalid", Namespace: "default"},
				Spec: karav1.RunnerPoolSpec{
					AzURL:               "not a url",
					PATSecretName:       "my-pat",
					Image:               "ghcr.io/example/agent:latest",
					ImagePullPolicy:     "IfNotPresent",
					MaxAgents:           5,
					TtlIdleSeconds:      300,
					PollIntervalSeconds: 5,
				},
			}
			err := k8sClient.Create(ctx, bad)
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("CRD round-trip through the API server", func() {
		It("persists the full spec and exposes status after defaulting", func() {
			pool := &karav1.RunnerPool{
				ObjectMeta: metav1.ObjectMeta{Name: "round-trip", Namespace: "default"},
				Spec: karav1.RunnerPoolSpec{
					AzURL:               "https://dev.azure.com/my-org",
					Pool:                "ci-pool",
					PATSecretName:       "my-pat",
					Image:               "ghcr.io/example/agent:latest",
					MaxAgents:           3,
					MinAgents:           1,
					TtlIdleSeconds:      120,
					PollIntervalSeconds: 10,
					CapabilityAware:     true,
					Capabilities:        map[string]string{"os": "linux"},
					CapabilityImages:    map[string]string{"docker": "ghcr.io/example/agent:docker"},
					ExtraEnv:            []karav1.ExtraEnv{{Name: "EXTRA", Value: ptr("1")}},
					Storage: []karav1.StorageSpec{
						{Name: "workspace", MountPath: "/work", CreatePvc: true, Size: "2Gi"},
					},
					CertTrustStore: []karav1.CertTrustStore{{SecretName: "my-ca"}},
				},
			}
			Expect(k8sClient.Create(ctx, pool)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, pool) }()

			got := &karav1.RunnerPool{}
			key := types.NamespacedName{Namespace: "default", Name: "round-trip"}
			Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
			Expect(got.Spec.CapabilityAware).To(BeTrue())
			Expect(got.Spec.CapabilityImages).To(HaveKeyWithValue("docker", "ghcr.io/example/agent:docker"))
			Expect(got.Spec.Storage[0].Size).To(Equal("2Gi"))
			Expect(got.Spec.CertTrustStore[0].SecretName).To(Equal("my-ca"))
			Expect(got.Labels).To(HaveKeyWithValue("managed-by", "azdo-runner-operator"))
		})
	})
})

func ptr(s string) *string { return &s }

// validRunnerPool returns a spec that passes all validation.
func validRunnerPool() *karav1.RunnerPool {
	return &karav1.RunnerPool{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pool", Namespace: "default"},
		Spec: karav1.RunnerPoolSpec{
			AzURL:               "https://dev.azure.com/my-org",
			Pool:                "default",
			PATSecretName:       "my-pat",
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
