package controller

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	gpuv1alpha1 "github.com/Arsh624/kube-op-lite/api/v1alpha1"
)

const (
	// finalizer lets us observe deletion so we can report a Draining phase and
	// scale children down gracefully before the object disappears.
	finalizerName = "gpu.kube-op-lite.io/finalizer"

	// Label / annotation keys used to surface the simulated GPU request and priority.
	labelManagedBy = "app.kubernetes.io/managed-by"
	labelWorkload  = "gpu.kube-op-lite.io/workload"
	labelGpuCount  = "gpu.kube-op-lite.io/gpu-count"
	annPriority    = "gpu.kube-op-lite.io/priority"
	annGpuCount    = "gpu.kube-op-lite.io/gpu-count"

	requeueInterval = 10 * time.Second
)

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
		// The update re-triggers reconcile; return cleanly.
		return ctrl.Result{}, nil
	}

	// 3 + 4. Reconcile the child Deployment and Service (with owner references).
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

// reconcileDelete reports a Draining phase, scales children down, then removes the
// finalizer so the object (and its garbage-collected children) can be removed.
func (r *GpuWorkloadReconciler) reconcileDelete(ctx context.Context, wl *gpuv1alpha1.GpuWorkload) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	if !controllerutil.ContainsFinalizer(wl, finalizerName) {
		return ctrl.Result{}, nil
	}

	logger.Info("draining workload", "name", wl.Name)
	wl.Status.Phase = gpuv1alpha1.PhaseDraining
	now := metav1.Now()
	wl.Status.LastReconcileTime = &now
	if err := r.Status().Update(ctx, wl); err != nil && !apierrors.IsConflict(err) {
		return ctrl.Result{}, err
	}

	// Children are removed automatically by owner-reference garbage collection once
	// the finalizer is gone. Removing the finalizer completes termination.
	controllerutil.RemoveFinalizer(wl, finalizerName)
	if err := r.Update(ctx, wl); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
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
				Containers: []corev1.Container{{
					Name:  "workload",
					Image: wl.Spec.Image,
				}},
			},
		}

		// Owner reference: deleting the GpuWorkload cascades to this Deployment.
		return controllerutil.SetControllerReference(wl, dep, r.Scheme)
	})
	return err
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

// updateStatus derives the phase from the Deployment and writes it back. It
// returns whether the workload has converged to its desired replica count.
func (r *GpuWorkloadReconciler) updateStatus(ctx context.Context, wl *gpuv1alpha1.GpuWorkload, dep *appsv1.Deployment) (bool, error) {
	available := dep.Status.AvailableReplicas
	desired := wl.Spec.Replicas
	converged := available == desired

	phase := gpuv1alpha1.PhaseProvisioning
	switch {
	case available == 0 && desired > 0:
		if wl.Status.Phase == "" {
			phase = gpuv1alpha1.PhasePending
		} else {
			phase = gpuv1alpha1.PhaseProvisioning
		}
	case converged:
		phase = gpuv1alpha1.PhaseRunning
	}

	wl.Status.Phase = phase
	wl.Status.AvailableReplicas = available
	now := metav1.Now()
	wl.Status.LastReconcileTime = &now

	cond := metav1.Condition{
		Type:               "Available",
		ObservedGeneration: wl.Generation,
		LastTransitionTime: now,
	}
	if converged {
		cond.Status = metav1.ConditionTrue
		cond.Reason = "MinimumReplicasAvailable"
		cond.Message = fmt.Sprintf("%d/%d replicas are available", available, desired)
	} else {
		cond.Status = metav1.ConditionFalse
		cond.Reason = "Provisioning"
		cond.Message = fmt.Sprintf("%d/%d replicas are available", available, desired)
	}
	meta.SetStatusCondition(&wl.Status.Conditions, cond)

	if err := r.Status().Update(ctx, wl); err != nil {
		return converged, err
	}
	return converged, nil
}

// SetupWithManager registers the controller and tells it to watch the child
// Deployment and Service it owns, so changes to them re-trigger reconcile.
func (r *GpuWorkloadReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gpuv1alpha1.GpuWorkload{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Complete(r)
}
