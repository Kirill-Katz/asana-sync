package internal

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Job describes a recurring operation. PollFunc must respect its context.
type Job struct {
	Name     string
	Interval time.Duration
	PollFunc func(context.Context) error
}

func (j Job) Poll(ctx context.Context) error {
	return j.PollFunc(ctx)
}

// Poller runs each job in its own goroutine, without overlapping calls to that job.
// Separate jobs may run concurrently and share the same API client limiter.
type Poller struct {
	jobs        []Job
	reportError func(string, error)
	running     atomic.Bool
}

// NewPoller snapshots the jobs and validates them before execution.
// reportError must be concurrency-safe and return promptly; job errors are
// reported through it and do not stop polling.
func NewPoller(jobs []Job, reportError func(name string, err error)) (*Poller, error) {
	if len(jobs) == 0 {
		return nil, errors.New("poller must have at least one job")
	}
	if reportError == nil {
		return nil, errors.New("poller error reporter must not be nil")
	}
	seen := make(map[string]bool, len(jobs))
	for _, job := range jobs {
		if strings.TrimSpace(job.Name) == "" {
			return nil, errors.New("job name must not be empty")
		}
		if seen[job.Name] {
			return nil, fmt.Errorf("duplicate job name %q", job.Name)
		}
		if job.Interval <= 0 {
			return nil, fmt.Errorf("job %q interval must be positive", job.Name)
		}
		if job.PollFunc == nil {
			return nil, fmt.Errorf("job %q PollFunc must not be nil", job.Name)
		}
		seen[job.Name] = true
	}
	return &Poller{jobs: slices.Clone(jobs), reportError: reportError}, nil
}

// Run blocks until cancellation and waits for in-flight Poll calls to return.
// Jobs run immediately, then on each interval tick. Only one Run may be
// active on this poller at a time.
func (p *Poller) Run(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !p.running.CompareAndSwap(false, true) {
		return errors.New("poller is already running")
	}
	defer p.running.Store(false)

	var workers sync.WaitGroup
	for _, job := range p.jobs {
		workers.Go(func() { p.runJob(ctx, job) })
	}
	workers.Wait()
	return ctx.Err()
}

func (p *Poller) runJob(ctx context.Context, job Job) {
	ticker := time.NewTicker(job.Interval)
	defer ticker.Stop()

	for {
		if ctx.Err() != nil {
			return
		}
		// Poll runs immediately and blocks this worker, so another tick
		// cannot start the job while its previous call is still running.
		if err := job.Poll(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			p.reportError(job.Name, err)
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
