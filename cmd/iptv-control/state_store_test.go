package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestStateStoreRejectsPreviousVersion(t *testing.T) {
	type legacyState struct {
		Version            int    `json:"version"`
		Sequence           uint64 `json:"sequence"`
		SavedAt            string `json:"saved_at"`
		Enabled            bool   `json:"enabled"`
		WatchLimitSeconds  int64  `json:"watch_limit_seconds"`
		AccumulatedSeconds int64  `json:"accumulated_seconds"`
		Phase              string `json:"phase"`
	}
	legacy := legacyState{
		Version:           persistedStateVersion - 1,
		Sequence:          7,
		SavedAt:           "2026-07-24T12:00:00Z",
		Enabled:           true,
		WatchLimitSeconds: 300,
		Phase:             string(LimiterIdle),
	}
	payload, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	line := fmt.Sprintf(`{"state":%s,"crc32":%d}`+"\n", payload, crc32.ChecksumIEEE(payload))
	path := filepath.Join(t.TempDir(), "state.log")
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}

	store := newStateStore(path)
	if _, err := store.Load(); !errors.Is(err, errStateVersionMismatch) {
		t.Fatalf("error=%v, want version mismatch", err)
	}
	if err := store.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old state log still exists: %v", err)
	}
}

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
		Enabled:                true,
		WatchLimitSeconds:      20 * 60,
		MinDownDurationSeconds: 7,
		MaxDownDurationSeconds: 17,
		AccumulatedSeconds:     10 * 60,
		Phase:                  string(LimiterWatching),
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	set := func(context.Context, bool) error { return nil }
	m.Step(context.Background(), start, watchingStatus(), set)
	snapshot := m.Snapshot(start)
	if snapshot.State != LimiterWatching || snapshot.WatchedDuration != 10*time.Minute ||
		snapshot.BlockMinSeconds != 7 || snapshot.BlockMaxSeconds != 17 {
		t.Fatalf("unexpected restored watching state: %+v", snapshot)
	}
	m.Step(context.Background(), start.Add(10*time.Minute), watchingStatus(), set)
	snapshot = m.Snapshot(start.Add(10 * time.Minute))
	if snapshot.State != LimiterInterventionDown {
		t.Fatalf("state=%s, want intervention_down", snapshot.State)
	}
}

func TestLimiterRejectsPersistedStateWithoutBlockRange(t *testing.T) {
	cfg := defaultLimiterConfig()
	m := newLimiterMachine(cfg)
	if err := m.Restore(persistedLimiterState{
		Enabled:           true,
		WatchLimitSeconds: 20 * 60,
		Phase:             string(LimiterIdle),
	}, time.Now()); err == nil {
		t.Fatal("expected missing block range to fail")
	}
}

func TestLimiterRejectsInvalidPersistedBlockRange(t *testing.T) {
	m := newLimiterMachine(defaultLimiterConfig())
	if err := m.Restore(persistedLimiterState{
		Enabled:                true,
		WatchLimitSeconds:      20 * 60,
		MinDownDurationSeconds: 20,
		MaxDownDurationSeconds: 10,
	}, time.Now()); err == nil {
		t.Fatal("expected invalid persisted block duration")
	}
}

