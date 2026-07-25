package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
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
	LimiterCooldown         LimiterState = "cooldown"

	minBlockSeconds = 1
	maxBlockSeconds = 26
)

type LimiterConfig struct {
	Enabled         bool
	PollInterval    time.Duration
	WatchLimit      time.Duration
	Cycle           time.Duration
	MinDownDuration time.Duration
	MaxDownDuration time.Duration
	Cooldown        time.Duration
	ActionRetry     time.Duration
}

func defaultLimiterConfig() LimiterConfig {
	return LimiterConfig{
		PollInterval:    30 * time.Second,
		WatchLimit:      20 * time.Minute,
		Cycle:           30 * time.Second,
		MinDownDuration: time.Second,
		MaxDownDuration: 26 * time.Second,
		Cooldown:        30 * time.Minute,
		ActionRetry:     time.Second,
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
	if cfg.MinDownDuration, err = durationEnv("IPTV_LIMITER_MIN_DOWN_DURATION", cfg.MinDownDuration); err != nil {
		return cfg, err
	}
	if cfg.MaxDownDuration, err = durationEnv("IPTV_LIMITER_MAX_DOWN_DURATION", cfg.MaxDownDuration); err != nil {
		return cfg, err
	}
	if cfg.Cooldown, err = durationEnv("IPTV_LIMITER_COOLDOWN_DURATION", cfg.Cooldown); err != nil {
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
	case validateBlockRange(c.MinDownDuration, c.MaxDownDuration, c.Cycle) != nil:
		return validateBlockRange(c.MinDownDuration, c.MaxDownDuration, c.Cycle)
	case c.Cooldown <= 0:
		return fmt.Errorf("cooldown duration must be positive")
	case c.ActionRetry <= 0:
		return fmt.Errorf("action retry must be positive")
	default:
		return nil
	}
}

type LimiterSnapshot struct {
	Enabled             bool          `json:"enabled"`
	State               LimiterState  `json:"state"`
	WatchLimitMinutes   int           `json:"watch_limit_minutes"`
	BlockMinSeconds     int           `json:"block_min_seconds"`
	BlockMaxSeconds     int           `json:"block_max_seconds"`
	CurrentBlockSeconds int           `json:"current_block_seconds,omitempty"`
	NoCarrierWindows    int           `json:"no_carrier_windows,omitempty"`
	WatchingSince       time.Time     `json:"watching_since,omitempty"`
	WatchedDuration     time.Duration `json:"watched_duration"`
	NextAction          time.Time     `json:"next_action,omitempty"`
	CooldownUntil       time.Time     `json:"cooldown_until,omitempty"`
	LastError           string        `json:"last_error,omitempty"`
	PersistencePending  bool          `json:"persistence_pending,omitempty"`
	PersistenceError    string        `json:"persistence_error,omitempty"`
}

type limiterMachine struct {
	mu               sync.Mutex
	config           LimiterConfig
	state            LimiterState
	watchingSince    time.Time
	watchedBefore    time.Duration
	nextAction       time.Time
	cooldownUntil    time.Time
	lastError        string
	currentBlock     time.Duration
	pendingDown      bool
	noCarrierWindows int
	randomBlock      func(time.Duration, time.Duration) (time.Duration, error)
}

func newLimiterMachine(config LimiterConfig) *limiterMachine {
	return &limiterMachine{
		config:      config,
		state:       LimiterIdle,
		randomBlock: cryptoRandomBlock,
	}
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
			m.enterCooldownLocked(ctx, now, setEnabled)
			return
		}
		if m.watchingSince.IsZero() {
			m.watchingSince = now
		}
		if m.watchedDurationLocked(now) >= m.config.WatchLimit {
			m.freezeWatchedLocked(now)
			m.beginDownLocked(ctx, now, setEnabled)
		}
	case LimiterInterventionUp:
		if m.nextAction.IsZero() || now.Before(m.nextAction) {
			return
		}
		if isWatching(status) {
			m.noCarrierWindows = 0
		} else {
			m.noCarrierWindows++
			if m.noCarrierWindows >= 2 {
				m.enterCooldownLocked(ctx, now, setEnabled)
				return
			}
		}
		m.currentBlock = 0
		m.beginDownLocked(ctx, now, setEnabled)
	case LimiterInterventionDown:
		if m.nextAction.IsZero() || now.Before(m.nextAction) {
			return
		}
		if m.pendingDown {
			m.performDownLocked(ctx, now, setEnabled)
			return
		}
		if err := setEnabled(ctx, true); err != nil {
			m.lastError = err.Error()
			m.nextAction = now.Add(m.config.ActionRetry)
			return
		}
		m.lastError = ""
		m.state = LimiterInterventionUp
		m.nextAction = now.Add(m.config.Cycle - m.currentBlock)
	case LimiterCooldown:
		if m.cooldownUntil.IsZero() {
			m.cooldownUntil = now.Add(m.config.Cooldown)
		}
		if !now.Before(m.cooldownUntil) {
			if err := setEnabled(ctx, true); err != nil {
				m.lastError = err.Error()
				m.nextAction = now.Add(m.config.ActionRetry)
				return
			}
			m.resetLocked()
			return
		}
		if status.AdminUp {
			if err := setEnabled(ctx, false); err != nil {
				m.lastError = err.Error()
				m.nextAction = now.Add(m.config.ActionRetry)
				return
			}
			m.lastError = ""
		}
		m.nextAction = m.cooldownUntil
	default:
		m.resetLocked()
	}
}

