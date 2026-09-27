package fuzzguard

import (
	"runtime/metrics"
	"strings"
	"testing"
	"time"
)

func TestGuardCheck(t *testing.T) {
	now := time.Unix(1000, 0)
	g := &Guard{heapBudget: 100, timeBudget: 5 * time.Second}
	tests := []struct {
		name     string
		heapUsed uint64
		started  int64
		want     string
	}{
		{name: "idle within heap", heapUsed: 100, started: 0, want: ""},
		{name: "idle over heap", heapUsed: 101, started: 0, want: "heap budget"},
		{name: "running within time", heapUsed: 10, started: now.Add(-5 * time.Second).UnixNano(), want: ""},
		{name: "running over time", heapUsed: 10, started: now.Add(-6 * time.Second).UnixNano(), want: "time budget"},
		{name: "heap wins over time", heapUsed: 101, started: now.Add(-6 * time.Second).UnixNano(), want: "heap budget"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := g.check(tc.heapUsed, tc.started, now)
			if tc.want == "" {
				if got != "" {
					t.Errorf("check() = %q, want no violation", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("check() = %q, want it to mention %q", got, tc.want)
			}
		})
	}
}

func TestGuardBeginEnd(t *testing.T) {
	var g Guard
	g.Begin()
	if g.started.Load() == 0 {
		t.Fatal("Begin did not record a start time")
	}
	g.End()
	if got := g.started.Load(); got != 0 {
		t.Fatalf("End left start time %d, want 0", got)
	}
}

func TestHeapObjectBytesReadsLiveHeap(t *testing.T) {
	sample := []metrics.Sample{{Name: heapMetric}}
	if got := heapObjectBytes(sample); got == 0 {
		t.Fatalf("heapObjectBytes() = 0, want the live heap size of this test process")
	}
}

func TestGuardWatchStopsWithinBudget(t *testing.T) {
	g := &Guard{heapBudget: 1 << 40, timeBudget: time.Hour}
	g.Begin()
	stop := make(chan struct{})
	done := make(chan struct{})
	go g.watch(stop, done)
	time.Sleep(30 * time.Millisecond)
	close(stop)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watch did not return after stop was closed")
	}
}
