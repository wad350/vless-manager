package observatory

import (
	"sync"

	"github.com/xtls/xray-core/common/errors"
)

type errorCollector struct {
	mu     sync.Mutex
	errors *errors.Error
}

func (e *errorCollector) SubmitError(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.errors == nil {
		e.errors = errors.New("underlying connection error").Base(err)
		return
	}
	// A report may already be in use by a logger; never mutate that snapshot.
	report := *e.errors
	e.errors = report.Base(errors.New("underlying connection error").Base(err))
}

func newErrorCollector() *errorCollector {
	return &errorCollector{}
}

func (e *errorCollector) UnderlyingError() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.errors == nil {
		return errors.New("failed to produce report")
	}
	return e.errors
}
