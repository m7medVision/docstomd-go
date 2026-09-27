// Package fuzzguard bounds the heap and per-input run time of a fuzz process
// so a hostile input fails as a crasher instead of exhausting the host.
package fuzzguard

import (
	"flag"
	"fmt"
	"runtime/debug"
	"runtime/metrics"
	"sync/atomic"
	"testing"
	"time"
)

const heapMetric = "/memory/classes/heap/objects:bytes"

// Guard watches one fuzz process. Begin and End bracket each input so the
// watchdog can tell how long the current input has been running.
type Guard struct {
	heapBudget uint64
	timeBudget time.Duration
	started    atomic.Int64
}

// Start sets a soft GC limit of heapBudget bytes and starts a watchdog that
// panics once live heap objects exceed heapBudget or a single input runs
// longer than timeBudget. The panic ends the fuzz worker, which the fuzzing
// engine records as a crasher for the current input.
func Start(f *testing.F, heapBudget uint64, timeBudget time.Duration) *Guard {
	f.Helper()
	g := &Guard{heapBudget: heapBudget, timeBudget: timeBudget}
	if isFuzzCoordinator() {
		// The coordinator holds the corpus and runs no inputs; its heap is not
		// an input's heap, so watching it only produces false crashers.
		return g
	}
	previous := debug.SetMemoryLimit(int64(heapBudget))
	stop := make(chan struct{})
	done := make(chan struct{})
	go g.watch(stop, done)
	f.Cleanup(func() {
		close(stop)
		<-done
		debug.SetMemoryLimit(previous)
	})
	return g
}

// isFuzzCoordinator reports whether this process is the fuzzing coordinator:
// -test.fuzz is set but -test.fuzzworker is not. Workers and plain `go test`
// seed runs both execute inputs and are watched.
func isFuzzCoordinator() bool {
	fuzz := flag.Lookup("test.fuzz")
	worker := flag.Lookup("test.fuzzworker")
	if fuzz == nil || worker == nil {
		return false
	}
	return fuzz.Value.String() != "" && worker.Value.String() != "true"
}

// Begin marks the start of one input.
func (g *Guard) Begin() { g.started.Store(time.Now().UnixNano()) }

// End marks the end of the current input.
func (g *Guard) End() { g.started.Store(0) }

func (g *Guard) watch(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	sample := []metrics.Sample{{Name: heapMetric}}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-ticker.C:
			if violation := g.check(heapObjectBytes(sample), g.started.Load(), now); violation != "" {
				panic(violation)
			}
		}
	}
}

func heapObjectBytes(sample []metrics.Sample) uint64 {
	metrics.Read(sample)
	if sample[0].Value.Kind() != metrics.KindUint64 {
		return 0
	}
	return sample[0].Value.Uint64()
}

// check returns a description of the exceeded budget, or "" when the process
// is within both budgets. started is the current input's start in Unix
// nanoseconds, or 0 when no input is running.
func (g *Guard) check(heapUsed uint64, started int64, now time.Time) string {
	if heapUsed > g.heapBudget {
		return fmt.Sprintf("fuzz input exceeded heap budget: %d bytes live, budget %d", heapUsed, g.heapBudget)
	}
	if started == 0 {
		return ""
	}
	if elapsed := now.Sub(time.Unix(0, started)); elapsed > g.timeBudget {
		return fmt.Sprintf("fuzz input exceeded time budget: running %v, budget %v", elapsed, g.timeBudget)
	}
	return ""
}
