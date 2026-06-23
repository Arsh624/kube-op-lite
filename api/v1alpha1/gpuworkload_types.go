package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Priority drives a scheduling annotation placed on the workload's pods.
// +kubebuilder:validation:Enum=low;medium;high
type Priority string

const (
	PriorityLow    Priority = "low"
	PriorityMedium Priority = "medium"
	PriorityHigh   Priority = "high"
)

// GpuWorkloadPhase is the high-level lifecycle state of a GpuWorkload.
type GpuWorkloadPhase string

const (
	// PhasePending means the object has been accepted but no child resources exist yet.
	PhasePending GpuWorkloadPhase = "Pending"
	// PhaseProvisioning means children exist but the desired replicas are not yet available.
	PhaseProvisioning GpuWorkloadPhase = "Provisioning"
	// PhaseRunning means actual state has converged to desired state.
	PhaseRunning GpuWorkloadPhase = "Running"
	// PhaseDraining means the object is being deleted and its children are scaling down.
	PhaseDraining GpuWorkloadPhase = "Draining"
	// PhaseTerminated means draining is complete and children are gone.
	PhaseTerminated GpuWorkloadPhase = "Terminated"
)

// GpuWorkloadSpec is the desired state: what the user asks for.
type GpuWorkloadSpec struct {
	// Replicas is the desired number of pods.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	Replicas int32 `json:"replicas"`

	// Image is the container image to run.
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// GpuCount is a simulated GPU request. It is surfaced on the pods as a label
	// and annotation rather than a real device request, so it runs on any cluster.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=8
	// +kubebuilder:default=0
	GpuCount int32 `json:"gpuCount,omitempty"`

	// Priority drives a scheduling annotation on the pods.
	// +kubebuilder:default=medium
	Priority Priority `json:"priority,omitempty"`
}

// GpuWorkloadStatus is the observed state: what the controller reports back.
type GpuWorkloadStatus struct {
	// Phase is the current lifecycle state.
	// +optional
	Phase GpuWorkloadPhase `json:"phase,omitempty"`

	// AvailableReplicas is the number of ready pods observed on the child Deployment.
	// +optional
	AvailableReplicas int32 `json:"availableReplicas,omitempty"`

	// Conditions represent the latest available observations of the object's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// LastReconcileTime is when the controller last reconciled this object.
	// +optional
	LastReconcileTime *metav1.Time `json:"lastReconcileTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=gw
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Desired",type=integer,JSONPath=`.spec.replicas`
// +kubebuilder:printcolumn:name="Available",type=integer,JSONPath=`.status.availableReplicas`
// +kubebuilder:printcolumn:name="GPUs",type=integer,JSONPath=`.spec.gpuCount`
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=`.spec.image`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// GpuWorkload is the Schema for the gpuworkloads API.
type GpuWorkload struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GpuWorkloadSpec   `json:"spec,omitempty"`
	Status GpuWorkloadStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// GpuWorkloadList contains a list of GpuWorkload.
type GpuWorkloadList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GpuWorkload `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GpuWorkload{}, &GpuWorkloadList{})
}