func TestLimiterRestoresInterventionByStartingNewDownCycle(t *testing.T) {
	m := newLimiterMachine(testLimiterConfig())
	if err := m.Restore(persistedLimiterState{
		Enabled:                true,
		WatchLimitSeconds:      20 * 60,
		MinDownDurationSeconds: 6,
		MaxDownDurationSeconds: 6,
		AccumulatedSeconds:     20 * 60,
		Phase:                  string(LimiterInterventionDown),
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	var actions []bool
	m.Step(context.Background(), now, watchingStatus(), func(_ context.Context, enabled bool) error {
		actions = append(actions, enabled)
		return nil
	})
	snapshot := m.Snapshot(now)
	if snapshot.State != LimiterInterventionDown || snapshot.NextAction.IsZero() {
		t.Fatalf("unexpected restored intervention: %+v", snapshot)
	}
	if len(actions) != 1 || actions[0] {
		t.Fatalf("restore did not start a down cycle: %v", actions)
	}
}

func TestRestoredProgressEntersCooldownWhenCarrierIsOff(t *testing.T) {
	m := newLimiterMachine(testLimiterConfig())
	if err := m.Restore(persistedLimiterState{
		Enabled:                true,
		WatchLimitSeconds:      20 * 60,
		MinDownDurationSeconds: 6,
		MaxDownDurationSeconds: 6,
		AccumulatedSeconds:     10 * 60,
		Phase:                  string(LimiterWatching),
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	var actions []bool
	m.Step(context.Background(), now, PortStatus{AdminUp: true, Carrier: "0"}, func(_ context.Context, enabled bool) error {
		actions = append(actions, enabled)
		return nil
	})
	snapshot := m.Snapshot(now)
	if snapshot.State != LimiterCooldown || snapshot.WatchedDuration != 10*time.Minute ||
		len(actions) != 1 || actions[0] {
		t.Fatalf("restored progress did not enter cooldown: snapshot=%+v actions=%v", snapshot, actions)
	}
}

func TestLimiterRestoresUnexpiredCooldown(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	until := now.Add(17 * time.Minute)
	m := newLimiterMachine(testLimiterConfig())
	if err := m.Restore(persistedLimiterState{
		Enabled:                true,
		WatchLimitSeconds:      20 * 60,
		MinDownDurationSeconds: 6,
		MaxDownDurationSeconds: 6,
		AccumulatedSeconds:     8 * 60,
		Phase:                  string(LimiterCooldown),
		CooldownUntil:          until.Format(time.RFC3339Nano),
	}, now); err != nil {
		t.Fatal(err)
	}
	snapshot := m.Snapshot(now)
	if snapshot.State != LimiterCooldown || snapshot.WatchedDuration != 8*time.Minute ||
		!snapshot.CooldownUntil.Equal(until) || !snapshot.NextAction.Equal(until) {
		t.Fatalf("unexpected restored cooldown: %+v", snapshot)
	}
}

func TestCooldownPersistentStateRoundTrip(t *testing.T) {
	start := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	m := newLimiterMachine(testLimiterConfig())
	m.Step(context.Background(), start, watchingStatus(), func(context.Context, bool) error { return nil })
	stopped := start.Add(8 * time.Minute)
	m.Step(
		context.Background(),
		stopped,
		PortStatus{AdminUp: true, Carrier: "0"},
		func(context.Context, bool) error { return nil },
	)
	state := m.PersistentState(stopped.Add(time.Minute))
	if state.Phase != string(LimiterCooldown) || state.AccumulatedSeconds != 8*60 ||
		state.CooldownUntil == "" {
		t.Fatalf("unexpected persistent cooldown: %+v", state)
	}

	path := filepath.Join(t.TempDir(), "state.log")
	if err := newStateStore(path).Save(state); err != nil {
		t.Fatal(err)
	}
	loaded, err := newStateStore(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	restored := newLimiterMachine(testLimiterConfig())
	if err := restored.Restore(loaded, stopped.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	snapshot := restored.Snapshot(stopped.Add(2 * time.Minute))
	if snapshot.State != LimiterCooldown || snapshot.WatchedDuration != 8*time.Minute {
		t.Fatalf("cooldown round trip failed: %+v", snapshot)
	}
}

func TestLimiterDiscardsExpiredCooldown(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	m := newLimiterMachine(testLimiterConfig())
	if err := m.Restore(persistedLimiterState{
		Enabled:                true,
		WatchLimitSeconds:      20 * 60,
		MinDownDurationSeconds: 6,
		MaxDownDurationSeconds: 6,
		AccumulatedSeconds:     8 * 60,
		Phase:                  string(LimiterCooldown),
		CooldownUntil:          now.Add(-time.Second).Format(time.RFC3339Nano),
	}, now); err != nil {
		t.Fatal(err)
	}
	snapshot := m.Snapshot(now)
	if snapshot.State != LimiterIdle || snapshot.WatchedDuration != 0 {
		t.Fatalf("expired cooldown was restored: %+v", snapshot)
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
	if err := m.UpdateSettings(context.Background(), true, 30*time.Minute, 8*time.Second, 18*time.Second, func(context.Context, bool) error {
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
	if after.MinDownDurationSeconds != 8 || after.MaxDownDurationSeconds != 18 {
		t.Fatalf("block range was not persisted: %+v", after)
	}
}

func TestEnteringCooldownForcesPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.log")
	store := newStateStore(path)
	cfg := testLimiterConfig()
	controller := &runnerFakeController{
		status: PortStatus{Interface: "eth1", Enabled: true, AdminUp: true, Carrier: "1"},
		calls:  make(chan bool, 2),
	}
	runner := newLimiterRunner(cfg, controller)
	persister := newStatePersister(store, runner.machine)
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	runner.now = func() time.Time { return now }
	persister.now = func() time.Time { return now }
	runner.persister = persister

	persister.Request(false)
	if err := persister.FlushDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	runner.poll(context.Background())
	controller.mu.Lock()
	controller.status.Carrier = "0"
	controller.mu.Unlock()
	now = now.Add(time.Second)
	runner.poll(context.Background())
	if err := persister.FlushDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	loaded, err := newStateStore(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Phase != string(LimiterCooldown) || loaded.CooldownUntil == "" {
		t.Fatalf("cooldown was not force-persisted: %+v", loaded)
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
