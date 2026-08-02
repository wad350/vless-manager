package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	operationLoadRetry = 15 * time.Second
	defaultStallLimit  = 90 * time.Second
)

var errOperationCancelled = errors.New("операция отменена")

type operationRequest struct {
	Kind        string
	Title       string
	Source      string
	DedupeKey   string
	Cancellable bool
	StallLimit  time.Duration
}

type operationProgress struct {
	Done    int
	Total   int
	Current string
	Message string
}

type operationView struct {
	ID              string    `json:"id"`
	Kind            string    `json:"kind"`
	Title           string    `json:"title"`
	Source          string    `json:"source"`
	State           string    `json:"state"`
	Done            int       `json:"done,omitempty"`
	Total           int       `json:"total,omitempty"`
	Progress        int       `json:"progress"`
	Current         string    `json:"current,omitempty"`
	Message         string    `json:"message,omitempty"`
	Cancellable     bool      `json:"cancellable"`
	CancelRequested bool      `json:"cancel_requested,omitempty"`
	StartedAt       time.Time `json:"started_at,omitempty"`
	UpdatedAt       time.Time `json:"updated_at"`
	QueuedAt        time.Time `json:"queued_at"`
}

type operationSnapshot struct {
	Active   *operationView  `json:"active,omitempty"`
	Queue    []operationView `json:"queue"`
	HighLoad bool            `json:"high_load"`
	Load1    float64         `json:"load_1"`
}

type queuedOperation struct {
	view operationView
	req  operationRequest
	ctx  context.Context
	fn   func(context.Context, func(operationProgress)) error
	done chan error
}

type operationCoordinator struct {
	mu     sync.Mutex
	pm     *ProcessManager
	queue  []*queuedOperation
	active *queuedOperation
	cancel context.CancelFunc
	wake   chan struct{}
	dedupe map[string]bool
	load   func() (float64, bool)
	retry  time.Duration
}

type operationLease struct {
	coordinator *operationCoordinator
	kind        string
	ctx         context.Context
	release     chan error
	once        sync.Once
}

func newOperationCoordinator(pm *ProcessManager) *operationCoordinator {
	c := &operationCoordinator{
		pm:     pm,
		wake:   make(chan struct{}, 1),
		dedupe: make(map[string]bool),
		load:   operationSystemLoad,
		retry:  operationLoadRetry,
	}
	go c.worker()
	return c
}

