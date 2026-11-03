package controller

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	gpuv1alpha1 "github.com/Arsh624/kube-op-lite/api/v1alpha1"
)

var _ = Describe("GpuWorkload controller", func() {
	const (
		name      = "demo"
		namespace = "default"
		timeout   = 10 * time.Second
		interval  = 250 * time.Millisecond
	)

	key := types.NamespacedName{Name: name, Namespace: namespace}

	reconcileOnce := func() {
		r := &GpuWorkloadReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		_, err := r.Reconcile(ctx, ctrlRequest(key))
		Expect(err).NotTo(HaveOccurred())
	}

	AfterEach(func() {
		wl := &gpuv1alpha1.GpuWorkload{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
		_ = k8sClient.Delete(ctx, wl)
		// envtest runs no manager or garbage collector: drive the finalizer
		// ourselves and remove the children so the next spec starts clean.
		Eventually(func() bool {
			r := &GpuWorkloadReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, _ = r.Reconcile(ctx, ctrlRequest(key))
			return apierrors.IsNotFound(k8sClient.Get(ctx, key, &gpuv1alpha1.GpuWorkload{}))
		}, timeout, interval).Should(BeTrue())
		_ = k8sClient.Delete(ctx, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}})
		_ = k8sClient.Delete(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}})
	})

	It("creates a child Deployment owned by the GpuWorkload", func() {
		By("creating a GpuWorkload")
		wl := &gpuv1alpha1.GpuWorkload{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: gpuv1alpha1.GpuWorkloadSpec{
				Replicas: 3,
				Image:    "nginx:1.27",
				GpuCount: 2,
				Priority: gpuv1alpha1.PriorityHigh,
			},
		}
		Expect(k8sClient.Create(ctx, wl)).To(Succeed())

		By("reconciling (adds finalizer, then creates children)")
		reconcileOnce() // adds finalizer
		reconcileOnce() // creates deployment + service

		By("expecting a Deployment with the desired replicas and image")
		dep := &appsv1.Deployment{}
		Eventually(func() error {
			return k8sClient.Get(ctx, key, dep)
		}, timeout, interval).Should(Succeed())

		Expect(*dep.Spec.Replicas).To(Equal(int32(3)))
		Expect(dep.Spec.Template.Spec.Containers).To(HaveLen(1))
		Expect(dep.Spec.Template.Spec.Containers[0].Image).To(Equal("nginx:1.27"))

		By("expecting the simulated GPU count and priority to be surfaced")
		Expect(dep.Spec.Template.Labels).To(HaveKeyWithValue(labelGpuCount, "2"))
		Expect(dep.Spec.Template.Annotations).To(HaveKeyWithValue(annPriority, "high"))

		By("expecting an owner reference back to the GpuWorkload")
		Expect(dep.OwnerReferences).To(HaveLen(1))
		Expect(dep.OwnerReferences[0].Kind).To(Equal("GpuWorkload"))
		Expect(dep.OwnerReferences[0].Name).To(Equal(name))
	})

	It("updates the child Deployment when the image changes (upgrade)", func() {
		wl := &gpuv1alpha1.GpuWorkload{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec:       gpuv1alpha1.GpuWorkloadSpec{Replicas: 1, Image: "nginx:1.27", Priority: gpuv1alpha1.PriorityLow},
		}
		Expect(k8sClient.Create(ctx, wl)).To(Succeed())
		reconcileOnce()
		reconcileOnce()

		By("changing the image")
		Expect(k8sClient.Get(ctx, key, wl)).To(Succeed())
		wl.Spec.Image = "nginx:1.28"
		Expect(k8sClient.Update(ctx, wl)).To(Succeed())
		reconcileOnce()

		dep := &appsv1.Deployment{}
		Eventually(func() string {
			if err := k8sClient.Get(ctx, key, dep); err != nil {
				return ""
			}
			return dep.Spec.Template.Spec.Containers[0].Image
		}, timeout, interval).Should(Equal("nginx:1.28"))
	})

	It("reports a phase in status", func() {
		wl := &gpuv1alpha1.GpuWorkload{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec:       gpuv1alpha1.GpuWorkloadSpec{Replicas: 1, Image: "nginx:1.27", Priority: gpuv1alpha1.PriorityMedium},
		}
		Expect(k8sClient.Create(ctx, wl)).To(Succeed())
		reconcileOnce()
		reconcileOnce()

		Eventually(func() gpuv1alpha1.GpuWorkloadPhase {
			got := &gpuv1alpha1.GpuWorkload{}
			if err := k8sClient.Get(ctx, key, got); err != nil {
				return ""
			}
			return got.Status.Phase
		}, timeout, interval).ShouldNot(BeEmpty())
	})

	It("requests real GPUs and a priority class the scheduler can preempt with", func() {
		wl := &gpuv1alpha1.GpuWorkload{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec:       gpuv1alpha1.GpuWorkloadSpec{Replicas: 1, Image: "nginx:1.27", GpuCount: 4, Priority: gpuv1alpha1.PriorityHigh},
		}
		Expect(k8sClient.Create(ctx, wl)).To(Succeed())
		reconcileOnce()

		dep := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, key, dep)).To(Succeed())
		limit := dep.Spec.Template.Spec.Containers[0].Resources.Limits[gpuResource]
		Expect(limit.Value()).To(Equal(int64(4)))
		Expect(dep.Spec.Template.Spec.PriorityClassName).To(Equal("kube-op-lite-high"))

		pc := &schedulingv1.PriorityClass{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "kube-op-lite-high"}, pc)).To(Succeed())
		Expect(pc.Value).To(Equal(int32(100000)))
	})

	It("drains to zero replicas, then reports Terminated and releases the object", func() {
		wl := &gpuv1alpha1.GpuWorkload{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec:       gpuv1alpha1.GpuWorkloadSpec{Replicas: 2, Image: "nginx:1.27", Priority: gpuv1alpha1.PriorityLow},
		}
		Expect(k8sClient.Create(ctx, wl)).To(Succeed())
		reconcileOnce()

		By("deleting: the finalizer holds the object while the Deployment scales to zero")
		Expect(k8sClient.Delete(ctx, wl)).To(Succeed())
		reconcileOnce()
		dep := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, key, dep)).To(Succeed())
		Expect(*dep.Spec.Replicas).To(Equal(int32(0)))
		got := &gpuv1alpha1.GpuWorkload{}
		Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
		Expect(got.Status.Phase).To(Equal(gpuv1alpha1.PhaseDraining))

		By("once no pods remain, the finalizer is removed")
		reconcileOnce()
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, key, &gpuv1alpha1.GpuWorkload{}))
		}, timeout, interval).Should(BeTrue())
	})

	It("does not rewrite status when nothing changed", func() {
		wl := &gpuv1alpha1.GpuWorkload{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec:       gpuv1alpha1.GpuWorkloadSpec{Replicas: 1, Image: "nginx:1.27", Priority: gpuv1alpha1.PriorityMedium},
		}
		Expect(k8sClient.Create(ctx, wl)).To(Succeed())
		reconcileOnce()
		before := &gpuv1alpha1.GpuWorkload{}
		Expect(k8sClient.Get(ctx, key, before)).To(Succeed())

		reconcileOnce()
		after := &gpuv1alpha1.GpuWorkload{}
		Expect(k8sClient.Get(ctx, key, after)).To(Succeed())
		Expect(after.ResourceVersion).To(Equal(before.ResourceVersion))
	})

	It("rejects an unknown object lookup cleanly", func() {
		r := &GpuWorkloadReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		_, err := r.Reconcile(ctx, ctrlRequest(types.NamespacedName{Name: "nope", Namespace: namespace}))
		Expect(err).NotTo(HaveOccurred())
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: "nope", Namespace: namespace}, &gpuv1alpha1.GpuWorkload{}))).To(BeTrue())
	})
})
