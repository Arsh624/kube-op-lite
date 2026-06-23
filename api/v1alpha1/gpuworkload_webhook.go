package v1alpha1

import (
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// gpuworkloadlog is for logging in this package.
var gpuworkloadlog = logf.Log.WithName("gpuworkload-webhook")

// SetupWebhookWithManager wires the defaulting and validating webhooks into the manager.
func (r *GpuWorkload) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-gpu-kube-op-lite-io-v1alpha1-gpuworkload,mutating=true,failurePolicy=fail,sideEffects=None,groups=gpu.kube-op-lite.io,resources=gpuworkloads,verbs=create;update,versions=v1alpha1,name=mgpuworkload.kb.io,admissionReviewVersions=v1

var _ webhook.Defaulter = &GpuWorkload{}

// Default implements the mutating webhook: it fills in sensible defaults so the
// user does not have to specify everything.
func (r *GpuWorkload) Default() {
	gpuworkloadlog.Info("applying defaults", "name", r.Name)

	if r.Spec.Priority == "" {
		r.Spec.Priority = PriorityMedium
	}
	if r.Spec.Replicas == 0 {
		r.Spec.Replicas = 1
	}
}

// +kubebuilder:webhook:path=/validate-gpu-kube-op-lite-io-v1alpha1-gpuworkload,mutating=false,failurePolicy=fail,sideEffects=None,groups=gpu.kube-op-lite.io,resources=gpuworkloads,verbs=create;update,versions=v1alpha1,name=vgpuworkload.kb.io,admissionReviewVersions=v1

var _ webhook.Validator = &GpuWorkload{}

// ValidateCreate implements the validating webhook on create.
func (r *GpuWorkload) ValidateCreate() (admission.Warnings, error) {
	gpuworkloadlog.Info("validating create", "name", r.Name)
	return r.validate()
}

// ValidateUpdate implements the validating webhook on update.
func (r *GpuWorkload) ValidateUpdate(_ runtime.Object) (admission.Warnings, error) {
	gpuworkloadlog.Info("validating update", "name", r.Name)
	return r.validate()
}

// ValidateDelete implements the validating webhook on delete. We allow all deletes.
func (r *GpuWorkload) ValidateDelete() (admission.Warnings, error) {
	return nil, nil
}

// validate enforces the business rules that simple OpenAPI bounds cannot express
// as clearly. (The same bounds are also declared as CRD validation markers as a
// second line of defence.)
func (r *GpuWorkload) validate() (admission.Warnings, error) {
	var errs []error
	var warnings admission.Warnings

	if r.Spec.Replicas < 1 {
		errs = append(errs, fmt.Errorf("spec.replicas must be >= 1 (got %d)", r.Spec.Replicas))
	}
	if r.Spec.GpuCount < 0 {
		errs = append(errs, fmt.Errorf("spec.gpuCount must be >= 0 (got %d)", r.Spec.GpuCount))
	}
	if r.Spec.GpuCount > 8 {
		errs = append(errs, fmt.Errorf("spec.gpuCount must be <= 8 (got %d)", r.Spec.GpuCount))
	}
	if r.Spec.Image == "" {
		errs = append(errs, errors.New("spec.image must not be empty"))
	}
	switch r.Spec.Priority {
	case "", PriorityLow, PriorityMedium, PriorityHigh:
		// valid (empty is defaulted by the mutating webhook)
	default:
		errs = append(errs, fmt.Errorf("spec.priority must be one of low|medium|high (got %q)", r.Spec.Priority))
	}

	if r.Spec.GpuCount >= 6 {
		warnings = append(warnings,
			fmt.Sprintf("gpuCount=%d is high; ensure your nodes can satisfy this before scheduling", r.Spec.GpuCount))
	}

	if len(errs) > 0 {
		return warnings, errors.Join(errs...)
	}
	return warnings, nil
}
