package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	persistedStateVersion = 1
	defaultStateLogLimit  = 64 * 1024
)

type persistedLimiterState struct {
	Version             int    `json:"version"`
	Sequence            uint64 `json:"sequence"`
	SavedAt             string `json:"saved_at"`
	Enabled             bool   `json:"enabled"`
	WatchLimitSeconds   int64  `json:"watch_limit_seconds"`
	DownDurationSeconds int64  `json:"down_duration_seconds,omitempty"`
	AccumulatedSeconds  int64  `json:"accumulated_seconds"`
	Phase               string `json:"phase"`
}

type stateLogRecord struct {
	State persistedLimiterState `json:"state"`
	CRC32 uint32                `json:"crc32"`
}

type stateStore struct {
	mu       sync.Mutex
	path     string
	sequence uint64
	maxBytes int64
}

func newStateStore(path string) *stateStore {
	return &stateStore{path: path, maxBytes: defaultStateLogLimit}
}

func (s *stateStore) Load() (persistedLimiterState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.Open(s.path)
	if err != nil {
		return persistedLimiterState{}, err
	}
	defer f.Close()

	var latest persistedLimiterState
	found := false
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		var record stateLogRecord
		if json.Unmarshal(scanner.Bytes(), &record) != nil || !validStateRecord(record) {
			continue
		}
		if !found || record.State.Sequence > latest.Sequence {
			latest = record.State
			found = true
		}
	}
	if err := scanner.Err(); err != nil {
		return persistedLimiterState{}, fmt.Errorf("scan state log: %w", err)
	}
	if !found {
		return persistedLimiterState{}, fmt.Errorf("state log has no valid records")
	}
	s.sequence = latest.Sequence
	return latest, nil
}

func (s *stateStore) Save(state persistedLimiterState) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	s.sequence++
	state.Version = persistedStateVersion
	state.Sequence = s.sequence
	record, err := encodeStateRecord(state)
	if err != nil {
		return err
	}

	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open state log: %w", err)
	}
	_, writeErr := f.Write(append(record, '\n'))
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil {
		return fmt.Errorf("append state log: %w", writeErr)
	}
	if syncErr != nil {
		return fmt.Errorf("sync state log: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close state log: %w", closeErr)
	}

	info, err := os.Stat(s.path)
	if err != nil {
		return fmt.Errorf("stat state log: %w", err)
	}
	if info.Size() >= s.maxBytes {
		if err := s.compactLocked(record); err != nil {
			return err
		}
	}
	return nil
}

func (s *stateStore) compactLocked(latestRecord []byte) error {
	tmp := s.path + ".new"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create compacted state log: %w", err)
	}
	_, writeErr := f.Write(append(latestRecord, '\n'))
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(tmp)
		return errors.Join(writeErr, syncErr, closeErr)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("install compacted state log: %w", err)
	}
	if dir, err := os.Open(filepath.Dir(s.path)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

func encodeStateRecord(state persistedLimiterState) ([]byte, error) {
	payload, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("encode state payload: %w", err)
	}
	record := stateLogRecord{State: state, CRC32: crc32.ChecksumIEEE(payload)}
	encoded, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("encode state record: %w", err)
	}
	return encoded, nil
}

func validStateRecord(record stateLogRecord) bool {
	if record.State.Version != persistedStateVersion || record.State.Sequence == 0 {
		return false
	}
	payload, err := json.Marshal(record.State)
	return err == nil && crc32.ChecksumIEEE(payload) == record.CRC32
}

type statePersister struct {
	mu            sync.Mutex
	store         *stateStore
	machine       *limiterMachine
	now           func() time.Time
	minWriteGap   time.Duration
	dirty         bool
	force         bool
	lastWrite     time.Time
	lastSaved     persistedLimiterState
	haveLastSaved bool
	lastError     string
}

func newStatePersister(store *stateStore, machine *limiterMachine) *statePersister {
	return &statePersister{
		store:       store,
		machine:     machine,
		now:         time.Now,
		minWriteGap: 30 * time.Second,
	}
}

func (p *statePersister) SetLoaded(state persistedLimiterState) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastSaved = state
	p.haveLastSaved = true
}

func (p *statePersister) Request(force bool) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.dirty = true
	p.force = p.force || force
	p.mu.Unlock()
}

func (p *statePersister) FlushDue(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.dirty {
		return nil
	}
	now := p.now()
	if !p.force && !p.lastWrite.IsZero() && now.Sub(p.lastWrite) < p.minWriteGap {
		return nil
	}
	return p.flushLocked(ctx, now)
}

func (p *statePersister) FlushNow(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.dirty {
		return nil
	}
	return p.flushLocked(ctx, p.now())
}

func (p *statePersister) flushLocked(ctx context.Context, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	state := p.machine.PersistentState(now)
	if p.haveLastSaved && samePersistedContent(state, p.lastSaved) {
		p.dirty = false
		p.force = false
		p.lastError = ""
		return nil
	}
	if err := p.store.Save(state); err != nil {
		p.lastError = err.Error()
		return err
	}
	p.lastSaved = state
	p.haveLastSaved = true
	p.lastWrite = now
	p.dirty = false
	p.force = false
	p.lastError = ""
	return nil
}

func (p *statePersister) Status() (pending bool, lastError string) {
	if p == nil {
		return false, ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dirty, p.lastError
}

func samePersistedContent(a, b persistedLimiterState) bool {
	return a.Enabled == b.Enabled &&
		a.WatchLimitSeconds == b.WatchLimitSeconds &&
		a.DownDurationSeconds == b.DownDurationSeconds &&
		a.AccumulatedSeconds == b.AccumulatedSeconds &&
		a.Phase == b.Phase
}
