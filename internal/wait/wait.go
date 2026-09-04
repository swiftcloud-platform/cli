// Package wait polls a resource until its status is terminal.
//
// Statuses on this platform are advanced only by the worker. When the worker
// is down nothing fails: statuses simply stop moving, and a naive poll would
// sit at "deploying" until its timeout and report a slow deploy. So a wait
// that sees no progress asks the platform's /health for the worker heartbeat
// and, when that is stale, says so — the most useful sentence the CLI can
// print in that situation.
package wait

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Poll fetches the current status. done=true ends the wait successfully.
type Poll func(ctx context.Context) (status string, done bool, err error)

// Health reports whether the worker is alive and how old its heartbeat is.
type Health func(ctx context.Context) (workerOK bool, age time.Duration, err error)

// Options for Until.
type Options struct {
	Interval time.Duration
	Timeout  time.Duration
	// After this long without a status change, Health is consulted.
	StallAfter time.Duration
	// OnStatus is called whenever the status changes, for progress output.
	OnStatus func(status string)
	Now      func() time.Time
	Sleep    func(context.Context, time.Duration) error
}

// ErrTimeout is returned when Timeout passes without a terminal status.
type ErrTimeout struct {
	LastStatus string
	Elapsed    time.Duration
	// WorkerDown is set when /health reported a stale or missing heartbeat.
	WorkerDown bool
	WorkerAge  time.Duration
}

func (e *ErrTimeout) Error() string {
	if e.WorkerDown {
		if e.WorkerAge > 0 {
			return fmt.Sprintf("still %q after %s — the platform worker has not reported for %s, so statuses are not being updated; the resource may be fine. Contact support if this persists.", e.LastStatus, e.Elapsed.Round(time.Second), e.WorkerAge.Round(time.Second))
		}
		return fmt.Sprintf("still %q after %s — the platform worker is not running, so statuses are not being updated", e.LastStatus, e.Elapsed.Round(time.Second))
	}
	return fmt.Sprintf("still %q after %s; check `cloud app get` later or increase --timeout", e.LastStatus, e.Elapsed.Round(time.Second))
}

// ErrFailed is returned when the status becomes a failure state.
type ErrFailed struct{ Status, Reason string }

func (e *ErrFailed) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("%s: %s", e.Status, e.Reason)
	}
	return e.Status
}

func defaultSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Until polls until done, a failure, a timeout, or ctx is cancelled.
func Until(ctx context.Context, poll Poll, health Health, o Options) (string, error) {
	if o.Interval <= 0 {
		o.Interval = 3 * time.Second
	}
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Minute
	}
	if o.StallAfter <= 0 {
		o.StallAfter = 90 * time.Second
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	sleep := o.Sleep
	if sleep == nil {
		sleep = defaultSleep
	}

	start := now()
	last := ""
	lastChange := start
	healthChecked := false
	workerDown := false
	var workerAge time.Duration

	for {
		status, done, err := poll(ctx)
		if err != nil {
			return last, err
		}
		if status != last {
			last = status
			lastChange = now()
			if o.OnStatus != nil {
				o.OnStatus(status)
			}
		}
		if done {
			return status, nil
		}
		elapsed := now().Sub(start)
		if elapsed >= o.Timeout {
			return status, &ErrTimeout{LastStatus: status, Elapsed: elapsed, WorkerDown: workerDown, WorkerAge: workerAge}
		}
		// Stalled: ask once whether anyone is actually working on it.
		if !healthChecked && health != nil && now().Sub(lastChange) >= o.StallAfter {
			healthChecked = true
			if ok, age, herr := health(ctx); herr == nil && !ok {
				workerDown = true
				workerAge = age
				// No point waiting the full timeout for a worker that is not there.
				return status, &ErrTimeout{LastStatus: status, Elapsed: elapsed, WorkerDown: true, WorkerAge: age}
			}
		}
		if err := sleep(ctx, o.Interval); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return status, err
			}
			return status, err
		}
	}
}
