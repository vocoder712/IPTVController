package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"
)

type LimiterState string

const (
	LimiterIdle             LimiterState = "idle"
	LimiterWatching         LimiterState = "watching"
	LimiterInterventionUp   LimiterState = "intervention_up"
	LimiterInterventionDown LimiterState = "intervention_down"

	minBlockSeconds = 1
	maxBlockSeconds = 25
)

type LimiterConfig struct {
	Enabled      bool
	PollInterval time.Duration
	WatchLimit   time.Duration
	Cycle        time.Duration
	DownDuration time.Duration
	ActionRetry  time.Duration
}

func defaultLimiterConfig() LimiterConfig {
	return LimiterConfig{
		PollInterval: 30 * time.Second,
		WatchLimit:   20 * time.Minute,
		Cycle:        time.Minute,
		DownDuration: 6 * time.Second,
		ActionRetry:  time.Second,
	}
}

func limiterConfigFromEnv() (LimiterConfig, error) {
	cfg := defaultLimiterConfig()
	cfg.Enabled = os.Getenv("IPTV_LIMITER_ENABLED") == "1"
	var err error
	if cfg.PollInterval, err = durationEnv("IPTV_LIMITER_POLL_INTERVAL", cfg.PollInterval); err != nil {
		return cfg, err
	}
	if cfg.WatchLimit, err = durationEnv("IPTV_LIMITER_WATCH_LIMIT", cfg.WatchLimit); err != nil {
		return cfg, err
	}
	if cfg.Cycle, err = durationEnv("IPTV_LIMITER_CYCLE", cfg.Cycle); err != nil {
		return cfg, err
	}
	if cfg.DownDuration, err = durationEnv("IPTV_LIMITER_DOWN_DURATION", cfg.DownDuration); err != nil {
		return cfg, err
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func durationEnv(name string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	return value, nil
}

func (c LimiterConfig) Validate() error {
	switch {
	case c.PollInterval <= 0:
		return fmt.Errorf("poll interval must be positive")
	case c.WatchLimit <= 0:
		return fmt.Errorf("watch limit must be positive")
	case c.Cycle <= 0:
		return fmt.Errorf("cycle must be positive")
	case c.DownDuration <= 0:
		return fmt.Errorf("down duration must be positive")
	case c.DownDuration%time.Second != 0:
		return fmt.Errorf("down duration must be a whole number of seconds")
	case c.DownDuration < minBlockSeconds*time.Second || c.DownDuration > maxBlockSeconds*time.Second:
		return fmt.Errorf("down duration must be between %d and %d seconds", minBlockSeconds, maxBlockSeconds)
	case c.DownDuration >= c.Cycle:
		return fmt.Errorf("down duration must be shorter than cycle")
	case c.ActionRetry <= 0:
		return fmt.Errorf("action retry must be positive")
	default:
		return nil
	}
}

func (c LimiterConfig) upDuration() time.Duration {
	return c.Cycle - c.DownDuration
}

type LimiterSnapshot struct {
	Enabled            bool          `json:"enabled"`
	State              LimiterState  `json:"state"`
	WatchLimitMinutes  int           `json:"watch_limit_minutes"`
	BlockSeconds       int           `json:"block_seconds"`
	WatchingSince      time.Time     `json:"watching_since,omitempty"`
	WatchedDuration    time.Duration `json:"watched_duration"`
	NextAction         time.Time     `json:"next_action,omitempty"`
	LastError          string        `json:"last_error,omitempty"`
	PersistencePending bool          `json:"persistence_pending,omitempty"`
	PersistenceError   string        `json:"persistence_error,omitempty"`
}

type limiterMachine struct {
	mu            sync.Mutex
	config        LimiterConfig
	state         LimiterState
	watchingSince time.Time
	watchedBefore time.Duration
	nextAction    time.Time
	lastError     string
}

func newLimiterMachine(config LimiterConfig) *limiterMachine {
	return &limiterMachine{config: config, state: LimiterIdle}
}

func (m *limiterMachine) Step(ctx context.Context, now time.Time, status PortStatus, setEnabled func(context.Context, bool) error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.config.Enabled {
		return
	}

	switch m.state {
	case LimiterIdle:
		if isWatching(status) {
			m.state = LimiterWatching
			m.watchingSince = now
			m.watchedBefore = 0
		}
	case LimiterWatching:
		if !isWatching(status) {
			m.resetLocked()
			return
		}
		if m.watchingSince.IsZero() {
			m.watchingSince = now
		}
		if m.watchedDurationLocked(now) >= m.config.WatchLimit {
			m.watchedBefore = m.watchedDurationLocked(now)
			m.watchingSince = time.Time{}
			m.state = LimiterInterventionUp
			m.nextAction = now.Add(m.config.upDuration())
		}
	case LimiterInterventionUp:
		// carrier=0 is meaningful only while the interface is administratively up.
		if status.AdminUp && status.Carrier == "0" {
			m.resetLocked()
			return
		}
		if m.nextAction.IsZero() {
			if isWatching(status) {
				m.nextAction = now.Add(m.config.upDuration())
			}
			return
		}
		if !now.Before(m.nextAction) {
			if err := setEnabled(ctx, false); err != nil {
				m.lastError = err.Error()
				m.nextAction = now.Add(m.config.ActionRetry)
				return
			}
			m.lastError = ""
			m.state = LimiterInterventionDown
			m.nextAction = now.Add(m.config.DownDuration)
		}
	case LimiterInterventionDown:
		// carrier is necessarily 0 after our own down action and must be ignored.
		if !m.nextAction.IsZero() && !now.Before(m.nextAction) {
			if err := setEnabled(ctx, true); err != nil {
				m.lastError = err.Error()
				m.nextAction = now.Add(m.config.ActionRetry)
				return
			}
			m.lastError = ""
			m.state = LimiterInterventionUp
			m.nextAction = now.Add(m.config.upDuration())
		}
	default:
		m.resetLocked()
	}
}

func (m *limiterMachine) Stop(ctx context.Context, setEnabled func(context.Context, bool) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state == LimiterInterventionDown {
		if err := setEnabled(ctx, true); err != nil {
			m.lastError = err.Error()
			return err
		}
	}
	m.resetLocked()
	return nil
}

func (m *limiterMachine) UpdateSettings(
	ctx context.Context,
	enabled bool,
	watchLimit time.Duration,
	downDuration time.Duration,
	setEnabled func(context.Context, bool) error,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if watchLimit <= 0 {
		return fmt.Errorf("watch limit must be positive")
	}
	if err := validateBlockDuration(downDuration, m.config.Cycle); err != nil {
		return err
	}
	if m.state == LimiterInterventionDown {
		if err := setEnabled(ctx, true); err != nil {
			m.lastError = err.Error()
			return fmt.Errorf("restore port before updating limiter: %w", err)
		}
	}
	m.config.Enabled = enabled
	m.config.WatchLimit = watchLimit
	m.config.DownDuration = downDuration
	m.resetLocked()
	return nil
}

func (m *limiterMachine) ManualOverride() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resetLocked()
}

func (m *limiterMachine) Snapshot(now time.Time) LimiterSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	watched := m.watchedDurationLocked(now)
	return LimiterSnapshot{
		Enabled:           m.config.Enabled,
		State:             m.state,
		WatchLimitMinutes: int(m.config.WatchLimit / time.Minute),
		BlockSeconds:      int(m.config.DownDuration / time.Second),
		WatchingSince:     m.watchingSince,
		WatchedDuration:   watched,
		NextAction:        m.nextAction,
		LastError:         m.lastError,
	}
}

