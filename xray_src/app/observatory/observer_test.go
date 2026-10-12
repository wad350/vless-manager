package observatory

import (
	"context"
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"
)

func TestObservationSnapshotSurvivesUpdates(t *testing.T) {
	o := &Observer{}
	o.updateStatusForResult("one", &ProbeResult{Alive: true, Delay: 42})
	report, err := o.GetObservation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	before := proto.Clone(report)
	o.updateStatusForResult("one", &ProbeResult{Alive: false, LastErrorReason: "offline"})
	o.updateStatusForResult("two", &ProbeResult{Alive: true, Delay: 11})
	o.clearRemovedOutbounds([]string{"two"})
	if !proto.Equal(report, before) {
		t.Fatal("a published observation changed after a probe update")
	}
}

func TestObservationConcurrentSnapshots(t *testing.T) {
	o := &Observer{}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			o.updateStatusForResult("one", &ProbeResult{Alive: true, Delay: int64(i)})
			o.clearRemovedOutbounds([]string{"one"})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			report, _ := o.GetObservation(context.Background())
			if _, err := proto.Marshal(report); err != nil {
				t.Error(err)
			}
		}
	}()
	wg.Wait()
}

func TestObserverUpdateStatusPrunesStaleOutbounds(t *testing.T) {
	observer := &Observer{
		status: []*OutboundStatus{
			{
				OutboundTag:     "keep",
				Alive:           true,
				Delay:           42,
				LastErrorReason: "",
				LastSeenTime:    111,
				LastTryTime:     222,
			},
			{
				OutboundTag:     "drop",
				Alive:           false,
				Delay:           99999999,
				LastErrorReason: "probe failed",
				LastSeenTime:    333,
				LastTryTime:     444,
			},
		},
	}

	observer.clearRemovedOutbounds([]string{"keep"})

	if len(observer.status) != 1 {
		t.Fatalf("expected 1 status after pruning, got %d", len(observer.status))
	}

	got := observer.status[0]
	if got.OutboundTag != "keep" {
		t.Fatalf("expected remaining status for keep, got %q", got.OutboundTag)
	}
	if !got.Alive {
		t.Fatal("expected remaining status to preserve Alive field")
	}
	if got.Delay != 42 {
		t.Fatalf("expected remaining status to preserve Delay, got %d", got.Delay)
	}
	if got.LastSeenTime != 111 {
		t.Fatalf("expected remaining status to preserve LastSeenTime, got %d", got.LastSeenTime)
	}
	if got.LastTryTime != 222 {
		t.Fatalf("expected remaining status to preserve LastTryTime, got %d", got.LastTryTime)
	}
}

func TestObserverUpdateStatusClearsWhenNoOutboundsRemain(t *testing.T) {
	observer := &Observer{
		status: []*OutboundStatus{
			{OutboundTag: "drop-1"},
			{OutboundTag: "drop-2"},
		},
	}

	observer.clearRemovedOutbounds(nil)

	if len(observer.status) != 0 {
		t.Fatalf("expected all statuses to be removed, got %d", len(observer.status))
	}
}
