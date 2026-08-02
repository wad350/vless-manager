package main

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testCoordinator(t *testing.T) *operationCoordinator {
	t.Helper()
	c := newOperationCoordinator(NewProcessManager(t.TempDir()))
	c.load = func() (float64, bool) { return 0, false }
	c.retry = 5 * time.Millisecond
	return c
}

func TestOperationCoordinatorSerializesWork(t *testing.T) {
	c := testCoordinator(t)
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondStarted := make(chan struct{})
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)

	go func() {
		firstDone <- c.Run(context.Background(), operationRequest{Kind: "ping", Title: "Ping"},
			func(context.Context, func(operationProgress)) error {
				close(firstStarted)
				<-releaseFirst
				return nil
			})
	}()
	<-firstStarted
	go func() {
		secondDone <- c.Run(context.Background(), operationRequest{Kind: "bypass", Title: "Bypass"},
			func(context.Context, func(operationProgress)) error {
				close(secondStarted)
				return nil
			})
	}()

	select {
	case <-secondStarted:
		t.Fatal("second operation started while first was running")
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("queued operation did not start")
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
}

func TestOperationCoordinatorProgressAndCancel(t *testing.T) {
	c := testCoordinator(t)
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- c.Run(context.Background(), operationRequest{
			Kind: "ping", Title: "Ping", Cancellable: true,
		}, func(ctx context.Context, report func(operationProgress)) error {
			report(operationProgress{Done: 2, Total: 5, Current: "node-2", Message: "checking"})
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	<-started
	snapshot := c.Snapshot()
	if snapshot.Active == nil || snapshot.Active.Progress != 40 || snapshot.Active.Current != "node-2" {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	if !c.CancelActive() {
		t.Fatal("active operation was not cancelled")
	}
	if err := <-done; !errors.Is(err, errOperationCancelled) {
		t.Fatalf("cancel error=%v", err)
	}
}

func TestOperationCoordinatorDeduplicatesBackgroundWork(t *testing.T) {
	c := testCoordinator(t)
	started := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	var calls atomic.Int32
	request := operationRequest{Kind: "subscriptions", Source: "background", DedupeKey: "refresh"}
	go func() {
		firstDone <- c.Run(context.Background(), request, func(context.Context, func(operationProgress)) error {
			calls.Add(1)
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	if err := c.Run(context.Background(), request, func(context.Context, func(operationProgress)) error {
		calls.Add(1)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("background operation ran %d times", calls.Load())
	}
}

func TestOperationCoordinatorCancelsStalledWork(t *testing.T) {
	c := testCoordinator(t)
	err := c.Run(context.Background(), operationRequest{
		Kind: "ping", Title: "Ping", Cancellable: true, StallLimit: 20 * time.Millisecond,
	}, func(ctx context.Context, report func(operationProgress)) error {
		report(operationProgress{Total: 10, Message: "started"})
		<-ctx.Done()
		return ctx.Err()
	})
	if err == nil || !strings.Contains(err.Error(), "нет прогресса") {
		t.Fatalf("stall error=%v", err)
	}
	if snapshot := c.Snapshot(); snapshot.Active != nil {
		t.Fatalf("stalled operation remained active: %+v", snapshot.Active)
	}
}

func TestOperationCoordinatorManualWorkBypassesDeferredBackground(t *testing.T) {
	c := testCoordinator(t)
	var high atomic.Bool
	high.Store(true)
	c.load = func() (float64, bool) { return 8, high.Load() }
	backgroundDone := make(chan error, 1)
	backgroundStarted := make(chan struct{})
	go func() {
		backgroundDone <- c.Run(context.Background(), operationRequest{
			Kind: "subscriptions", Source: "background", DedupeKey: "refresh",
		}, func(context.Context, func(operationProgress)) error {
			close(backgroundStarted)
			return nil
		})
	}()
	time.Sleep(15 * time.Millisecond)
	manualDone := make(chan error, 1)
	go func() {
		manualDone <- c.Run(context.Background(), operationRequest{Kind: "ping", Source: "manual"},
			func(context.Context, func(operationProgress)) error { return nil })
	}()
	select {
	case err := <-manualDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("manual operation was blocked by deferred background work")
	}
	high.Store(false)
	c.signal()
	select {
	case <-backgroundStarted:
	case <-time.After(time.Second):
		t.Fatal("background operation did not resume")
	}
	if err := <-backgroundDone; err != nil {
		t.Fatal(err)
	}
}