func (m *limiterMachine) resetLocked() {
	m.state = LimiterIdle
	m.watchingSince = time.Time{}
	m.watchedBefore = 0
	m.nextAction = time.Time{}
	m.lastError = ""
}

func (m *limiterMachine) watchedDurationLocked(now time.Time) time.Duration {
	watched := m.watchedBefore
	if !m.watchingSince.IsZero() {
		elapsed := now.Sub(m.watchingSince)
		if elapsed > 0 {
			watched += elapsed
		}
	}
	return watched
}

func (m *limiterMachine) PersistentState(now time.Time) persistedLimiterState {
	m.mu.Lock()
	defer m.mu.Unlock()
	phase := string(m.state)
	if m.state == LimiterInterventionDown {
		phase = string(LimiterInterventionUp)
	}
	return persistedLimiterState{
		SavedAt:             now.UTC().Format(time.RFC3339Nano),
		Enabled:             m.config.Enabled,
		WatchLimitSeconds:   int64(m.config.WatchLimit / time.Second),
		DownDurationSeconds: int64(m.config.DownDuration / time.Second),
		AccumulatedSeconds:  int64(m.watchedDurationLocked(now) / time.Second),
		Phase:               phase,
	}
}

func (m *limiterMachine) Restore(state persistedLimiterState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if state.WatchLimitSeconds <= 0 {
		return fmt.Errorf("persisted watch limit must be positive")
	}
	downDuration := m.config.DownDuration
	if state.DownDurationSeconds != 0 {
		downDuration = time.Duration(state.DownDurationSeconds) * time.Second
	}
	if err := validateBlockDuration(downDuration, m.config.Cycle); err != nil {
		return fmt.Errorf("persisted block duration: %w", err)
	}
	m.config.Enabled = state.Enabled
	m.config.WatchLimit = time.Duration(state.WatchLimitSeconds) * time.Second
	m.config.DownDuration = downDuration
	m.resetLocked()
	if !state.Enabled {
		return nil
	}
	if state.AccumulatedSeconds > 0 {
		m.watchedBefore = time.Duration(state.AccumulatedSeconds) * time.Second
	}
	switch LimiterState(state.Phase) {
	case LimiterWatching:
		m.state = LimiterWatching
	case LimiterInterventionUp, LimiterInterventionDown:
		m.state = LimiterInterventionUp
	default:
		m.resetLocked()
	}
	return nil
}

