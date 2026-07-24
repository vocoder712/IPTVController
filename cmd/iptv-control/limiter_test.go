package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func testLimiterConfig() LimiterConfig {
	return LimiterConfig{
		Enabled:      true,
		PollInterval: 30 * time.Second,
		WatchLimit:   20 * time.Minute,
		Cycle:        time.Minute,
		DownDuration: 6 * time.Second,
		ActionRetry:  time.Second,
	}
}

func TestDefaultLimiterCycleIs54SecondsUpAnd6SecondsDown(t *testing.T) {
	cfg := defaultLimiterConfig()
	if cfg.Cycle != time.Minute || cfg.DownDuration != 6*time.Second ||
		cfg.upDuration() != 54*time.Second {
		t.Fatalf("unexpected default intervention cycle: %+v", cfg)
	}
}

func watchingStatus() PortStatus {
	return PortStatus{AdminUp: true, Enabled: true, Carrier: "1"}
}

func TestLimiterStateTransitions(t *testing.T) {
	cfg := testLimiterConfig()
	m := newLimiterMachine(cfg)
	start := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	var actions []bool
	set := func(_ context.Context, enabled bool) error {
		actions = append(actions, enabled)
		return nil
	}

	m.Step(context.Background(), start, watchingStatus(), set)
	assertLimiterState(t, m, start, LimiterWatching)

	m.Step(context.Background(), start.Add(cfg.WatchLimit-time.Second), watchingStatus(), set)
	assertLimiterState(t, m, start, LimiterWatching)

	limitReached := start.Add(cfg.WatchLimit)
	m.Step(context.Background(), limitReached, watchingStatus(), set)
	snapshot := assertLimiterState(t, m, limitReached, LimiterInterventionUp)
	wantDownAt := limitReached.Add(cfg.upDuration())
	if !snapshot.NextAction.Equal(wantDownAt) {
		t.Fatalf("next action=%v, want %v", snapshot.NextAction, wantDownAt)
	}

	m.Step(context.Background(), wantDownAt, watchingStatus(), set)
	snapshot = assertLimiterState(t, m, wantDownAt, LimiterInterventionDown)
	if len(actions) != 1 || actions[0] {
		t.Fatalf("actions=%v, want [false]", actions)
	}

	downStatus := PortStatus{AdminUp: false, Carrier: "0"}
	m.Step(context.Background(), wantDownAt.Add(time.Second), downStatus, set)
	assertLimiterState(t, m, wantDownAt.Add(time.Second), LimiterInterventionDown)

	wantUpAt := wantDownAt.Add(cfg.DownDuration)
	m.Step(context.Background(), wantUpAt, downStatus, set)
	snapshot = assertLimiterState(t, m, wantUpAt, LimiterInterventionUp)
	if len(actions) != 2 || !actions[1] {
		t.Fatalf("actions=%v, want [false true]", actions)
	}
	if !snapshot.NextAction.Equal(wantUpAt.Add(cfg.upDuration())) {
		t.Fatalf("next cycle=%v", snapshot.NextAction)
	}
}

func TestLimiterResetsWhenBoxTurnsOff(t *testing.T) {
	cfg := testLimiterConfig()
	m := newLimiterMachine(cfg)
	start := time.Now()
	set := func(context.Context, bool) error { return nil }

	m.Step(context.Background(), start, watchingStatus(), set)
	m.Step(context.Background(), start.Add(cfg.WatchLimit), watchingStatus(), set)
	m.Step(context.Background(), start.Add(cfg.WatchLimit+time.Second), PortStatus{AdminUp: true, Carrier: "0"}, set)

	snapshot := assertLimiterState(t, m, start.Add(cfg.WatchLimit+time.Second), LimiterIdle)
	if !snapshot.WatchingSince.IsZero() || !snapshot.NextAction.IsZero() {
		t.Fatalf("idle state retained timers: %+v", snapshot)
	}
}

func TestLimiterDoesNotTreatAutomaticDownAsBoxOff(t *testing.T) {
	cfg := testLimiterConfig()
	m := newLimiterMachine(cfg)
	start := time.Now()
	set := func(context.Context, bool) error { return nil }

	m.Step(context.Background(), start, watchingStatus(), set)
	limitReached := start.Add(cfg.WatchLimit)
	m.Step(context.Background(), limitReached, watchingStatus(), set)
	downAt := limitReached.Add(cfg.upDuration())
	m.Step(context.Background(), downAt, watchingStatus(), set)
	m.Step(context.Background(), downAt.Add(time.Second), PortStatus{AdminUp: false, Carrier: "0"}, set)

	assertLimiterState(t, m, downAt.Add(time.Second), LimiterInterventionDown)
}

