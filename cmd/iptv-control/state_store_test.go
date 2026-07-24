package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestStateStoreLoadsLatestValidRecordAndIgnoresTornTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "state.log")
	store := newStateStore(path)
	first := persistedLimiterState{
		SavedAt:            "2026-07-24T12:00:00Z",
		Enabled:            true,
		WatchLimitSeconds:  1200,
		AccumulatedSeconds: 300,
		Phase:              string(LimiterWatching),
	}
	second := first
	second.AccumulatedSeconds = 600
	if err := store.Save(first); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(second); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"state":`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	reloaded := newStateStore(path)
	got, err := reloaded.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Sequence != 2 || got.AccumulatedSeconds != 600 {
		t.Fatalf("unexpected restored state: %+v", got)
	}
}

func TestStateStoreRejectsLogWithoutValidRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.log")
	if err := os.WriteFile(path, []byte("{broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newStateStore(path).Load(); err == nil {
		t.Fatal("expected invalid state log error")
	}
}

func TestStateStoreCompactsLogToLatestRecord(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows rename cannot atomically replace the open destination used by production Linux")
	}
	path := filepath.Join(t.TempDir(), "state.log")
	store := newStateStore(path)
	store.maxBytes = 1
	state := persistedLimiterState{Enabled: true, WatchLimitSeconds: 1200}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	state.WatchLimitSeconds = 1800
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	got, err := newStateStore(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.WatchLimitSeconds != 1800 || got.Sequence != 2 {
		t.Fatalf("unexpected compacted state: %+v", got)
	}
}

func TestLimiterRestoresAccumulatedWatchingTime(t *testing.T) {
	cfg := testLimiterConfig()
	cfg.Enabled = false
	m := newLimiterMachine(cfg)
	if err := m.Restore(persistedLimiterState{
		Enabled:            true,
		WatchLimitSeconds:  20 * 60,
		AccumulatedSeconds: 10 * 60,
		Phase:              string(LimiterWatching),
	}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	set := func(context.Context, bool) error { return nil }
	m.Step(context.Background(), start, watchingStatus(), set)
	snapshot := m.Snapshot(start)
	if snapshot.State != LimiterWatching || snapshot.WatchedDuration != 10*time.Minute {
		t.Fatalf("unexpected restored watching state: %+v", snapshot)
	}
	m.Step(context.Background(), start.Add(10*time.Minute), watchingStatus(), set)
	snapshot = m.Snapshot(start.Add(10 * time.Minute))
	if snapshot.State != LimiterInterventionUp {
		t.Fatalf("state=%s, want intervention_up", snapshot.State)
	}
}

func TestLimiterRestoresInterventionAsUpOnly(t *testing.T) {
	m := newLimiterMachine(testLimiterConfig())
	if err := m.Restore(persistedLimiterState{
		Enabled:            true,
		WatchLimitSeconds:  20 * 60,
		AccumulatedSeconds: 20 * 60,
		Phase:              string(LimiterInterventionDown),
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	var actions []bool
	m.Step(context.Background(), now, watchingStatus(), func(_ context.Context, enabled bool) error {
		actions = append(actions, enabled)
		return nil
	})
	snapshot := m.Snapshot(now)
	if snapshot.State != LimiterInterventionUp || snapshot.NextAction.IsZero() {
		t.Fatalf("unexpected restored intervention: %+v", snapshot)
	}
	if len(actions) != 0 {
		t.Fatalf("restore performed port action: %v", actions)
	}
}

func TestRestoredProgressClearsWhenCarrierIsOff(t *testing.T) {
	m := newLimiterMachine(testLimiterConfig())
	if err := m.Restore(persistedLimiterState{
		Enabled:            true,
		WatchLimitSeconds:  20 * 60,
		AccumulatedSeconds: 10 * 60,
		Phase:              string(LimiterWatching),
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	m.Step(context.Background(), now, PortStatus{AdminUp: true, Carrier: "0"}, func(context.Context, bool) error {
		return nil
	})
	snapshot := m.Snapshot(now)
	if snapshot.State != LimiterIdle || snapshot.WatchedDuration != 0 {
		t.Fatalf("stale progress was not cleared: %+v", snapshot)
	}
}

func TestStatePersisterCoalescesWritesWithinMinimumGap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.log")
	store := newStateStore(path)
	m := newLimiterMachine(defaultLimiterConfig())
	p := newStatePersister(store, m)
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return now }
	p.minWriteGap = 30 * time.Second

	p.Request(false)
	if err := p.FlushDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.UpdateSettings(context.Background(), true, 30*time.Minute, func(context.Context, bool) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	p.Request(false)
	now = now.Add(10 * time.Second)
	if err := p.FlushDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, err := newStateStore(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	if before.WatchLimitSeconds != 20*60 {
		t.Fatalf("write was not coalesced: %+v", before)
	}

	now = now.Add(20 * time.Second)
	if err := p.FlushDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, err := newStateStore(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	if after.WatchLimitSeconds != 30*60 {
		t.Fatalf("coalesced state was not flushed: %+v", after)
	}
}

func TestStateRecordCRCDetectsMutation(t *testing.T) {
	state := persistedLimiterState{
		Version:           persistedStateVersion,
		Sequence:          1,
		WatchLimitSeconds: 1200,
	}
	encoded, err := encodeStateRecord(state)
	if err != nil {
		t.Fatal(err)
	}
	encoded[len(encoded)-2] ^= 1
	path := filepath.Join(t.TempDir(), "state.log")
	if err := os.WriteFile(path, append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newStateStore(path).Load(); err == nil {
		t.Fatal("expected CRC or JSON validation failure")
	}
}
