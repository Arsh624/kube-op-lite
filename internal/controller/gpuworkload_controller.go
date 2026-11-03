package controller

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	gpuv1alpha1 "github.com/Arsh624/kube-op-lite/api/v1alpha1"
)

const (
	// finalizer lets us observe deletion so we can drain the pods and report
	// Terminated before the object disappears.
	finalizerName = "gpu.kube-op-lite.io/finalizer"

	// Label / annotation keys used to surface the GPU request and priority.
	labelManagedBy = "app.kubernetes.io/managed-by"
	labelWorkload  = "gpu.kube-op-lite.io/workload"
	labelGpuCount  = "gpu.kube-op-lite.io/gpu-count"
	annPriority    = "gpu.kube-op-lite.io/priority"
	annGpuCount    = "gpu.kube-op-lite.io/gpu-count"

	// gpuResource is the extended resource the NVIDIA device plugin advertises.
	// Requesting it makes the scheduler place pods only where GPUs are free.
	gpuResource corev1.ResourceName = "nvidia.com/gpu"

	condAvailable     = "Available"
	condUnschedulable = "GpuUnschedulable"

	requeueInterval = 10 * time.Second
	drainInterval   = 2 * time.Second
)

// priorityValues maps the GpuWorkload priority onto PriorityClass values. A
// higher value lets the scheduler preempt lower-priority pods when GPUs run out.
var priorityValues = map[gpuv1alpha1.Priority]int32{
	gpuv1alpha1.PriorityLow:    1000,
	gpuv1alpha1.PriorityMedium: 10000,
	gpuv1alpha1.PriorityHigh:   100000,
}

// GpuWorkloadReconciler reconciles a GpuWorkload object.
type GpuWorkloadReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=gpu.kube-op-lite.io,resources=gpuworkloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gpu.kube-op-lite.io,resources=gpuworkloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=gpu.kube-op-lite.io,resources=gpuworkloads/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=scheduling.k8s.io,resources=priorityclasses,verbs=get;list;watch;create

// Reconcile drives the actual cluster state toward the desired state declared on
// the GpuWorkload. It is the control loop: read desired, read actual, make them
// match, report status, and requeue until they converge.
func (r *GpuWorkloadReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// 1. Fetch the desired state.
	var wl gpuv1alpha1.GpuWorkload
	if err := r.Get(ctx, req.NamespacedName, &wl); err != nil {
		// Not found: it was deleted and its children are being garbage collected
		// via owner references. Nothing to do.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// 2. Handle deletion (drain) via the finalizer.
	if !wl.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &wl)
	}

	// Ensure our finalizer is present so we get a chance to drain on delete.
	if !controllerutil.ContainsFinalizer(&wl, finalizerName) {
		controllerutil.AddFinalizer(&wl, finalizerName)
		if err := r.Update(ctx, &wl); err != nil {
			return ctrl.Result{}, err
		}
		// Adding a finalizer does not bump generation, so the predicate filters
		// out the resulting event. Carry on in this pass instead of waiting.
	}

	// 3 + 4. Make sure the PriorityClass exists, then reconcile the child
	// Deployment and Service (with owner references).
	if err := r.ensurePriorityClass(ctx, wl.Spec.Priority); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring priority class: %w", err)
	}
	if err := r.reconcileDeployment(ctx, &wl); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling deployment: %w", err)
	}
	if err := r.reconcileService(ctx, &wl); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling service: %w", err)
	}

	// 5. Read actual state back and update status.
	var dep appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Name: wl.Name, Namespace: wl.Namespace}, &dep); err != nil {
		return ctrl.Result{}, err
	}
	converged, err := r.updateStatus(ctx, &wl, &dep)
	if err != nil {
		return ctrl.Result{}, err
	}

	// 6. Requeue until desired and actual converge.
	if !converged {
		logger.Info("not yet converged, requeueing",
			"desired", wl.Spec.Replicas, "available", dep.Status.AvailableReplicas)
		return ctrl.Result{RequeueAfter: requeueInterval}, nil
	}
	return ctrl.Result{}, nil
}