func TestLimiterRetriesFailedActionsWithoutCommittingState(t *testing.T) {
	cfg := testLimiterConfig()
	m := newLimiterMachine(cfg)
	start := time.Now()
	setErr := errors.New("set failed")
	set := func(context.Context, bool) error { return setErr }

	m.Step(context.Background(), start, watchingStatus(), set)
	limitReached := start.Add(cfg.WatchLimit)
	m.Step(context.Background(), limitReached, watchingStatus(), set)
	downAt := limitReached.Add(cfg.upDuration())
	m.Step(context.Background(), downAt, watchingStatus(), set)

	snapshot := assertLimiterState(t, m, downAt, LimiterInterventionUp)
	if snapshot.LastError != setErr.Error() {
		t.Fatalf("last_error=%q", snapshot.LastError)
	}
	if !snapshot.NextAction.Equal(downAt.Add(cfg.ActionRetry)) {
		t.Fatalf("retry=%v", snapshot.NextAction)
	}
}

func TestLimiterStopRestoresPortWhenDown(t *testing.T) {
	cfg := testLimiterConfig()
	m := newLimiterMachine(cfg)
	start := time.Now()
	var actions []bool
	set := func(_ context.Context, enabled bool) error {
		actions = append(actions, enabled)
		return nil
	}
	m.Step(context.Background(), start, watchingStatus(), set)
	m.Step(context.Background(), start.Add(cfg.WatchLimit), watchingStatus(), set)
	m.Step(context.Background(), start.Add(cfg.WatchLimit+cfg.upDuration()), watchingStatus(), set)

	if err := m.Stop(context.Background(), set); err != nil {
		t.Fatal(err)
	}
	assertLimiterState(t, m, start, LimiterIdle)
	if len(actions) != 2 || actions[0] || !actions[1] {
		t.Fatalf("actions=%v, want [false true]", actions)
	}
}

func TestLimiterManualOverrideResetsAutomaticState(t *testing.T) {
	cfg := testLimiterConfig()
	m := newLimiterMachine(cfg)
	start := time.Now()
	set := func(context.Context, bool) error { return nil }
	m.Step(context.Background(), start, watchingStatus(), set)
	m.Step(context.Background(), start.Add(cfg.WatchLimit), watchingStatus(), set)

	m.ManualOverride()

	snapshot := assertLimiterState(t, m, start.Add(cfg.WatchLimit), LimiterIdle)
	if !snapshot.WatchingSince.IsZero() || !snapshot.NextAction.IsZero() {
		t.Fatalf("manual override retained automatic timers: %+v", snapshot)
	}
}

