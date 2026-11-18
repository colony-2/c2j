package metricsruntime

import (
	"errors"
	"math"
	"sync"
	"time"

	"github.com/colony-2/c2j/pkg/metrics"
)

var ErrCPUUnsupported = errors.New("process CPU readings unsupported")

// Sampler serializes membership changes and cumulative process readings. It keeps
// only accumulators per active window, never a list of samples.
type Sampler struct {
	mu       sync.Mutex
	now      func() time.Time
	read     func() (float64, error)
	previous time.Time
	cpu      float64
	valid    bool
	active   map[*metrics.CPUCoverage]bool
	stop     chan struct{}
	periodic bool
}

func NewSampler(now func() time.Time, read func() (float64, error)) *Sampler {
	return &Sampler{now: now, read: read, active: map[*metrics.CPUCoverage]bool{}}
}

var processSampler = func() *Sampler { s := NewSampler(time.Now, readProcessCPU); s.periodic = true; return s }()

func (s *Sampler) Start() func() metrics.CPUCoverage {
	s.mu.Lock()
	s.sample()
	w := &metrics.CPUCoverage{Availability: "unavailable", ExcludedSeconds: map[string]float64{}}
	if s.valid {
		w.Availability = "available"
	}
	s.active[w] = true
	if s.periodic && s.stop == nil {
		stop := make(chan struct{})
		s.stop = stop
		go func() {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					s.Tick()
				}
			}
		}()
	}
	s.mu.Unlock()
	var once sync.Once
	return func() metrics.CPUCoverage {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.sample()
			delete(s.active, w)
			if len(s.active) == 0 && s.stop != nil {
				close(s.stop)
				s.stop = nil
				s.valid = false
			}
		})
		s.mu.Lock()
		defer s.mu.Unlock()
		copy := *w
		copy.ExcludedSeconds = map[string]float64{}
		for k, n := range w.ExcludedSeconds {
			copy.ExcludedSeconds[k] = n
		}
		return copy
	}
}
func (s *Sampler) Tick() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.active) > 0 {
		s.sample()
	}
}
func (s *Sampler) sample() {
	now := s.now()
	cpu, err := s.read()
	if err == nil && (cpu < 0 || math.IsNaN(cpu) || math.IsInf(cpu, 0)) {
		err = errors.New("invalid process CPU reading")
	}
	dt := now.Sub(s.previous).Seconds()
	reason := ""
	switch {
	case errors.Is(err, ErrCPUUnsupported):
		reason = "unsupported"
	case err != nil || !s.valid:
		reason = "read_error"
	case dt <= 0:
		reason = "read_error"
	case cpu < s.cpu:
		reason = "reset"
	case len(s.active) > 1:
		reason = "concurrent_tasks"
	}
	for w := range s.active {
		if reason == "" {
			w.MeasuredSeconds += dt
			w.ProcessSeconds += cpu - s.cpu
			w.Availability = "available"
		} else {
			if dt > 0 {
				w.ExcludedSeconds[reason] += dt
			}
			if reason == "unsupported" {
				w.Availability = "unsupported"
			}
		}
	}
	s.previous = now
	s.cpu = cpu
	s.valid = err == nil
}
