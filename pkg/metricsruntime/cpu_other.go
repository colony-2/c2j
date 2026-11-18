//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package metricsruntime

func readProcessCPU() (float64, error) { return 0, ErrCPUUnsupported }
