package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed web/*
var webFS embed.FS

type PortStatus struct {
	Interface       string           `json:"interface"`
	Enabled         bool             `json:"enabled"`
	AdminUp         bool             `json:"admin_up"`
	OperState       string           `json:"oper_state"`
	Carrier         string           `json:"carrier"`
	CarrierSource   string           `json:"carrier_source"`
	LastChange      time.Time        `json:"last_change,omitempty"`
	LastError       string           `json:"last_error,omitempty"`
	CapabilityCheck string           `json:"capability_check"`
	Limiter         *LimiterSnapshot `json:"limiter,omitempty"`
}

type PortController interface {
	Status(context.Context) (PortStatus, error)
	SetEnabled(context.Context, bool) error
}

type ipController struct {
	iface, ip     string
	dbusSend      string
	sysClassNet   string
	real          bool
	now           func() time.Time
	commandOutput func(context.Context, string, ...string) ([]byte, error)
	mu            sync.Mutex
	last          PortStatus
}

func newIPController(iface, ip string, real bool) *ipController {
	return &ipController{
		iface:       iface,
		ip:          ip,
		dbusSend:    "/usr/bin/dbus-send",
		sysClassNet: "/sys/class/net",
		real:        real,
		now:         time.Now,
		commandOutput: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		},
		last: PortStatus{
			Interface:       iface,
			OperState:       "simulated",
			Carrier:         "simulated",
			CarrierSource:   "simulation",
			CapabilityCheck: "simulation",
		},
	}
}

func (c *ipController) Status(ctx context.Context) (PortStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.real {
		return c.last, nil
	}

	p := PortStatus{
		Interface:       c.iface,
		LastChange:      c.last.LastChange,
		LastError:       c.last.LastError,
		CapabilityCheck: "not_checked",
	}
	ifacePath := filepath.Join(c.sysClassNet, c.iface)
	if _, err := os.Stat(ifacePath); err != nil {
		return c.statusError(p, fmt.Errorf("interface %s: %w", c.iface, err))
	}

	flags, err := os.ReadFile(filepath.Join(ifacePath, "flags"))
	if err != nil {
		return c.statusError(p, fmt.Errorf("read flags: %w", err))
	}
	p.AdminUp, err = adminUpFromFlags(flags)
	if err != nil {
		return c.statusError(p, err)
	}

	state, err := os.ReadFile(filepath.Join(ifacePath, "operstate"))
	if err != nil {
		return c.statusError(p, fmt.Errorf("read operstate: %w", err))
	}
	p.OperState = strings.TrimSpace(string(state))

	carrier, err := c.readLAN2Status(ctx)
	if err != nil {
		return c.statusError(p, err)
	}
	p.Carrier = carrier
	p.CarrierSource = "dbus_lan2_status"
	p.Enabled = p.AdminUp
	p.CapabilityCheck = "interface_and_dbus_visible"
	c.last = p
	return p, nil
}

func (c *ipController) readLAN2Status(ctx context.Context) (string, error) {
	output, err := c.commandOutput(
		ctx,
		c.dbusSend,
		"--system",
		"--type=method_call",
		"--print-reply",
		"--reply-timeout=3000",
		"--dest=com.cuc.igd1",
		"/com/cuc/igd1/Info/Network",
		"org.freedesktop.DBus.Properties.Get",
		"string:com.cuc.igd1.NetworkInfo",
		"string:LAN2Status",
	)
	if err != nil {
		return "", fmt.Errorf("read LAN2Status via DBus: %w: %s", err, strings.TrimSpace(string(output)))
	}
	status, err := parseLAN2Status(output)
	if err != nil {
		return "", fmt.Errorf("read LAN2Status via DBus: %w", err)
	}
	return status, nil
}

func parseLAN2Status(output []byte) (string, error) {
	fields := strings.Fields(string(output))
	var status string
	for i := 0; i+1 < len(fields); i++ {
		if fields[i] != "byte" || (fields[i+1] != "0" && fields[i+1] != "1") {
			continue
		}
		if status != "" {
			return "", fmt.Errorf("ambiguous output %q", strings.TrimSpace(string(output)))
		}
		status = fields[i+1]
	}
	if status == "" {
		return "", fmt.Errorf("unexpected output %q", strings.TrimSpace(string(output)))
	}
	return status, nil
}

func (c *ipController) statusError(p PortStatus, err error) (PortStatus, error) {
	p.LastError = err.Error()
	c.last = p
	return p, err
}

func adminUpFromFlags(raw []byte) (bool, error) {
	flags, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 0, 64)
	if err != nil {
		return false, fmt.Errorf("parse interface flags: %w", err)
	}
	const iffUp = 0x1
	return flags&iffUp != 0, nil
}

func (c *ipController) SetEnabled(ctx context.Context, enabled bool) error {
	if !c.real {
		c.mu.Lock()
		c.last.Interface = c.iface
		c.last.Enabled = enabled
		c.last.AdminUp = enabled
		c.last.OperState = "simulated"
		c.last.Carrier = "simulated"
		c.last.CarrierSource = "simulation"
		c.last.LastChange = c.now()
		c.last.LastError = ""
		c.last.CapabilityCheck = "simulation"
		c.mu.Unlock()
		return nil
	}
	state := "down"
	if enabled {
		state = "up"
	}
	cmd := exec.CommandContext(ctx, c.ip, "link", "set", "dev", c.iface, state)
	if out, err := cmd.CombinedOutput(); err != nil {
		controlErr := fmt.Errorf("ip link %s: %w: %s", state, err, strings.TrimSpace(string(out)))
		c.mu.Lock()
		c.last.LastError = controlErr.Error()
		c.mu.Unlock()
		return controlErr
	}
	c.mu.Lock()
	c.last.LastChange = c.now()
	c.last.LastError = ""
	c.mu.Unlock()
	return nil
}

type app struct {
	controller PortController
	limiter    *limiterMachine
	persister  *statePersister
	now        func() time.Time
}

func (a *app) readStatus(ctx context.Context) PortStatus {
	s, err := a.controller.Status(ctx)
	if err != nil {
		s.LastError = err.Error()
	}
	if a.limiter != nil {
		now := time.Now()
		if a.now != nil {
			now = a.now()
		}
		snapshot := a.limiter.Snapshot(now)
		if a.persister != nil {
			snapshot.PersistencePending, snapshot.PersistenceError = a.persister.Status()
		}
		s.Limiter = &snapshot
	}
	return s
}
func (a *app) status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, a.readStatus(r.Context()))
}
func (a *app) port(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.Enabled == nil {
		http.Error(w, "enabled must be boolean", http.StatusBadRequest)
		return
	}
	if err := a.controller.SetEnabled(r.Context(), *req.Enabled); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if a.limiter != nil {
		a.limiter.ManualOverride()
	}
	if a.persister != nil {
		a.persister.Request(false)
	}
	writeJSON(w, http.StatusOK, a.readStatus(r.Context()))
}
func (a *app) limiterSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if a.limiter == nil {
		http.Error(w, "limiter is unavailable", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Enabled         *bool `json:"enabled"`
		MaxWatchMinutes *int  `json:"max_watch_minutes"`
		BlockSeconds    *int  `json:"block_seconds"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.Enabled == nil || req.MaxWatchMinutes == nil {
		http.Error(w, "enabled and max_watch_minutes are required", http.StatusBadRequest)
		return
	}
	if *req.MaxWatchMinutes < 1 || *req.MaxWatchMinutes > 24*60 {
		http.Error(w, "max_watch_minutes must be between 1 and 1440", http.StatusBadRequest)
		return
	}
	blockSeconds := a.limiter.Snapshot(a.nowTime()).BlockSeconds
	if req.BlockSeconds != nil {
		blockSeconds = *req.BlockSeconds
	}
	if blockSeconds < minBlockSeconds || blockSeconds > maxBlockSeconds {
		http.Error(w, "block_seconds must be between 1 and 25", http.StatusBadRequest)
		return
	}
	if err := a.limiter.UpdateSettings(
		r.Context(),
		*req.Enabled,
		time.Duration(*req.MaxWatchMinutes)*time.Minute,
		time.Duration(blockSeconds)*time.Second,
		a.controller.SetEnabled,
	); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if a.persister != nil {
		a.persister.Request(false)
	}
	writeJSON(w, http.StatusOK, a.readStatus(r.Context()))
}

func (a *app) nowTime() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

func restorePortAfterLoad(
	ctx context.Context,
	realControl bool,
	loadedPhase string,
	restoredState LimiterState,
	setEnabled func(context.Context, bool) error,
) error {
	if !realControl {
		return nil
	}
	switch {
	case restoredState == LimiterCooldown:
		return setEnabled(ctx, false)
	case loadedPhase == string(LimiterCooldown):
		return setEnabled(ctx, true)
	case restoredState == LimiterInterventionUp:
		return setEnabled(ctx, true)
	default:
		return nil
	}
}

func (a *app) intervene(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if a.limiter == nil {
		http.Error(w, "limiter is unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := a.limiter.StartIntervention(a.nowTime()); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if a.persister != nil {
		a.persister.Request(true)
	}
	writeJSON(w, http.StatusOK, a.readStatus(r.Context()))
}

func (a *app) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	s, err := a.controller.Status(r.Context())
	result := map[string]any{"ok": err == nil, "interface": s.Interface, "real_control": os.Getenv("IPTV_CONTROL_REAL") == "1"}
	if err != nil {
		result["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, result)
}
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func main() {
	realControl := os.Getenv("IPTV_CONTROL_REAL") == "1"
	if os.Geteuid() != 0 && realControl {
		log.Fatal("real control requires root")
	}
	iface := getenv("IPTV_CONTROL_INTERFACE", "eth1")
	ip := getenv("IPTV_CONTROL_IP", "/bin/ip")
	controller := newIPController(iface, ip, realControl)
	controller.dbusSend = getenv("IPTV_CONTROL_DBUS_SEND", "/usr/bin/dbus-send")
	if realControl {
		if _, err := controller.Status(context.Background()); err != nil {
			log.Printf("warning: initial interface check failed: %v", err)
		}
	}
	limiterConfig, err := limiterConfigFromEnv()
	if err != nil {
		log.Fatalf("invalid limiter configuration: %v", err)
	}
	limiter := newLimiterRunner(limiterConfig, controller)
	var persister *statePersister
	statePath := os.Getenv("IPTV_CONTROL_STATE_FILE")
	if statePath != "" {
		store := newStateStore(statePath)
		persister = newStatePersister(store, limiter.machine)
		loaded, loadErr := store.Load()
		switch {
		case loadErr == nil:
			if loaded.WatchLimitSeconds > 24*60*60 {
				log.Printf("warning: persisted watch limit is too large; ignoring state")
			} else if err := limiter.machine.Restore(loaded, time.Now()); err != nil {
				log.Printf("warning: restore limiter state failed: %v", err)
			} else {
				persister.SetLoaded(loaded)
				restoredState := limiter.machine.Snapshot(time.Now()).State
				if err := restorePortAfterLoad(
					context.Background(),
					realControl,
					loaded.Phase,
					restoredState,
					controller.SetEnabled,
				); err != nil {
					log.Printf("warning: reconcile LAN2 after limiter restore failed: %v", err)
				}
			}
		case !errors.Is(loadErr, os.ErrNotExist):
			log.Printf("warning: load limiter state failed: %v", loadErr)
		}
		limiter.persister = persister
	}
	a := &app{controller: controller, limiter: limiter.machine, persister: persister, now: time.Now}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/status", a.status)
	mux.HandleFunc("/api/v1/port", a.port)
	mux.HandleFunc("/api/v1/limiter", a.limiterSettings)
	mux.HandleFunc("/api/v1/intervene", a.intervene)
	mux.HandleFunc("/healthz", a.health)
	staticFS, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}
	mux.Handle("/", http.FileServer(http.FS(staticFS)))
	addr := getenv("IPTV_CONTROL_ADDR", ":8088")
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	limiterDone := make(chan struct{})
	go func() {
		defer close(limiterDone)
		limiter.Run(ctx)
	}()
	go func() { <-ctx.Done(); _ = srv.Shutdown(context.Background()) }()
	log.Printf("iptv-control listening on %s interface=%s real=%v limiter=%v", addr, iface, realControl, limiterConfig.Enabled)
	serveErr := srv.ListenAndServe()
	stop()
	select {
	case <-limiterDone:
	case <-time.After(6 * time.Second):
		log.Printf("warning: limiter shutdown timed out")
	}
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		log.Fatal(serveErr)
	}
}
func getenv(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
