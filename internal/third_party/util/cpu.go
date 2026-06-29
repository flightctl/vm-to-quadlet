// Minimal extract from github.com/containers/podman pkg/util/utils.go.
// Only the CPU period/quota helpers needed by pkg/kube/quadlet/resources.go are included;
// the rest of utils.go was omitted because it pulls in heavy libpod/storage dependencies.

package util

// DefaultCPUPeriod is the default CPU period (100ms) in microseconds, which is
// the same default as Kubernetes.
const DefaultCPUPeriod uint64 = 100000

// CoresToPeriodAndQuota converts a fraction of cores to the equivalent
// Completely Fair Scheduler (CFS) parameters period and quota.
//
// Cores is a fraction of the CFS period that a container may use. Period and
// Quota are in microseconds.
func CoresToPeriodAndQuota(cores float64) (uint64, int64) {
	return DefaultCPUPeriod, int64(cores * float64(DefaultCPUPeriod))
}

// PeriodAndQuotaToCores takes the CFS parameters period and quota and returns
// a fraction that represents the limit to the number of cores that can be
// utilized over the scheduling period.
//
// Cores is a fraction of the CFS period that a container may use. Period and
// Quota are in microseconds.
func PeriodAndQuotaToCores(period uint64, quota int64) float64 {
	return float64(quota) / float64(period)
}