// reconcileDelete scales the child Deployment to zero, waits for its pods to
// terminate (Draining), reports Terminated, and only then removes the finalizer
// so owner-reference garbage collection can clean up the children.
func (r *GpuWorkloadReconciler) reconcileDelete(ctx context.Context, wl *gpuv1alpha1.GpuWorkload) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	if !controllerutil.ContainsFinalizer(wl, finalizerName) {
		return ctrl.Result{}, nil
	}

	var dep appsv1.Deployment
	err := r.Get(ctx, types.NamespacedName{Name: wl.Name, Namespace: wl.Namespace}, &dep)
	if err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	if err == nil && (dep.Spec.Replicas == nil || *dep.Spec.Replicas != 0 || dep.Status.Replicas != 0) {
		if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 0 {
			logger.Info("draining workload", "name", wl.Name)
			zero := int32(0)
			dep.Spec.Replicas = &zero
			if err := r.Update(ctx, &dep); err != nil {
				return ctrl.Result{}, err
			}
		}
		if err := r.setPhase(ctx, wl, gpuv1alpha1.PhaseDraining); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: drainInterval}, nil
	}

	// All pods are gone: report Terminated, then let the object go.
	if err := r.setPhase(ctx, wl, gpuv1alpha1.PhaseTerminated); err != nil {
		return ctrl.Result{}, err
	}
	gpusAllocated.DeleteLabelValues(wl.Namespace, wl.Name, string(wl.Spec.Priority))
	controllerutil.RemoveFinalizer(wl, finalizerName)
	if err := r.Update(ctx, wl); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// setPhase writes only the phase, skipping the API call when nothing changed.
func (r *GpuWorkloadReconciler) setPhase(ctx context.Context, wl *gpuv1alpha1.GpuWorkload, phase gpuv1alpha1.GpuWorkloadPhase) error {
	if wl.Status.Phase == phase {
		return nil
	}
	wl.Status.Phase = phase
	now := metav1.Now()
	wl.Status.LastReconcileTime = &now
	return r.Status().Update(ctx, wl)
}

// ensurePriorityClass creates the cluster-scoped PriorityClass backing a
// priority level if it does not exist yet. Values are immutable, so it never updates.
func (r *GpuWorkloadReconciler) ensurePriorityClass(ctx context.Context, p gpuv1alpha1.Priority) error {
	name := priorityClassName(p)
	var pc schedulingv1.PriorityClass
	err := r.Get(ctx, client.ObjectKey{Name: name}, &pc)
	if !apierrors.IsNotFound(err) {
		return err
	}
	if p == "" {
		p = gpuv1alpha1.PriorityMedium
	}
	pc = schedulingv1.PriorityClass{
		ObjectMeta:  metav1.ObjectMeta{Name: name, Labels: map[string]string{labelManagedBy: "kube-op-lite"}},
		Value:       priorityValues[p],
		Description: fmt.Sprintf("kube-op-lite %s priority GPU workloads", p),
	}
	if err := r.Create(ctx, &pc); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func priorityClassName(p gpuv1alpha1.Priority) string {
	if p == "" {
		p = gpuv1alpha1.PriorityMedium
	}
	return "kube-op-lite-" + string(p)
}

// reconcileDeployment creates or updates the child Deployment to match the spec.
func (r *GpuWorkloadReconciler) reconcileDeployment(ctx context.Context, wl *gpuv1alpha1.GpuWorkload) error {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: wl.Name, Namespace: wl.Namespace},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, dep, func() error {
		replicas := wl.Spec.Replicas
		selector := map[string]string{labelWorkload: wl.Name}
		podLabels := map[string]string{
			labelWorkload:  wl.Name,
			labelManagedBy: "kube-op-lite",
			labelGpuCount:  fmt.Sprintf("%d", wl.Spec.GpuCount),
		}
		podAnnotations := map[string]string{
			annPriority: string(wl.Spec.Priority),
			annGpuCount: fmt.Sprintf("%d", wl.Spec.GpuCount),
		}

		dep.Labels = podLabels
		dep.Spec.Replicas = &replicas
		dep.Spec.Selector = &metav1.LabelSelector{MatchLabels: selector}
		dep.Spec.Template = corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{
				Labels:      podLabels,
				Annotations: podAnnotations,
			},
			Spec: corev1.PodSpec{
				PriorityClassName: priorityClassName(wl.Spec.Priority),
				Containers: []corev1.Container{{
					Name:      "workload",
					Image:     wl.Spec.Image,
					Resources: gpuResources(wl.Spec.GpuCount),
				}},
			},
		}

		// Owner reference: deleting the GpuWorkload cascades to this Deployment.
		return controllerutil.SetControllerReference(wl, dep, r.Scheme)
	})
	return err
}

// gpuResources requests real nvidia.com/gpu units so the scheduler enforces node
// GPU capacity. Extended resources only need a limit; the request defaults to it.
func gpuResources(count int32) corev1.ResourceRequirements {
	if count == 0 {
		return corev1.ResourceRequirements{}
	}
	return corev1.ResourceRequirements{
		Limits: corev1.ResourceList{gpuResource: *resource.NewQuantity(int64(count), resource.DecimalSI)},
	}
}

