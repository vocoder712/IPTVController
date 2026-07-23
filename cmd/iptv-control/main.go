package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"io/fs"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed web/*
var webFS embed.FS

type PortStatus struct {
	Interface string `json:"interface"`
	Enabled bool `json:"enabled"`
	AdminUp bool `json:"admin_up"`
	OperState string `json:"oper_state"`
	Carrier string `json:"carrier"`
	LastChange time.Time `json:"last_change,omitempty"`
	LastError string `json:"last_error,omitempty"`
	CapabilityCheck string `json:"capability_check"`
}

type PortController interface { Status(context.Context) (PortStatus, error); SetEnabled(context.Context, bool) error }

type ipController struct { iface, ip string; mu sync.Mutex; last PortStatus }

func (c *ipController) Status(ctx context.Context) (PortStatus, error) {
	c.mu.Lock(); defer c.mu.Unlock()
	p := PortStatus{Interface: c.iface, CapabilityCheck: "not_checked"}
	if _, err := os.Stat("/sys/class/net/" + c.iface); err != nil { p.LastError = err.Error(); return p, err }
	flags, err := os.ReadFile("/sys/class/net/" + c.iface + "/flags"); if err == nil { p.AdminUp = strings.TrimSpace(string(flags)) != "0x0" }
	state, _ := os.ReadFile("/sys/class/net/" + c.iface + "/operstate"); p.OperState = strings.TrimSpace(string(state))
	carrier, _ := os.ReadFile("/sys/class/net/" + c.iface + "/carrier"); p.Carrier = strings.TrimSpace(string(carrier))
	p.Enabled = p.AdminUp; p.CapabilityCheck = "interface_visible"
	c.last = p; return p, nil
}

func (c *ipController) SetEnabled(ctx context.Context, enabled bool) error {
	if os.Getenv("IPTV_CONTROL_REAL") != "1" { c.mu.Lock(); c.last.Enabled = enabled; c.last.AdminUp = enabled; c.last.LastChange = time.Now(); c.mu.Unlock(); return nil }
	state := "down"; if enabled { state = "up" }
	cmd := exec.CommandContext(ctx, c.ip, "link", "set", "dev", c.iface, state)
	if out, err := cmd.CombinedOutput(); err != nil { return fmt.Errorf("ip link %s: %w: %s", state, err, strings.TrimSpace(string(out))) }
	c.mu.Lock(); c.last.LastChange = time.Now(); c.mu.Unlock(); return nil
}

type app struct { controller PortController; mu sync.Mutex; lastErr string }

func (a *app) readStatus(ctx context.Context) PortStatus { s, err := a.controller.Status(ctx); if err != nil { a.mu.Lock(); a.lastErr = err.Error(); a.mu.Unlock() }; return s }
func (a *app) status(w http.ResponseWriter, r *http.Request) { if r.Method != http.MethodGet { w.WriteHeader(http.StatusMethodNotAllowed); return }; writeJSON(w, http.StatusOK, a.readStatus(r.Context())) }
func (a *app) port(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost { w.WriteHeader(http.StatusMethodNotAllowed); return }
	var req struct{ Enabled *bool `json:"enabled"` }; if json.NewDecoder(r.Body).Decode(&req) != nil || req.Enabled == nil { http.Error(w, "enabled must be boolean", http.StatusBadRequest); return }
	if err := a.controller.SetEnabled(r.Context(), *req.Enabled); err != nil { a.mu.Lock(); a.lastErr = err.Error(); a.mu.Unlock(); http.Error(w, err.Error(), http.StatusBadGateway); return }
	writeJSON(w, http.StatusOK, a.readStatus(r.Context()))
}
func (a *app) health(w http.ResponseWriter, r *http.Request) { if r.Method != http.MethodGet { w.WriteHeader(http.StatusMethodNotAllowed); return }; s, err := a.controller.Status(r.Context()); result := map[string]any{"ok": err == nil, "interface": s.Interface, "real_control": os.Getenv("IPTV_CONTROL_REAL") == "1"}; if err != nil { result["error"] = err.Error() }; writeJSON(w, http.StatusOK, result) }
func writeJSON(w http.ResponseWriter, code int, v any) { w.Header().Set("Content-Type", "application/json; charset=utf-8"); w.WriteHeader(code); _ = json.NewEncoder(w).Encode(v) }

func main() {
	if os.Geteuid() != 0 && os.Getenv("IPTV_CONTROL_REAL") == "1" { log.Fatal("real control requires root") }
	iface := getenv("IPTV_CONTROL_INTERFACE", "eth1"); ip := getenv("IPTV_CONTROL_IP", "/bin/ip")
	if _, err := os.Stat("/sys/class/net/" + iface); err != nil && os.Getenv("IPTV_CONTROL_REAL") == "1" { log.Printf("warning: %v", err) }
	a := &app{controller: &ipController{iface: iface, ip: ip}}
	mux := http.NewServeMux(); mux.HandleFunc("/api/v1/status", a.status); mux.HandleFunc("/api/v1/port", a.port); mux.HandleFunc("/healthz", a.health)
	staticFS, err := fs.Sub(webFS, "web"); if err != nil { log.Fatal(err) }
	mux.Handle("/", http.FileServer(http.FS(staticFS)))
	addr := getenv("IPTV_CONTROL_ADDR", ":8088"); srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM); defer stop()
	go func() { <-ctx.Done(); _ = srv.Shutdown(context.Background()) }()
	log.Printf("iptv-control listening on %s interface=%s real=%v", addr, iface, os.Getenv("IPTV_CONTROL_REAL") == "1")
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) { log.Fatal(err) }
}
func getenv(k, fallback string) string { if v := os.Getenv(k); v != "" { return v }; return fallback }
