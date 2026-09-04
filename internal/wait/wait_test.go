package wait

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A fake clock: Sleep advances Now instead of waiting.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }
func (c *clock) sleep(_ context.Context, d time.Duration) error {
	c.t = c.t.Add(d)
	return nil
}

func opts(c *clock, o Options) Options {
	o.Now = c.now
	o.Sleep = c.sleep
	if o.Interval == 0 {
		o.Interval = time.Second
	}
	return o
}

func sequence(statuses ...string) Poll {
	i := 0
	return func(context.Context) (string, bool, error) {
		s := statuses[min(i, len(statuses)-1)]
		i++
		return s, s == "running", nil
	}
}

func TestUntil_ReturnsWhenDone(t *testing.T) {
	c := &clock{t: time.Unix(0, 0)}
	var seen []string
	status, err := Until(context.Background(), sequence("deploying", "deploying", "running"), nil, opts(c, Options{OnStatus: func(s string) { seen = append(seen, s) }}))
	if err != nil || status != "running" {
		t.Fatalf("got %q %v", status, err)
	}
	if len(seen) != 2 || seen[0] != "deploying" || seen[1] != "running" {
		t.Errorf("OnStatus must fire on change only: %v", seen)
	}
}

func TestUntil_TimeoutWithHealthyWorker(t *testing.T) {
	c := &clock{t: time.Unix(0, 0)}
	health := func(context.Context) (bool, time.Duration, error) { return true, 3 * time.Second, nil }
	_, err := Until(context.Background(), sequence("deploying"), health, opts(c, Options{Timeout: 10 * time.Second, StallAfter: 5 * time.Second}))
	var te *ErrTimeout
	if !errors.As(err, &te) {
		t.Fatalf("want ErrTimeout, got %v", err)
	}
	if te.WorkerDown {
		t.Error("worker was healthy; must not blame it")
	}
	if te.LastStatus != "deploying" {
		t.Errorf("last status = %q", te.LastStatus)
	}
}

// The case the package exists for.
func TestUntil_StalledWithDeadWorker_ReportsWorkerNotTimeout(t *testing.T) {
	c := &clock{t: time.Unix(0, 0)}
	health := func(context.Context) (bool, time.Duration, error) { return false, 20 * time.Minute, nil }
	_, err := Until(context.Background(), sequence("deploying"), health, opts(c, Options{Timeout: time.Hour, StallAfter: 5 * time.Second}))
	var te *ErrTimeout
	if !errors.As(err, &te) {
		t.Fatalf("want ErrTimeout, got %v", err)
	}
	if !te.WorkerDown {
		t.Fatal("must report the worker as down")
	}
	if te.Elapsed >= time.Hour {
		t.Error("must give up early once the worker is known to be down, not wait the full timeout")
	}
	msg := err.Error()
	if !contains(msg, "worker") || !contains(msg, "20m") {
		t.Errorf("message must name the worker and its heartbeat age: %q", msg)
	}
}

func TestUntil_HealthNotConsultedWhileProgressing(t *testing.T) {
	c := &clock{t: time.Unix(0, 0)}
	calls := 0
	health := func(context.Context) (bool, time.Duration, error) { calls++; return false, time.Hour, nil }
	// Status changes every poll, so it never stalls.
	i := 0
	poll := func(context.Context) (string, bool, error) {
		i++
		if i >= 6 {
			return "running", true, nil
		}
		return "step-" + string(rune('a'+i)), false, nil
	}
	if _, err := Until(context.Background(), poll, health, opts(c, Options{StallAfter: 3 * time.Second})); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Errorf("health must only be checked when stalled, was called %d times", calls)
	}
}

func TestUntil_PollErrorPropagates(t *testing.T) {
	c := &clock{t: time.Unix(0, 0)}
	boom := errors.New("boom")
	_, err := Until(context.Background(), func(context.Context) (string, bool, error) { return "", false, boom }, nil, opts(c, Options{}))
	if !errors.Is(err, boom) {
		t.Errorf("want poll error, got %v", err)
	}
}

func TestUntil_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c := &clock{t: time.Unix(0, 0)}
	o := opts(c, Options{})
	o.Sleep = func(context.Context, time.Duration) error { cancel(); return context.Canceled }
	_, err := Until(ctx, sequence("deploying"), nil, o)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("want Canceled, got %v", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
