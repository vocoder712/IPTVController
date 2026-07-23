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
	Interface       string    `json:"interface"`
	Enabled         bool      `json:"enabled"`
	AdminUp         bool      `json:"admin_up"`
	OperState       string    `json:"oper_state"`
	Carrier         string    `json:"carrier"`
	LastChange      time.Time `json:"last_change,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
	CapabilityCheck string    `json:"capability_check"`
}

type PortController interface {
	Status(context.Context) (PortStatus, error)
	SetEnabled(context.Context, bool) error
}

type ipController struct {
	iface, ip   string
	sysClassNet string
	real        bool
	now         func() time.Time
	mu          sync.Mutex
	last        PortStatus
}

func newIPController(iface, ip string, real bool) *ipController {
	return &ipController{
		iface:       iface,
		ip:          ip,
		sysClassNet: "/sys/class/net",
		real:        real,
		now:         time.Now,
		last: PortStatus{
			Interface:       iface,
			OperState:       "simulated",
			Carrier:         "simulated",
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

	carrier, err := os.ReadFile(filepath.Join(ifacePath, "carrier"))
	if err != nil {
		return c.statusError(p, fmt.Errorf("read carrier: %w", err))
	}
	p.Carrier = strings.TrimSpace(string(carrier))
	p.Enabled = p.AdminUp
	p.CapabilityCheck = "interface_visible"
	c.last = p
	return p, nil
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
}

func (a *app) readStatus(ctx context.Context) PortStatus {
	s, err := a.controller.Status(ctx)
	if err != nil {
		s.LastError = err.Error()
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
	if realControl {
		if _, err := controller.Status(context.Background()); err != nil {
			log.Printf("warning: initial interface check failed: %v", err)
		}
	}
	a := &app{controller: controller}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/status", a.status)
	mux.HandleFunc("/api/v1/port", a.port)
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
	go func() { <-ctx.Done(); _ = srv.Shutdown(context.Background()) }()
	log.Printf("iptv-control listening on %s interface=%s real=%v", addr, iface, realControl)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
func getenv(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
