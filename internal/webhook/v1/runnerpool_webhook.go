// File: webhook/v1/runnerpool_webhook.go
// Description: Mutating and validating webhooks for RunnerPool CRD.
// The defaulter and validator port the behavior of the C# operator's
// V1RunnerPoolMutationWebhook and V1RunnerPoolValidationWebhook.
package v1

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	karav1 "github.com/mahmoudk1000/azdo-runner-operator/api/v1"
)

// log is for logging in this package.
var runnerpoollog = logf.Log.WithName("runnerpool-webhook")

// SetupRunnerPoolWebhookWithManager registers the webhook for RunnerPool in the manager.
func SetupRunnerPoolWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&karav1.RunnerPool{}).
		WithValidator(&RunnerPoolCustomValidator{}).
		WithDefaulter(&RunnerPoolCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-kara-felukka-org-v1-runnerpool,mutating=true,failurePolicy=fail,sideEffects=None,groups=kara.felukka.org,resources=runnerpools,verbs=create;update,versions=v1,name=mrunnerpool-v1.kb.io,admissionReviewVersions=v1

// RunnerPoolCustomDefaulter sets default values on RunnerPool resources.
//
// +kubebuilder:object:generate=false
type RunnerPoolCustomDefaulter struct{}

var _ webhook.CustomDefaulter = &RunnerPoolCustomDefaulter{}

const (
	defaultAgentImage  = "ghcr.io/mahmoudk1000/kara/agent:latest"
	defaultStorageSize = "1Gi"
	defaultManagedBy   = "azdo-runner-operator"
	defaultTTLIdle     = 300 // 5 minutes
)

// Default applies default values to the RunnerPool spec.
func (d *RunnerPoolCustomDefaulter) Default(_ context.Context, obj runtime.Object) error {
	runnerpool, ok := obj.(*karav1.RunnerPool)
	if !ok {
		return fmt.Errorf("expected a RunnerPool object but got %T", obj)
	}
	runnerpoollog.Info("Defaulting for RunnerPool", "name", runnerpool.GetName())

	labels := runnerpool.Labels
	if labels == nil {
		labels = map[string]string{}
		runnerpool.Labels = labels
	}
	if _, ok := labels["managed-by"]; !ok {
		labels["managed-by"] = defaultManagedBy
	}

	if strings.TrimSpace(runnerpool.Spec.Pool) == "" {
		runnerpool.Spec.Pool = "default"
	}
	if strings.TrimSpace(runnerpool.Spec.Image) == "" {
		runnerpool.Spec.Image = defaultAgentImage
	}
	if strings.TrimSpace(runnerpool.Spec.ImagePullPolicy) == "" {
		runnerpool.Spec.ImagePullPolicy = "IfNotPresent"
	}
	// 0 is the "unset" marker: it is replaced by the default TTL, so the
	// explicit one-shot mode is only reachable with webhooks disabled
	// (same behavior as the C# defaulter).
	if runnerpool.Spec.TtlIdleSeconds == 0 {
		runnerpool.Spec.TtlIdleSeconds = defaultTTLIdle
	}
	if runnerpool.Spec.MinAgents == 0 && runnerpool.Spec.MaxAgents == 0 {
		runnerpool.Spec.MinAgents = 0
		runnerpool.Spec.MaxAgents = 5
	} else if runnerpool.Spec.MaxAgents == 0 {
		max := runnerpool.Spec.MinAgents
		if max < 1 {
			max = 1
		}
		runnerpool.Spec.MaxAgents = max
	}

	for i := range runnerpool.Spec.Storage {
		if runnerpool.Spec.Storage[i].CreatePvc && strings.TrimSpace(runnerpool.Spec.Storage[i].Size) == "" {
			runnerpool.Spec.Storage[i].Size = defaultStorageSize
		}
	}

	return nil
}

// +kubebuilder:webhook:path=/validate-kara-felukka-org-v1-runnerpool,mutating=false,failurePolicy=fail,sideEffects=None,groups=kara.felukka.org,resources=runnerpools,verbs=create;update,versions=v1,name=vrunnerpool-v1.kb.io,admissionReviewVersions=v1

// RunnerPoolCustomValidator validates RunnerPool resources.
//
// +kubebuilder:object:generate=false
type RunnerPoolCustomValidator struct{}

var _ webhook.CustomValidator = &RunnerPoolCustomValidator{}

var (
	validStorageUnits = regexp.MustCompile(`^\d+(Gi|Mi|Ki)$`)
	rfc1123Name       = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	envVarName        = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// ValidateCreate validates a new RunnerPool resource.
func (v *RunnerPoolCustomValidator) ValidateCreate(
	_ context.Context,
	obj runtime.Object,
) (admission.Warnings, error) {
	runnerpool, ok := obj.(*karav1.RunnerPool)
	if !ok {
		return nil, fmt.Errorf("expected a RunnerPool object but got %T", obj)
	}
	runnerpoollog.Info("Validation for RunnerPool upon creation", "name", runnerpool.GetName())

	return v.validateSpec(runnerpool)
}

// ValidateUpdate validates an updated RunnerPool resource.
func (v *RunnerPoolCustomValidator) ValidateUpdate(
	_ context.Context,
	_, newObj runtime.Object,
) (admission.Warnings, error) {
	runnerpool, ok := newObj.(*karav1.RunnerPool)
	if !ok {
		return nil, fmt.Errorf("expected a RunnerPool object but got %T", newObj)
	}
	runnerpoollog.Info("Validation for RunnerPool upon update", "name", runnerpool.GetName())

	return v.validateSpec(runnerpool)
}

// ValidateDelete is not enforced for RunnerPool — deletions are handled by the finalizer.
func (v *RunnerPoolCustomValidator) ValidateDelete(
	_ context.Context,
	obj runtime.Object,
) (admission.Warnings, error) {
	runnerpool, ok := obj.(*karav1.RunnerPool)
	if !ok {
		return nil, fmt.Errorf("expected a RunnerPool object but got %T", obj)
	}
	runnerpoollog.Info("Validation for RunnerPool upon deletion", "name", runnerpool.GetName())
	return nil, nil
}

// validateSpec performs the business-logic, env, storage and cert checks.
func (v *RunnerPoolCustomValidator) validateSpec(rp *karav1.RunnerPool) (admission.Warnings, error) {
	var errMsgs []string

	if strings.TrimSpace(rp.Spec.AzURL) == "" {
		errMsgs = append(errMsgs, "spec.azUrl is required and cannot be empty")
	} else if u, err := url.Parse(rp.Spec.AzURL); err != nil ||
		(u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		errMsgs = append(errMsgs, "spec.azUrl must be a valid HTTP or HTTPS URL")
	}

	if strings.TrimSpace(rp.Spec.PATSecretName) == "" {
		errMsgs = append(errMsgs, "spec.patSecretName is required and cannot be empty")
	}

	if strings.TrimSpace(rp.Spec.Image) != "" && strings.ContainsAny(rp.Spec.Image, " \t") {
		errMsgs = append(errMsgs, "spec.image cannot contain spaces or tabs")
	}

	switch rp.Spec.ImagePullPolicy {
	case "", "Always", "IfNotPresent", "Never":
	default:
		errMsgs = append(errMsgs, "spec.imagePullPolicy must be one of: Always, IfNotPresent, Never")
	}

	if rp.Spec.TtlIdleSeconds < 0 {
		errMsgs = append(errMsgs, "spec.ttlIdleSeconds must be a non-negative value")
	}
	if rp.Spec.MinAgents < 0 {
		errMsgs = append(errMsgs, "spec.minAgents must be a non-negative value")
	}
	if rp.Spec.MaxAgents < 1 {
		errMsgs = append(errMsgs, "spec.maxAgents must be at least 1")
	}
	if rp.Spec.MinAgents > rp.Spec.MaxAgents {
		errMsgs = append(errMsgs, fmt.Sprintf("spec.minAgents (%d) cannot be greater than spec.maxAgents (%d)",
			rp.Spec.MinAgents, rp.Spec.MaxAgents))
	}
	if rp.Spec.PollIntervalSeconds != 0 && rp.Spec.PollIntervalSeconds < 5 {
		errMsgs = append(errMsgs, "spec.pollIntervalSeconds must be at least 5")
	}

	envNames := map[string]bool{}
	for _, env := range rp.Spec.ExtraEnv {
		if strings.TrimSpace(env.Name) == "" {
			errMsgs = append(errMsgs, "spec.extraEnv entries must have a non-empty name")
			continue
		}
		if envNames[env.Name] {
			errMsgs = append(errMsgs, fmt.Sprintf("duplicate environment variable name '%s' found in spec.extraEnv", env.Name))
			continue
		}
		envNames[env.Name] = true
		if !envVarName.MatchString(env.Name) {
			errMsgs = append(errMsgs, fmt.Sprintf(
				"invalid spec.extraEnv name '%s'. Must contain only alphanumeric characters and underscores, and cannot start with a digit",
				env.Name))
		}
		hasValue := env.Value != nil && *env.Value != ""
		hasValueFrom := env.ValueFrom != nil
		if !hasValue && !hasValueFrom {
			errMsgs = append(errMsgs, fmt.Sprintf("spec.extraEnv entry '%s' must have either value or valueFrom", env.Name))
		}
		if hasValue && hasValueFrom {
			errMsgs = append(errMsgs, fmt.Sprintf("spec.extraEnv entry '%s' cannot have both value and valueFrom", env.Name))
		}
	}

	errMsgs = append(errMsgs, validateStorage(rp.Spec.Storage)...)
	errMsgs = append(errMsgs, validateCertTrustStore(rp.Spec.CertTrustStore)...)

	if len(errMsgs) > 0 {
		return nil, fmt.Errorf("validation failed: %s", strings.Join(errMsgs, "; "))
	}
	return nil, nil
}

func validateStorage(storage []karav1.StorageSpec) []string {
	var errs []string
	names := map[string]bool{}
	mountPaths := map[string]bool{}

	for _, pvc := range storage {
		if strings.TrimSpace(pvc.Name) == "" {
			errs = append(errs, "spec.storage entries must have a non-empty name")
			continue
		}
		if names[pvc.Name] {
			errs = append(errs, fmt.Sprintf("duplicate storage name '%s' found in spec.storage", pvc.Name))
			continue
		}
		names[pvc.Name] = true

		if !isValidKubernetesName(pvc.Name) {
			errs = append(errs, fmt.Sprintf("invalid storage name '%s'. Must be a valid Kubernetes name (RFC 1123)", pvc.Name))
		}

		if strings.TrimSpace(pvc.MountPath) == "" {
			errs = append(errs, fmt.Sprintf("storage '%s' must have a mountPath specified", pvc.Name))
		} else {
			if mountPaths[pvc.MountPath] {
				errs = append(errs, fmt.Sprintf("duplicate mount path '%s' found in spec.storage", pvc.MountPath))
			}
			mountPaths[pvc.MountPath] = true
			if !strings.HasPrefix(pvc.MountPath, "/") {
				errs = append(errs, fmt.Sprintf("storage '%s' mount path '%s' must be an absolute path (start with '/')", pvc.Name, pvc.MountPath))
			}
		}

		if pvc.CreatePvc {
			if strings.TrimSpace(pvc.Size) == "" {
				errs = append(errs, fmt.Sprintf("storage '%s' has createPvc=true but no size specified. A size is required when creating a PVC", pvc.Name))
			} else if !isValidStorageQuantity(pvc.Size) {
				errs = append(errs, fmt.Sprintf("storage '%s' has invalid size '%s'. Must use units: Gi, Mi, or Ki (e.g., '1Gi', '500Mi')", pvc.Name, pvc.Size))
			}
		}

		if strings.TrimSpace(pvc.StorageClass) != "" && !isValidKubernetesName(pvc.StorageClass) {
			errs = append(errs, fmt.Sprintf("storage '%s' has invalid storage class name '%s'. Must be a valid Kubernetes name", pvc.Name, pvc.StorageClass))
		}

		if !pvc.CreatePvc && pvc.DeleteWithAgent {
			errs = append(errs, fmt.Sprintf("storage '%s' has createPvc=false but deleteWithAgent=true. Cannot delete a PVC that wasn't created by this operator", pvc.Name))
		}
	}
	return errs
}

func validateCertTrustStore(certs []karav1.CertTrustStore) []string {
	var errs []string
	secretNames := map[string]bool{}

	for _, cert := range certs {
		if strings.TrimSpace(cert.SecretName) == "" {
			errs = append(errs, "spec.certTrustStore entries must have a non-empty secretName")
			continue
		}
		if secretNames[cert.SecretName] {
			errs = append(errs, fmt.Sprintf("duplicate secret name '%s' found in spec.certTrustStore", cert.SecretName))
			continue
		}
		secretNames[cert.SecretName] = true
		if !isValidKubernetesName(cert.SecretName) {
			errs = append(errs, fmt.Sprintf("invalid secret name '%s'. Must be a valid Kubernetes name (RFC 1123)", cert.SecretName))
		}
	}
	return errs
}

func isValidKubernetesName(name string) bool {
	return len(name) > 0 && len(name) <= 63 && rfc1123Name.MatchString(name)
}

func isValidStorageQuantity(quantity string) bool {
	if strings.TrimSpace(quantity) == "" {
		return false
	}
	if quantity == "" {
		return false
	}
	// Plain byte counts and the common binary units are accepted.
	if validStorageUnits.MatchString(quantity) {
		return true
	}
	return regexp.MustCompile(`^\d+$`).MatchString(quantity)
}