func (m *limiterMachine) beginDownLocked(
	ctx context.Context,
	now time.Time,
	setEnabled func(context.Context, bool) error,
) {
	if m.currentBlock == 0 {
		block, err := m.randomBlock(m.config.MinDownDuration, m.config.MaxDownDuration)
		if err != nil {
			m.lastError = err.Error()
			m.state = LimiterInterventionDown
			m.pendingDown = true
			m.nextAction = now.Add(m.config.ActionRetry)
			return
		}
		m.currentBlock = block
	}
	m.state = LimiterInterventionDown
	m.pendingDown = true
	m.performDownLocked(ctx, now, setEnabled)
}

func (m *limiterMachine) performDownLocked(
	ctx context.Context,
	now time.Time,
	setEnabled func(context.Context, bool) error,
) {
	if m.currentBlock == 0 {
		block, err := m.randomBlock(m.config.MinDownDuration, m.config.MaxDownDuration)
		if err != nil {
			m.lastError = err.Error()
			m.nextAction = now.Add(m.config.ActionRetry)
			return
		}
		m.currentBlock = block
	}
	if err := setEnabled(ctx, false); err != nil {
		m.lastError = err.Error()
		m.nextAction = now.Add(m.config.ActionRetry)
		return
	}
	m.lastError = ""
	m.pendingDown = false
	m.nextAction = now.Add(m.currentBlock)
}

func (m *limiterMachine) freezeWatchedLocked(now time.Time) {
	m.watchedBefore = m.watchedDurationLocked(now)
	m.watchingSince = time.Time{}
}

