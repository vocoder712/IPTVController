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
		Cooldown:     30 * time.Minute,
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

func TestLimiterEntersCooldownWhenBoxTurnsOffBeforeLimit(t *testing.T) {
	cfg := testLimiterConfig()
	m := newLimiterMachine(cfg)
	start := time.Now()
	var actions []bool
	set := func(_ context.Context, enabled bool) error {
		actions = append(actions, enabled)
		return nil
	}

	m.Step(context.Background(), start, watchingStatus(), set)
	turnedOff := start.Add(5 * time.Minute)
	m.Step(context.Background(), turnedOff, PortStatus{AdminUp: true, Carrier: "0"}, set)

	snapshot := assertLimiterState(t, m, turnedOff, LimiterCooldown)
	if snapshot.WatchedDuration != 5*time.Minute ||
		!snapshot.CooldownUntil.Equal(turnedOff.Add(cfg.Cooldown)) ||
		!snapshot.NextAction.Equal(snapshot.CooldownUntil) {
		t.Fatalf("unexpected cooldown snapshot: %+v", snapshot)
	}
	if len(actions) != 1 || actions[0] {
		t.Fatalf("actions=%v, want [false]", actions)
	}

	m.Step(
		context.Background(),
		snapshot.CooldownUntil.Add(-time.Second),
		PortStatus{AdminUp: false, Carrier: "0"},
		set,
	)
	assertLimiterState(t, m, snapshot.CooldownUntil.Add(-time.Second), LimiterCooldown)

	m.Step(
		context.Background(),
		snapshot.CooldownUntil,
		PortStatus{AdminUp: false, Carrier: "0"},
		set,
	)
	snapshot = assertLimiterState(t, m, snapshot.CooldownUntil, LimiterIdle)
	if snapshot.WatchedDuration != 0 || len(actions) != 2 || !actions[1] {
		t.Fatalf("cooldown did not restore and clear: snapshot=%+v actions=%v", snapshot, actions)
	}
}

func TestLimiterEntersCooldownWhenCablePulledDuringIntervention(t *testing.T) {
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
	pulled := start.Add(cfg.WatchLimit + time.Second)
	m.Step(context.Background(), pulled, PortStatus{AdminUp: true, Carrier: "0"}, set)

	snapshot := assertLimiterState(t, m, pulled, LimiterCooldown)
	if len(actions) != 1 || actions[0] || snapshot.WatchedDuration < cfg.WatchLimit {
		t.Fatalf("unexpected cooldown: snapshot=%+v actions=%v", snapshot, actions)
	}
}

func TestCooldownForcesPortBackDown(t *testing.T) {
	cfg := testLimiterConfig()
	m := newLimiterMachine(cfg)
	start := time.Now()
	var actions []bool
	set := func(_ context.Context, enabled bool) error {
		actions = append(actions, enabled)
		return nil
	}
	m.Step(context.Background(), start, watchingStatus(), set)
	m.Step(context.Background(), start.Add(time.Minute), PortStatus{AdminUp: true, Carrier: "0"}, set)
	m.Step(context.Background(), start.Add(2*time.Minute), watchingStatus(), set)
	if len(actions) != 2 || actions[0] || actions[1] {
		t.Fatalf("cooldown did not force down: %v", actions)
	}
}

func TestCooldownDownRetryKeepsOriginalDeadline(t *testing.T) {
	cfg := testLimiterConfig()
	m := newLimiterMachine(cfg)
	start := time.Now()
	attempt := 0
	set := func(_ context.Context, enabled bool) error {
		if enabled {
			t.Fatal("unexpected up")
		}
		attempt++
		if attempt == 1 {
			return errors.New("down failed")
		}
		return nil
	}
	m.Step(context.Background(), start, watchingStatus(), set)
	stopped := start.Add(time.Minute)
	m.Step(context.Background(), stopped, PortStatus{AdminUp: true, Carrier: "0"}, set)
	failed := assertLimiterState(t, m, stopped, LimiterCooldown)
	wantUntil := stopped.Add(cfg.Cooldown)
	if !failed.CooldownUntil.Equal(wantUntil) ||
		!failed.NextAction.Equal(stopped.Add(cfg.ActionRetry)) {
		t.Fatalf("unexpected failed cooldown: %+v", failed)
	}
	m.Step(context.Background(), stopped.Add(cfg.ActionRetry), watchingStatus(), set)
	retried := assertLimiterState(t, m, stopped.Add(cfg.ActionRetry), LimiterCooldown)
	if !retried.CooldownUntil.Equal(wantUntil) || !retried.NextAction.Equal(wantUntil) {
		t.Fatalf("retry extended cooldown: %+v", retried)
	}
}