// reconcileService creates or updates a ClusterIP Service fronting the pods.
func (r *GpuWorkloadReconciler) reconcileService(ctx context.Context, wl *gpuv1alpha1.GpuWorkload) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: wl.Name, Namespace: wl.Namespace},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		svc.Labels = map[string]string{labelManagedBy: "kube-op-lite", labelWorkload: wl.Name}
		svc.Spec.Selector = map[string]string{labelWorkload: wl.Name}
		svc.Spec.Type = corev1.ServiceTypeClusterIP
		svc.Spec.Ports = []corev1.ServicePort{{
			Name:       "http",
			Port:       80,
			TargetPort: intstr.FromInt32(80),
			Protocol:   corev1.ProtocolTCP,
		}}
		return controllerutil.SetControllerReference(wl, svc, r.Scheme)
	})
	return err
}

// unschedulableMessage returns the scheduler's message for the first pod of the
// workload that cannot be placed (e.g. "0/1 nodes are available: 1 Insufficient nvidia.com/gpu").
func (r *GpuWorkloadReconciler) unschedulableMessage(ctx context.Context, wl *gpuv1alpha1.GpuWorkload) (string, error) {
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(wl.Namespace), client.MatchingLabels{labelWorkload: wl.Name}); err != nil {
		return "", err
	}
	for _, p := range pods.Items {
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason == corev1.PodReasonUnschedulable {
				return c.Message, nil
			}
		}
	}
	return "", nil
}

// updateStatus derives the phase and conditions from the Deployment and its pods,
// and writes them back only if they changed. Writing an unchanged status (or a
// fresh timestamp every pass) would cost an API call and wake watchers for nothing.
// It returns whether the workload has converged to its desired replica count.
func (r *GpuWorkloadReconciler) updateStatus(ctx context.Context, wl *gpuv1alpha1.GpuWorkload, dep *appsv1.Deployment) (bool, error) {
	available := dep.Status.AvailableReplicas
	desired := wl.Spec.Replicas
	converged := available == desired

	unschedulable, err := r.unschedulableMessage(ctx, wl)
	if err != nil {
		return false, err
	}

	old := wl.Status.DeepCopy()
	phase := gpuv1alpha1.PhaseProvisioning
	switch {
	case converged:
		phase = gpuv1alpha1.PhaseRunning
	case unschedulable != "":
		phase = gpuv1alpha1.PhasePending
	case available == 0 && (old.Phase == "" || old.Phase == gpuv1alpha1.PhasePending):
		phase = gpuv1alpha1.PhasePending
	}
	wl.Status.Phase = phase
	wl.Status.AvailableReplicas = available

	availCond := metav1.Condition{
		Type:               condAvailable,
		ObservedGeneration: wl.Generation,
		Status:             metav1.ConditionFalse,
		Reason:             "Provisioning",
		Message:            fmt.Sprintf("%d/%d replicas are available", available, desired),
	}
	if converged {
		availCond.Status, availCond.Reason = metav1.ConditionTrue, "MinimumReplicasAvailable"
	}
	meta.SetStatusCondition(&wl.Status.Conditions, availCond)

	gpuCond := metav1.Condition{
		Type:               condUnschedulable,
		ObservedGeneration: wl.Generation,
		Status:             metav1.ConditionFalse,
		Reason:             "Scheduled",
		Message:            "all pods are placed",
	}
	if unschedulable != "" {
		gpuCond.Status, gpuCond.Reason, gpuCond.Message = metav1.ConditionTrue, "InsufficientGPU", unschedulable
	}
	meta.SetStatusCondition(&wl.Status.Conditions, gpuCond)

	gpusAllocated.WithLabelValues(wl.Namespace, wl.Name, string(wl.Spec.Priority)).
		Set(float64(available * wl.Spec.GpuCount))

	// Compare ignoring the timestamp so only real changes are written.
	next := wl.Status.DeepCopy()
	next.LastReconcileTime = old.LastReconcileTime
	if equality.Semantic.DeepEqual(old, next) {
		return converged, nil
	}

	if phase == gpuv1alpha1.PhaseRunning && old.Phase != gpuv1alpha1.PhaseRunning {
		timeToRunning.Observe(time.Since(wl.CreationTimestamp.Time).Seconds())
	}
	if unschedulable != "" && !meta.IsStatusConditionTrue(old.Conditions, condUnschedulable) {
		unschedulableTotal.WithLabelValues(string(wl.Spec.Priority)).Inc()
	}
	now := metav1.Now()
	wl.Status.LastReconcileTime = &now
	return converged, r.Status().Update(ctx, wl)
}

// SetupWithManager registers the controller and tells it to watch the child
// Deployment and Service it owns, so changes to them re-trigger reconcile.
// GenerationChangedPredicate drops status-only updates to the GpuWorkload, so the
// controller's own status writes cannot re-trigger it in a loop.
func (r *GpuWorkloadReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gpuv1alpha1.GpuWorkload{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Complete(r)
}