func (m *limiterMachine) enterCooldownLocked(
	ctx context.Context,
	now time.Time,
	setEnabled func(context.Context, bool) error,
) {
	if m.state == LimiterWatching {
		m.freezeWatchedLocked(now)
	}
	m.state = LimiterCooldown
	m.cooldownUntil = now.Add(m.config.Cooldown)
	if err := setEnabled(ctx, false); err != nil {
		m.lastError = err.Error()
		m.nextAction = now.Add(m.config.ActionRetry)
		return
	}
	m.lastError = ""
	m.nextAction = m.cooldownUntil
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
	minDownDuration time.Duration,
	maxDownDuration time.Duration,
	setEnabled func(context.Context, bool) error,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if watchLimit <= 0 {
		return fmt.Errorf("watch limit must be positive")
	}
	if err := validateBlockRange(minDownDuration, maxDownDuration, m.config.Cycle); err != nil {
		return err
	}
	wasEnabled := m.config.Enabled
	if wasEnabled && enabled {
		m.config.WatchLimit = watchLimit
		m.config.MinDownDuration = minDownDuration
		m.config.MaxDownDuration = maxDownDuration
		return nil
	}
	if !enabled && (m.state == LimiterInterventionDown || m.state == LimiterCooldown) {
		if err := setEnabled(ctx, true); err != nil {
			m.lastError = err.Error()
			return fmt.Errorf("restore port before updating limiter: %w", err)
		}
	}
	m.config.Enabled = enabled
	m.config.WatchLimit = watchLimit
	m.config.MinDownDuration = minDownDuration
	m.config.MaxDownDuration = maxDownDuration
	m.resetLocked()
	return nil
}

func (m *limiterMachine) StartIntervention(
	ctx context.Context,
	now time.Time,
	setEnabled func(context.Context, bool) error,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.config.Enabled {
		return fmt.Errorf("limiter is disabled")
	}
	if m.state != LimiterWatching {
		return fmt.Errorf("manual intervention is only available while watching")
	}
	m.freezeWatchedLocked(now)
	m.beginDownLocked(ctx, now, setEnabled)
	if m.pendingDown {
		return fmt.Errorf("start intervention: %s", m.lastError)
	}
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
		Enabled:             m.config.Enabled,
		State:               m.state,
		WatchLimitMinutes:   int(m.config.WatchLimit / time.Minute),
		BlockMinSeconds:     int(m.config.MinDownDuration / time.Second),
		BlockMaxSeconds:     int(m.config.MaxDownDuration / time.Second),
		CurrentBlockSeconds: int(m.currentBlock / time.Second),
		NoCarrierWindows:    m.noCarrierWindows,
		WatchingSince:       m.watchingSince,
		WatchedDuration:     watched,
		NextAction:          m.nextAction,
		CooldownUntil:       m.cooldownUntil,
		LastError:           m.lastError,
	}
}

func (m *limiterMachine) resetLocked() {
	m.state = LimiterIdle
	m.watchingSince = time.Time{}
	m.watchedBefore = 0
	m.nextAction = time.Time{}
	m.cooldownUntil = time.Time{}
	m.lastError = ""
	m.currentBlock = 0
	m.pendingDown = false
	m.noCarrierWindows = 0
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
	if m.state == LimiterInterventionUp || m.state == LimiterInterventionDown {
		phase = string(LimiterInterventionDown)
	}
	return persistedLimiterState{
		SavedAt:                now.UTC().Format(time.RFC3339Nano),
		Enabled:                m.config.Enabled,
		WatchLimitSeconds:      int64(m.config.WatchLimit / time.Second),
		MinDownDurationSeconds: int64(m.config.MinDownDuration / time.Second),
		MaxDownDurationSeconds: int64(m.config.MaxDownDuration / time.Second),
		AccumulatedSeconds:     int64(m.watchedDurationLocked(now) / time.Second),
		Phase:                  phase,
		CooldownUntil:          formatOptionalTime(m.cooldownUntil),
		NoCarrierWindows:       m.noCarrierWindows,
	}
}