func TestCooldownUpRetryKeepsStateUntilSuccess(t *testing.T) {
	cfg := testLimiterConfig()
	m := newLimiterMachine(cfg)
	start := time.Now()
	upAttempts := 0
	set := func(_ context.Context, enabled bool) error {
		if enabled {
			upAttempts++
			if upAttempts == 1 {
				return errors.New("up failed")
			}
		}
		return nil
	}
	m.Step(context.Background(), start, watchingStatus(), set)
	stopped := start.Add(time.Minute)
	m.Step(context.Background(), stopped, PortStatus{AdminUp: true, Carrier: "0"}, set)
	until := stopped.Add(cfg.Cooldown)
	m.Step(context.Background(), until, PortStatus{AdminUp: false, Carrier: "0"}, set)
	failed := assertLimiterState(t, m, until, LimiterCooldown)
	if !failed.CooldownUntil.Equal(until) ||
		!failed.NextAction.Equal(until.Add(cfg.ActionRetry)) {
		t.Fatalf("unexpected up retry: %+v", failed)
	}
	m.Step(
		context.Background(),
		until.Add(cfg.ActionRetry),
		PortStatus{AdminUp: false, Carrier: "0"},
		set,
	)
	assertLimiterState(t, m, until.Add(cfg.ActionRetry), LimiterIdle)
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

	if err := m.UpdateSettings(context.Background(), true, 45*time.Minute, 12*time.Second, func(context.Context, bool) error {
		t.Fatal("unexpected port action")
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	snapshot := m.Snapshot(time.Now())
	if !snapshot.Enabled || snapshot.WatchLimitMinutes != 45 || snapshot.BlockSeconds != 12 ||
		snapshot.State != LimiterIdle {
		t.Fatalf("unexpected settings: %+v", snapshot)
	}
}

func TestChangingSettingsPreservesWatchingProgress(t *testing.T) {
	cfg := testLimiterConfig()
	m := newLimiterMachine(cfg)
	start := time.Now()
	set := func(context.Context, bool) error {
		t.Fatal("unexpected port action")
		return nil
	}
	m.Step(context.Background(), start, watchingStatus(), set)
	changed := start.Add(7 * time.Minute)
	if err := m.UpdateSettings(context.Background(), true, 30*time.Minute, 15*time.Second, set); err != nil {
		t.Fatal(err)
	}
	snapshot := assertLimiterState(t, m, changed, LimiterWatching)
	if snapshot.WatchedDuration != 7*time.Minute || snapshot.WatchLimitMinutes != 30 ||
		snapshot.BlockSeconds != 15 {
		t.Fatalf("settings reset progress: %+v", snapshot)
	}
}

func TestChangingSettingsPreservesInterventionStateAndTimer(t *testing.T) {
	cfg := testLimiterConfig()
	m := newLimiterMachine(cfg)
	start := time.Now()
	set := func(context.Context, bool) error { return nil }
	m.Step(context.Background(), start, watchingStatus(), set)
	reached := start.Add(cfg.WatchLimit)
	m.Step(context.Background(), reached, watchingStatus(), set)
	before := m.Snapshot(reached)

	if err := m.UpdateSettings(context.Background(), true, 40*time.Minute, 20*time.Second, set); err != nil {
		t.Fatal(err)
	}
	after := assertLimiterState(t, m, reached, LimiterInterventionUp)
	if !after.NextAction.Equal(before.NextAction) || after.WatchedDuration != before.WatchedDuration ||
		after.WatchLimitMinutes != 40 || after.BlockSeconds != 20 {
		t.Fatalf("settings changed intervention state: before=%+v after=%+v", before, after)
	}
}

func TestChangingSettingsPreservesInterventionDown(t *testing.T) {
	cfg := testLimiterConfig()
	m := newLimiterMachine(cfg)
	start := time.Now()
	var actions []bool
	set := func(_ context.Context, enabled bool) error {
		actions = append(actions, enabled)
		return nil
	}
	m.Step(context.Background(), start, watchingStatus(), set)
	reached := start.Add(cfg.WatchLimit)
	m.Step(context.Background(), reached, watchingStatus(), set)
	downAt := reached.Add(cfg.upDuration())
	m.Step(context.Background(), downAt, watchingStatus(), set)
	before := assertLimiterState(t, m, downAt, LimiterInterventionDown)

	if err := m.UpdateSettings(context.Background(), true, 40*time.Minute, 20*time.Second, set); err != nil {
		t.Fatal(err)
	}
	after := assertLimiterState(t, m, downAt, LimiterInterventionDown)
	if !after.NextAction.Equal(before.NextAction) || len(actions) != 1 || actions[0] {
		t.Fatalf("settings exited down phase: before=%+v after=%+v actions=%v", before, after, actions)
	}
}

func TestChangingSettingsPreservesCooldown(t *testing.T) {
	cfg := testLimiterConfig()
	m := newLimiterMachine(cfg)
	start := time.Now()
	set := func(context.Context, bool) error { return nil }
	m.Step(context.Background(), start, watchingStatus(), set)
	stopped := start.Add(5 * time.Minute)
	m.Step(context.Background(), stopped, PortStatus{AdminUp: true, Carrier: "0"}, set)
	before := assertLimiterState(t, m, stopped, LimiterCooldown)

	if err := m.UpdateSettings(context.Background(), true, 40*time.Minute, 20*time.Second, set); err != nil {
		t.Fatal(err)
	}
	after := assertLimiterState(t, m, stopped, LimiterCooldown)
	if !after.CooldownUntil.Equal(before.CooldownUntil) ||
		!after.NextAction.Equal(before.NextAction) ||
		after.WatchedDuration != before.WatchedDuration {
		t.Fatalf("settings exited cooldown: before=%+v after=%+v", before, after)
	}
}

func TestManualInterventionOnlyWhileWatching(t *testing.T) {
	cfg := testLimiterConfig()
	m := newLimiterMachine(cfg)
	now := time.Now()
	if err := m.StartIntervention(now); err == nil {
		t.Fatal("expected idle intervention to fail")
	}
	m.Step(context.Background(), now, watchingStatus(), func(context.Context, bool) error { return nil })
	if err := m.StartIntervention(now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	snapshot := assertLimiterState(t, m, now.Add(time.Minute), LimiterInterventionUp)
	if snapshot.WatchedDuration != time.Minute ||
		!snapshot.NextAction.Equal(now.Add(time.Minute).Add(cfg.upDuration())) {
		t.Fatalf("unexpected manual intervention: %+v", snapshot)
	}
	if err := m.StartIntervention(now.Add(2 * time.Minute)); err == nil {
		t.Fatal("expected repeated intervention to fail")
	}
}

func TestLimiterSettingsRejectInvalidBlockSeconds(t *testing.T) {
	m := newLimiterMachine(defaultLimiterConfig())
	for _, duration := range []time.Duration{0, 26 * time.Second, 1500 * time.Millisecond} {
		if err := m.UpdateSettings(
			context.Background(),
			true,
			20*time.Minute,
			duration,
			func(context.Context, bool) error { return nil },
		); err == nil {
			t.Fatalf("expected block duration %v to fail", duration)
		}
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

	if err := m.UpdateSettings(context.Background(), false, 30*time.Minute, 6*time.Second, set); err != nil {
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

func TestDisablingLimiterRestoresCooldownPort(t *testing.T) {
	cfg := testLimiterConfig()
	m := newLimiterMachine(cfg)
	now := time.Now()
	var actions []bool
	set := func(_ context.Context, enabled bool) error {
		actions = append(actions, enabled)
		return nil
	}
	m.Step(context.Background(), now, watchingStatus(), set)
	m.Step(context.Background(), now.Add(time.Minute), PortStatus{AdminUp: true, Carrier: "0"}, set)
	if err := m.UpdateSettings(context.Background(), false, 30*time.Minute, 10*time.Second, set); err != nil {
		t.Fatal(err)
	}
	snapshot := assertLimiterState(t, m, now, LimiterIdle)
	if snapshot.Enabled || len(actions) != 2 || actions[0] || !actions[1] {
		t.Fatalf("cooldown disable did not restore: snapshot=%+v actions=%v", snapshot, actions)
	}
}

func TestLimiterConfigFromEnv(t *testing.T) {
	t.Setenv("IPTV_LIMITER_ENABLED", "1")
	t.Setenv("IPTV_LIMITER_POLL_INTERVAL", "5s")
	t.Setenv("IPTV_LIMITER_WATCH_LIMIT", "2m")
	t.Setenv("IPTV_LIMITER_CYCLE", "10s")
	t.Setenv("IPTV_LIMITER_DOWN_DURATION", "2s")
	t.Setenv("IPTV_LIMITER_COOLDOWN_DURATION", "45m")
	cfg, err := limiterConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled || cfg.PollInterval != 5*time.Second || cfg.WatchLimit != 2*time.Minute ||
		cfg.Cycle != 10*time.Second || cfg.DownDuration != 2*time.Second || cfg.Cooldown != 45*time.Minute {
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

func TestLimiterConfigRejectsInvalidCooldown(t *testing.T) {
	t.Setenv("IPTV_LIMITER_COOLDOWN_DURATION", "0s")
	if _, err := limiterConfigFromEnv(); err == nil {
		t.Fatal("expected invalid cooldown error")
	}
}

func TestLimiterConfigRejectsBlockDurationOutsideParentRange(t *testing.T) {
	for _, value := range []string{"500ms", "26s"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("IPTV_LIMITER_DOWN_DURATION", value)
			if _, err := limiterConfigFromEnv(); err == nil {
				t.Fatalf("expected %s to fail", value)
			}
		})
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
		Cycle:        1100 * time.Millisecond,
		DownDuration: time.Second,
		ActionRetry:  time.Millisecond,
	}
	runner := newLimiterRunner(cfg, controller)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runner.Run(ctx)
		close(done)
	}()
	if err := runner.machine.UpdateSettings(ctx, true, cfg.WatchLimit, cfg.DownDuration, controller.SetEnabled); err != nil {
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