// Acquire is convenient for existing handlers with several return paths. The
// returned lease owns the global heavy-operation slot until Finish is called.
func (c *operationCoordinator) Acquire(ctx context.Context, req operationRequest) (*operationLease, error) {
	started := make(chan context.Context, 1)
	claimed := make(chan struct{})
	release := make(chan error, 1)
	completed := make(chan error, 1)
	go func() {
		err := c.Run(ctx, req, func(runCtx context.Context, report func(operationProgress)) error {
			select {
			case started <- runCtx:
			case <-runCtx.Done():
				return runCtx.Err()
			}
			// Do not retain the global slot unless Acquire actually hands the
			// lease to its caller. The request can be cancelled after the worker
			// dequeues it but before the outer select receives started.
			select {
			case <-claimed:
			case <-runCtx.Done():
				return runCtx.Err()
			}
			select {
			case err := <-release:
				return err
			case <-runCtx.Done():
				// Cancellation asks the owner to stop, but the global slot stays
				// occupied until the handler actually unwinds. Otherwise another
				// heavy operation could overlap a blocking network call.
				<-release
				return runCtx.Err()
			}
		})
		completed <- err
	}()
	select {
	case runCtx := <-started:
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		close(claimed)
		return &operationLease{coordinator: c, kind: req.Kind, ctx: runCtx, release: release}, nil
	case err := <-completed:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (l *operationLease) Context() context.Context { return l.ctx }

func (l *operationLease) Progress(progress operationProgress) {
	l.coordinator.Progress(l.kind, progress)
}

func (l *operationLease) Finish(err error) {
	l.once.Do(func() { l.release <- err })
}

func (c *operationCoordinator) Run(
	ctx context.Context,
	req operationRequest,
	fn func(context.Context, func(operationProgress)) error,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if req.Source == "" {
		req.Source = "manual"
	}
	if req.StallLimit <= 0 {
		req.StallLimit = defaultStallLimit
	}
	if req.DedupeKey == "" && req.Source == "background" {
		req.DedupeKey = req.Kind
	}

	now := time.Now()
	op := &queuedOperation{
		req:  req,
		ctx:  ctx,
		fn:   fn,
		done: make(chan error, 1),
		view: operationView{
			ID:          c.pm.nextOperationID("work"),
			Kind:        req.Kind,
			Title:       req.Title,
			Source:      req.Source,
			State:       "queued",
			Cancellable: req.Cancellable,
			QueuedAt:    now,
			UpdatedAt:   now,
			Message:     "Ожидает запуска",
		},
	}

	c.mu.Lock()
	if req.DedupeKey != "" && c.dedupe[req.DedupeKey] {
		c.mu.Unlock()
		return nil
	}
	if req.DedupeKey != "" {
		c.dedupe[req.DedupeKey] = true
	}
	c.queue = append(c.queue, op)
	position := len(c.queue)
	c.mu.Unlock()
	c.pm.event(serviceLogInfo, "operations", "operation.queued",
		"тяжёлая операция поставлена в очередь",
		field("operation_id", op.view.ID), field("kind", req.Kind),
		field("source", req.Source), field("queue_position", position))
	c.signal()

	select {
	case err := <-op.done:
		return err
	case <-ctx.Done():
		if c.removeQueued(op.view.ID) {
			return ctx.Err()
		}
		return <-op.done
	}
}

func (c *operationCoordinator) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *operationCoordinator) worker() {
	for {
		op := c.next()
		if op == nil {
			<-c.wake
			continue
		}
		if op.req.Source == "background" {
			load, high := c.load()
			if high {
				c.deferBackground(op, load)
				timer := time.NewTimer(c.retry)
				select {
				case <-timer.C:
				case <-c.wake:
					if !timer.Stop() {
						<-timer.C
					}
				}
				continue
			}
		}
		c.execute(op)
	}
}

func (c *operationCoordinator) next() *queuedOperation {
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(c.queue) > 0 {
		// User actions must remain responsive while background maintenance is
		// repeatedly deferred by high system load.
		index := 0
		for i, candidate := range c.queue {
			if candidate.req.Source != "background" {
				index = i
				break
			}
		}
		op := c.queue[index]
		c.queue = append(c.queue[:index], c.queue[index+1:]...)
		if err := op.ctx.Err(); err != nil {
			c.finishQueuedLocked(op)
			op.done <- err
			continue
		}
		return op
	}
	return nil
}

func (c *operationCoordinator) deferBackground(op *queuedOperation, load float64) {
	c.mu.Lock()
	op.view.State = "deferred"
	op.view.Message = fmt.Sprintf("Высокая нагрузка (LA %.2f), запуск отложен", load)
	op.view.UpdatedAt = time.Now()
	c.queue = append(c.queue, op)
	c.mu.Unlock()
	c.pm.event(serviceLogInfo, "operations", "operation.deferred",
		"фоновая операция отложена из-за высокой нагрузки",
		field("operation_id", op.view.ID), field("kind", op.req.Kind), field("load_1", load))
}

func (c *operationCoordinator) execute(op *queuedOperation) {
	runCtx, cancel := context.WithCancel(op.ctx)
	now := time.Now()
	c.mu.Lock()
	c.active = op
	c.cancel = cancel
	op.view.State = "running"
	op.view.Message = "Выполняется"
	op.view.StartedAt = now
	op.view.UpdatedAt = now
	c.mu.Unlock()

	c.pm.event(serviceLogInfo, "operations", "operation.started",
		"тяжёлая операция начата",
		field("operation_id", op.view.ID), field("kind", op.req.Kind), field("source", op.req.Source))

	report := func(progress operationProgress) {
		c.mu.Lock()
		if c.active == op {
			op.view.Done = progress.Done
			op.view.Total = progress.Total
			op.view.Current = progress.Current
			op.view.Message = progress.Message
			op.view.Progress = operationPercent(progress.Done, progress.Total)
			op.view.UpdatedAt = time.Now()
		}
		c.mu.Unlock()
	}

	watchDone := make(chan struct{})
	go c.watchStall(op, cancel, watchDone)
	err := op.fn(runCtx, report)
	close(watchDone)
	cancel()

	c.mu.Lock()
	stalled := op.view.State == "stalled"
	c.active = nil
	c.cancel = nil
	c.finishQueuedLocked(op)
	c.mu.Unlock()

	if stalled {
		err = fmt.Errorf("операция зависла: нет прогресса более %s", op.req.StallLimit.Round(time.Second))
	} else if errors.Is(err, context.Canceled) {
		err = errOperationCancelled
	}
	level := serviceLogInfo
	event := "operation.completed"
	message := "тяжёлая операция завершена"
	if err != nil {
		level = serviceLogWarn
		event = "operation.failed"
		message = "тяжёлая операция завершилась ошибкой"
	}
	c.pm.event(level, "operations", event, message,
		field("operation_id", op.view.ID), field("kind", op.req.Kind),
		field("duration_ms", time.Since(op.view.StartedAt).Milliseconds()), field("error", err))
	op.done <- err
	c.signal()
}

func (c *operationCoordinator) watchStall(op *queuedOperation, cancel context.CancelFunc, done <-chan struct{}) {
	interval := op.req.StallLimit / 4
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			c.mu.Lock()
			if c.active == op && time.Since(op.view.UpdatedAt) > op.req.StallLimit {
				op.view.State = "stalled"
				op.view.Message = "Нет прогресса, операция остановлена"
				op.view.UpdatedAt = time.Now()
				c.mu.Unlock()
				cancel()
				return
			}
			c.mu.Unlock()
		}
	}
}