func validateBlockDuration(downDuration, cycle time.Duration) error {
	switch {
	case downDuration%time.Second != 0:
		return fmt.Errorf("block_seconds must be a whole number")
	case downDuration < minBlockSeconds*time.Second || downDuration > maxBlockSeconds*time.Second:
		return fmt.Errorf("block_seconds must be between %d and %d", minBlockSeconds, maxBlockSeconds)
	case downDuration >= cycle:
		return fmt.Errorf("block duration must be shorter than cycle")
	default:
		return nil
	}
}

func isWatching(status PortStatus) bool {
	return status.AdminUp && status.Carrier == "1"
}

type limiterRunner struct {
	machine    *limiterMachine
	controller PortController
	now        func() time.Time
	lastStatus PortStatus
	hasStatus  bool
	persister  *statePersister
}

func newLimiterRunner(config LimiterConfig, controller PortController) *limiterRunner {
	return &limiterRunner{
		machine:    newLimiterMachine(config),
		controller: controller,
		now:        time.Now,
	}
}

func (r *limiterRunner) Run(ctx context.Context) {
	pollInterval := r.machine.config.PollInterval
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	checkpoint := time.NewTicker(5 * time.Minute)
	defer checkpoint.Stop()
	r.poll(ctx)
	for {
		select {
		case <-ctx.Done():
			restoreCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if r.persister != nil {
				r.persister.Request(true)
				_ = r.persister.FlushNow(restoreCtx)
			}
			_ = r.machine.Stop(restoreCtx, r.controller.SetEnabled)
			return
		case <-ticker.C:
			r.poll(ctx)
		case <-checkpoint.C:
			if r.persister != nil {
				r.persister.Request(false)
			}
		default:
			if r.persister != nil {
				_ = r.persister.FlushDue(ctx)
			}
			snapshot := r.machine.Snapshot(r.now())
			if snapshot.NextAction.IsZero() {
				time.Sleep(minDuration(pollInterval/10, 100*time.Millisecond))
				continue
			}
			wait := time.Until(snapshot.NextAction)
			if wait > 0 {
				time.Sleep(minDuration(wait, 100*time.Millisecond))
				continue
			}
			if r.hasStatus {
				r.machine.Step(ctx, r.now(), r.lastStatus, r.controller.SetEnabled)
			}
		}
	}
}

func (r *limiterRunner) poll(ctx context.Context) {
	status, err := r.controller.Status(ctx)
	if err != nil {
		return
	}
	r.lastStatus = status
	r.hasStatus = true
	before := r.machine.Snapshot(r.now()).State
	r.machine.Step(ctx, r.now(), status, r.controller.SetEnabled)
	after := r.machine.Snapshot(r.now()).State
	if r.persister != nil {
		if before != LimiterInterventionUp && after == LimiterInterventionUp {
			r.persister.Request(true)
		}
		if before != LimiterIdle && after == LimiterIdle {
			r.persister.Request(false)
		}
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