func TestLimiterSettingsEnableAndChangeWatchLimit(t *testing.T) {
	cfg := testLimiterConfig()
	cfg.Enabled = false
	m := newLimiterMachine(cfg)

	if err := m.UpdateSettings(context.Background(), true, 45*time.Minute, func(context.Context, bool) error {
		t.Fatal("unexpected port action")
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	snapshot := m.Snapshot(time.Now())
	if !snapshot.Enabled || snapshot.WatchLimitMinutes != 45 || snapshot.State != LimiterIdle {
		t.Fatalf("unexpected settings: %+v", snapshot)
	}
}

func TestDisablingLimiterRestoresAutomaticDown(t *testing.T) {
	cfg := testLimiterConfig()
	m := newLimiterMachine(cfg)
	start := time.Now()
	var actions []bool
	set := func(_ context.Context, enabled bool) error {
		actions = append(actions, enabled)
		return nil
	}
	m.Step(context.Background(), start, watchingStatus(), set)
	m.Step(context.Background(), start.Add(cfg.WatchLimit), watchingStatus(), set)
	m.Step(context.Background(), start.Add(cfg.WatchLimit+cfg.upDuration()), watchingStatus(), set)

	if err := m.UpdateSettings(context.Background(), false, 30*time.Minute, set); err != nil {
		t.Fatal(err)
	}

	snapshot := m.Snapshot(start)
	if snapshot.Enabled || snapshot.State != LimiterIdle || snapshot.WatchLimitMinutes != 30 {
		t.Fatalf("unexpected disabled state: %+v", snapshot)
	}
	if len(actions) != 2 || actions[0] || !actions[1] {
		t.Fatalf("actions=%v, want [false true]", actions)
	}
}

func TestLimiterConfigFromEnv(t *testing.T) {
	t.Setenv("IPTV_LIMITER_ENABLED", "1")
	t.Setenv("IPTV_LIMITER_POLL_INTERVAL", "5s")
	t.Setenv("IPTV_LIMITER_WATCH_LIMIT", "2m")
	t.Setenv("IPTV_LIMITER_CYCLE", "10s")
	t.Setenv("IPTV_LIMITER_DOWN_DURATION", "2s")
	cfg, err := limiterConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled || cfg.PollInterval != 5*time.Second || cfg.WatchLimit != 2*time.Minute ||
		cfg.Cycle != 10*time.Second || cfg.DownDuration != 2*time.Second {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLimiterConfigRejectsInvalidDurations(t *testing.T) {
	t.Setenv("IPTV_LIMITER_CYCLE", "3s")
	t.Setenv("IPTV_LIMITER_DOWN_DURATION", "3s")
	if _, err := limiterConfigFromEnv(); err == nil {
		t.Fatal("expected invalid duration error")
	}
}

func TestLimiterRunnerSimulation(t *testing.T) {
	controller := &runnerFakeController{
		status: PortStatus{Interface: "eth1", Enabled: true, AdminUp: true, Carrier: "1"},
		calls:  make(chan bool, 8),
	}
	cfg := LimiterConfig{
		Enabled:      true,
		PollInterval: 5 * time.Millisecond,
		WatchLimit:   15 * time.Millisecond,
		Cycle:        20 * time.Millisecond,
		DownDuration: 5 * time.Millisecond,
		ActionRetry:  time.Millisecond,
	}
	runner := newLimiterRunner(cfg, controller)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runner.Run(ctx)
		close(done)
	}()

	if got := waitAction(t, controller.calls); got {
		t.Fatalf("first action=%v, want down", got)
	}
	if got := waitAction(t, controller.calls); !got {
		t.Fatalf("second action=%v, want up", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runner did not stop")
	}
}

func TestLimiterRunnerCanBeEnabledAtRuntime(t *testing.T) {
	controller := &runnerFakeController{
		status: PortStatus{Interface: "eth1", Enabled: true, AdminUp: true, Carrier: "1"},
		calls:  make(chan bool, 8),
	}
	cfg := LimiterConfig{
		Enabled:      false,
		PollInterval: 5 * time.Millisecond,
		WatchLimit:   15 * time.Millisecond,
		Cycle:        20 * time.Millisecond,
		DownDuration: 5 * time.Millisecond,
		ActionRetry:  time.Millisecond,
	}
	runner := newLimiterRunner(cfg, controller)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runner.Run(ctx)
		close(done)
	}()
	if err := runner.machine.UpdateSettings(ctx, true, cfg.WatchLimit, controller.SetEnabled); err != nil {
		t.Fatal(err)
	}
	if got := waitAction(t, controller.calls); got {
		t.Fatalf("first action=%v, want down", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runner did not stop")
	}
}

func assertLimiterState(t *testing.T, m *limiterMachine, now time.Time, want LimiterState) LimiterSnapshot {
	t.Helper()
	snapshot := m.Snapshot(now)
	if snapshot.State != want {
		t.Fatalf("state=%s, want %s; snapshot=%+v", snapshot.State, want, snapshot)
	}
	return snapshot
}

type runnerFakeController struct {
	mu     sync.Mutex
	status PortStatus
	calls  chan bool
}

func (f *runnerFakeController) Status(context.Context) (PortStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status, nil
}

func (f *runnerFakeController) SetEnabled(_ context.Context, enabled bool) error {
	f.mu.Lock()
	f.status.Enabled = enabled
	f.status.AdminUp = enabled
	if enabled {
		f.status.Carrier = "1"
	} else {
		f.status.Carrier = "0"
	}
	f.mu.Unlock()
	f.calls <- enabled
	return nil
}

func waitAction(t *testing.T, calls <-chan bool) bool {
	t.Helper()
	select {
	case value := <-calls:
		return value
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for limiter action")
		return false
	}
}
