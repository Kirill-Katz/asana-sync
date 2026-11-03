package internal

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestNewPollerValidation(t *testing.T) {
	poll := func(context.Context) error { return nil }
	report := func(string, error) {}
	for _, jobs := range [][]Job{
		nil,
		{{Name: "", Interval: time.Second, PollFunc: poll}},
		{{Name: "job", Interval: 0, PollFunc: poll}},
		{{Name: "job", Interval: -time.Second, PollFunc: poll}},
		{{Name: "job", Interval: time.Second}},
		{{Name: "job", Interval: time.Second, PollFunc: poll}, {Name: "job", Interval: time.Second, PollFunc: poll}},
	} {
		if _, err := NewPoller(jobs, report); err == nil {
			t.Fatal("accepted invalid jobs")
		}
	}
	if _, err := NewPoller([]Job{{Name: "job", Interval: time.Second, PollFunc: poll}}, nil); err == nil {
		t.Fatal("accepted nil error reporter")
	}
}

func TestPollerIntervalsAndSnapshot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var fast, slow atomic.Int32
		jobs := []Job{
			{Name: "fast", Interval: time.Second, PollFunc: func(received context.Context) error {
				if received != ctx {
					t.Error("job did not receive run context")
				}
				fast.Add(1)
				return nil
			}},
			{Name: "slow", Interval: 2 * time.Second, PollFunc: func(context.Context) error {
				slow.Add(1)
				return nil
			}},
		}
		poller, err := NewPoller(jobs, func(name string, err error) { t.Errorf("%s: %v", name, err) })
		if err != nil {
			t.Fatal(err)
		}
		// Changing the caller's slice must not alter the poller's snapshot.
		jobs[0].Interval = time.Hour
		jobs[0].PollFunc = func(context.Context) error { t.Error("modified job ran"); return nil }
		done := make(chan error, 1)
		go func() { done <- poller.Run(ctx) }()
		synctest.Wait()
		if fast.Load() != 1 || slow.Load() != 1 {
			t.Fatal("jobs did not run immediately")
		}
		time.Sleep(time.Second - time.Nanosecond)
		synctest.Wait()
		if fast.Load() != 1 || slow.Load() != 1 {
			t.Fatal("jobs ran again before their interval elapsed")
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if fast.Load() != 2 || slow.Load() != 1 {
			t.Fatalf("first tick: fast=%d slow=%d", fast.Load(), slow.Load())
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if fast.Load() != 3 || slow.Load() != 2 {
			t.Fatalf("second tick: fast=%d slow=%d", fast.Load(), slow.Load())
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want cancellation", err)
		}
	})
}

func TestPollerDoesNotOverlapAndJobsAreIndependent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		release := make(chan struct{})
		var blockedCalls, otherCalls atomic.Int32
		poller, err := NewPoller([]Job{
			{Name: "blocked", Interval: time.Second, PollFunc: func(context.Context) error {
				blockedCalls.Add(1)
				<-release
				return nil
			}},
			{Name: "other", Interval: time.Second, PollFunc: func(context.Context) error {
				otherCalls.Add(1)
				return nil
			}},
		}, func(name string, err error) { t.Errorf("%s: %v", name, err) })
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- poller.Run(ctx) }()
		synctest.Wait()
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if blockedCalls.Load() != 1 || otherCalls.Load() != 6 {
			t.Fatalf("blocked=%d other=%d, want 1 and 6", blockedCalls.Load(), otherCalls.Load())
		}
		cancel()
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("Run returned before the in-flight job finished")
		default:
		}
		close(release)
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want cancellation", err)
		}
		if blockedCalls.Load() != 1 {
			t.Fatal("a buffered tick started a new poll after cancellation")
		}
	})
}

func TestPollerReportsErrorsAndContinues(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		failure := errors.New("poll failed")
		var calls, reports atomic.Int32
		poller, err := NewPoller([]Job{{Name: "retrying", Interval: time.Second, PollFunc: func(context.Context) error {
			calls.Add(1)
			return failure
		}}}, func(name string, err error) {
			if name != "retrying" || !errors.Is(err, failure) {
				t.Errorf("unexpected report: name=%q err=%v", name, err)
			}
			reports.Add(1)
		})
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- poller.Run(ctx) }()
		synctest.Wait()
		time.Sleep(3 * time.Second)
		synctest.Wait()
		if calls.Load() != 4 || reports.Load() != 4 {
			t.Fatalf("calls=%d reports=%d, want 4 of each", calls.Load(), reports.Load())
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want cancellation", err)
		}
	})
}

func TestPollerRespectsContextDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		var calls atomic.Int32
		poller, err := NewPoller([]Job{{Name: "job", Interval: time.Second, PollFunc: func(ctx context.Context) error {
			if ctx.Err() != nil || calls.Add(1) != 1 {
				t.Error("job ran after its context deadline")
			}
			return nil
		}}}, func(name string, err error) { t.Errorf("%s: %v", name, err) })
		if err != nil {
			t.Fatal(err)
		}
		if err := poller.Run(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("got %v, want deadline exceeded", err)
		}
		if calls.Load() != 1 {
			t.Fatal("job did not run immediately before the deadline")
		}
	})
}

func TestPollerCancellationAndConcurrentRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var calls, reports atomic.Int32
		poller, err := NewPoller([]Job{{Name: "job", Interval: time.Second, PollFunc: func(ctx context.Context) error {
			calls.Add(1)
			<-ctx.Done()
			return ctx.Err()
		}}}, func(string, error) { reports.Add(1) })
		if err != nil {
			t.Fatal(err)
		}
		canceled, cancelNow := context.WithCancel(context.Background())
		cancelNow()
		if err := poller.Run(canceled); !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want preexisting cancellation", err)
		}
		done := make(chan error, 1)
		go func() { done <- poller.Run(ctx) }()
		synctest.Wait()
		if err := poller.Run(ctx); err == nil {
			t.Fatal("accepted a concurrent Run")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want cancellation", err)
		}
		if calls.Load() != 1 || reports.Load() != 0 {
			t.Fatalf("calls=%d reports=%d, want 1 call and no cancellation reports", calls.Load(), reports.Load())
		}
	})
}
