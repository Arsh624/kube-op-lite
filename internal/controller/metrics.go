package controller

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// gpusAllocated is the number of GPUs held by running pods of each workload.
	gpusAllocated = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kube_op_lite_gpus_allocated",
		Help: "GPUs held by available pods of a GpuWorkload.",
	}, []string{"namespace", "name", "priority"})

	// timeToRunning measures how long a workload took from creation to Running.
	timeToRunning = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "kube_op_lite_time_to_running_seconds",
		Help:    "Seconds from GpuWorkload creation until it first reaches Running.",
		Buckets: prometheus.ExponentialBuckets(1, 2, 10),
	})

	// unschedulableTotal counts transitions into the GpuUnschedulable condition.
	unschedulableTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kube_op_lite_unschedulable_total",
		Help: "Times a GpuWorkload transitioned to GpuUnschedulable.",
	}, []string{"priority"})
)

func init() {
	metrics.Registry.MustRegister(gpusAllocated, timeToRunning, unschedulableTotal)
}