func (c *operationCoordinator) Progress(kind string, progress operationProgress) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil || c.active.req.Kind != kind {
		return
	}
	c.active.view.Done = progress.Done
	c.active.view.Total = progress.Total
	c.active.view.Current = progress.Current
	c.active.view.Message = progress.Message
	c.active.view.Progress = operationPercent(progress.Done, progress.Total)
	c.active.view.UpdatedAt = time.Now()
}

func (c *operationCoordinator) CancelActive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil || !c.active.req.Cancellable || c.cancel == nil {
		return false
	}
	c.active.view.CancelRequested = true
	c.active.view.State = "cancelling"
	c.active.view.Message = "Останавливается"
	c.active.view.UpdatedAt = time.Now()
	c.cancel()
	return true
}

func (c *operationCoordinator) Snapshot() operationSnapshot {
	load, high := c.load()
	c.mu.Lock()
	defer c.mu.Unlock()
	result := operationSnapshot{HighLoad: high, Load1: load, Queue: make([]operationView, len(c.queue))}
	if c.active != nil {
		active := c.active.view
		result.Active = &active
	}
	for i, op := range c.queue {
		result.Queue[i] = op.view
	}
	return result
}

func (c *operationCoordinator) removeQueued(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, op := range c.queue {
		if op.view.ID != id {
			continue
		}
		c.queue = append(c.queue[:i], c.queue[i+1:]...)
		c.finishQueuedLocked(op)
		return true
	}
	return false
}

func (c *operationCoordinator) finishQueuedLocked(op *queuedOperation) {
	if op.req.DedupeKey != "" {
		delete(c.dedupe, op.req.DedupeKey)
	}
}

func operationPercent(done, total int) int {
	if total <= 0 {
		return 0
	}
	percent := done * 100 / total
	if percent > 100 {
		return 100
	}
	if percent < 0 {
		return 0
	}
	return percent
}

func operationSystemLoad() (float64, bool) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, false
	}
	load, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, false
	}
	// Keep one hardware thread available for ndm and packet forwarding.
	return load, load >= float64(runtimeCPULimit)
}