func (m *limiterMachine) Restore(state persistedLimiterState, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if state.WatchLimitSeconds <= 0 {
		return fmt.Errorf("persisted watch limit must be positive")
	}
	minDownDuration := time.Duration(state.MinDownDurationSeconds) * time.Second
	maxDownDuration := time.Duration(state.MaxDownDurationSeconds) * time.Second
	if err := validateBlockRange(minDownDuration, maxDownDuration, m.config.Cycle); err != nil {
		return fmt.Errorf("persisted block range: %w", err)
	}
	m.config.Enabled = state.Enabled
	m.config.WatchLimit = time.Duration(state.WatchLimitSeconds) * time.Second
	m.config.MinDownDuration = minDownDuration
	m.config.MaxDownDuration = maxDownDuration
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
		m.state = LimiterInterventionDown
		m.pendingDown = true
		m.noCarrierWindows = state.NoCarrierWindows
		m.nextAction = now
	case LimiterCooldown:
		until, err := time.Parse(time.RFC3339Nano, state.CooldownUntil)
		if err != nil {
			return fmt.Errorf("persisted cooldown deadline: %w", err)
		}
		if !until.After(now) {
			m.resetLocked()
			return nil
		}
		m.state = LimiterCooldown
		m.cooldownUntil = until
		m.nextAction = until
	default:
		m.resetLocked()
	}
	return nil
}

func formatOptionalTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func validateBlockRange(minDownDuration, maxDownDuration, cycle time.Duration) error {
	switch {
	case minDownDuration%time.Second != 0 || maxDownDuration%time.Second != 0:
		return fmt.Errorf("block_min_seconds and block_max_seconds must be whole numbers")
	case minDownDuration < minBlockSeconds*time.Second || minDownDuration > maxBlockSeconds*time.Second,
		maxDownDuration < minBlockSeconds*time.Second || maxDownDuration > maxBlockSeconds*time.Second:
		return fmt.Errorf("block durations must be between %d and %d seconds", minBlockSeconds, maxBlockSeconds)
	case minDownDuration > maxDownDuration:
		return fmt.Errorf("block_min_seconds must not exceed block_max_seconds")
	case maxDownDuration >= cycle:
		return fmt.Errorf("block duration must be shorter than cycle")
	default:
		return nil
	}
}

func cryptoRandomBlock(minDuration, maxDuration time.Duration) (time.Duration, error) {
	minSeconds := int64(minDuration / time.Second)
	maxSeconds := int64(maxDuration / time.Second)
	span := maxSeconds - minSeconds + 1
	value, err := rand.Int(rand.Reader, big.NewInt(span))
	if err != nil {
		return 0, fmt.Errorf("choose random block duration: %w", err)
	}
	return time.Duration(minSeconds+value.Int64()) * time.Second, nil
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
			r.advanceDue(ctx, snapshot)
		}
	}
}

func (r *limiterRunner) advanceDue(ctx context.Context, snapshot LimiterSnapshot) {
	status := r.lastStatus
	if snapshot.State == LimiterInterventionUp {
		fresh, err := r.controller.Status(ctx)
		if err != nil {
			r.machine.recordActionError(r.now(), err)
			return
		}
		status = fresh
		r.lastStatus = fresh
		r.hasStatus = true
	}
	if !r.hasStatus {
		return
	}
	r.stepAndPersist(ctx, status)
}

func (r *limiterRunner) poll(ctx context.Context) {
	status, err := r.controller.Status(ctx)
	if err != nil {
		return
	}
	r.lastStatus = status
	r.hasStatus = true
	r.stepAndPersist(ctx, status)
}

func (r *limiterRunner) stepAndPersist(ctx context.Context, status PortStatus) {
	before := r.machine.Snapshot(r.now()).State
	r.machine.Step(ctx, r.now(), status, r.controller.SetEnabled)
	after := r.machine.Snapshot(r.now()).State
	if r.persister != nil {
		if before != LimiterInterventionDown && after == LimiterInterventionDown {
			r.persister.Request(true)
		}
		if before != LimiterCooldown && after == LimiterCooldown {
			r.persister.Request(true)
		}
		if before != LimiterIdle && after == LimiterIdle {
			r.persister.Request(false)
		}
	}
}

func (m *limiterMachine) recordActionError(now time.Time, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastError = err.Error()
	m.nextAction = now.Add(m.config.ActionRetry)
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
