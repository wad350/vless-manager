package observatory

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

func TestErrorCollectorSnapshotsRemainStable(t *testing.T) {
	collector := newErrorCollector()
	if report := collector.UnderlyingError(); report == nil || !strings.Contains(report.Error(), "failed to produce report") {
		t.Fatalf("empty report=%v", report)
	}
	collector.SubmitError(errors.New("first failure"))
	first := collector.UnderlyingError()
	before := first.Error()
	collector.SubmitError(errors.New("second failure"))
	if first.Error() != before {
		t.Fatal("a previously published error report changed")
	}
	if !strings.Contains(collector.UnderlyingError().Error(), "second failure") {
		t.Fatal("latest underlying error was lost")
	}
}

func TestErrorCollectorConcurrentReports(t *testing.T) {
	collector := newErrorCollector()
	collector.SubmitError(errors.New("initial failure"))
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				report := collector.UnderlyingError()
				before := report.Error()
				collector.SubmitError(errors.New("concurrent failure"))
				if report.Error() != before {
					t.Error("published error mutated during concurrent submit")
					return
				}
			}
		}()
	}
	wg.Wait()
}
