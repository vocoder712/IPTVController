package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeController struct {
	status PortStatus
	set    []bool
	err    error
}

func (f *fakeController) Status(context.Context) (PortStatus, error) { return f.status, f.err }
func (f *fakeController) SetEnabled(_ context.Context, enabled bool) error {
	f.set = append(f.set, enabled)
	f.status.Enabled = enabled
	f.status.AdminUp = enabled
	return f.err
}

func TestStatus(t *testing.T) {
	f := &fakeController{status: PortStatus{Interface: "eth1", AdminUp: true, Enabled: true}}
	a := &app{controller: f}
	rr := httptest.NewRecorder()
	a.status(rr, httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	var got PortStatus
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Interface != "eth1" || !got.AdminUp {
		t.Fatalf("unexpected status: %+v", got)
	}
}

func TestSetPort(t *testing.T) {
	f := &fakeController{status: PortStatus{Interface: "eth1"}}
	a := &app{controller: f}
	rr := httptest.NewRecorder()
	a.port(rr, httptest.NewRequest(http.MethodPost, "/api/v1/port", strings.NewReader(`{"enabled":true}`)))
	if rr.Code != http.StatusOK || len(f.set) != 1 || !f.set[0] {
		t.Fatalf("code=%d calls=%v", rr.Code, f.set)
	}
}

func TestSetPortRejectsInvalidBody(t *testing.T) {
	a := &app{controller: &fakeController{}}
	rr := httptest.NewRecorder()
	a.port(rr, httptest.NewRequest(http.MethodPost, "/api/v1/port", strings.NewReader(`{}`)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", rr.Code)
	}
}

func TestSetPortRejectsGet(t *testing.T) {
	a := &app{controller: &fakeController{}}
	rr := httptest.NewRecorder()
	a.port(rr, httptest.NewRequest(http.MethodGet, "/api/v1/port", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d", rr.Code)
	}
}

func TestManualPortControlResetsSessionButPreservesLimiterSetting(t *testing.T) {
	f := &fakeController{status: watchingStatus()}
	cfg := defaultLimiterConfig()
	cfg.Enabled = true
	machine := newLimiterMachine(cfg)
	machine.Step(context.Background(), time.Now(), watchingStatus(), f.SetEnabled)
	a := &app{controller: f, limiter: machine}

	rr := httptest.NewRecorder()
	a.port(rr, httptest.NewRequest(http.MethodPost, "/api/v1/port", strings.NewReader(`{"enabled":false}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	snapshot := machine.Snapshot(time.Now())
	if !snapshot.Enabled || snapshot.State != LimiterIdle {
		t.Fatalf("manual close changed limiter configuration: %+v", snapshot)
	}

	rr = httptest.NewRecorder()
	a.port(rr, httptest.NewRequest(http.MethodPost, "/api/v1/port", strings.NewReader(`{"enabled":true}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	snapshot = machine.Snapshot(time.Now())
	if !snapshot.Enabled || snapshot.State != LimiterIdle {
		t.Fatalf("manual open did not preserve fresh enabled session: %+v", snapshot)
	}
}

func TestManualOpenDoesNotEnableDisabledLimiter(t *testing.T) {
	f := &fakeController{}
	machine := newLimiterMachine(defaultLimiterConfig())
	a := &app{controller: f, limiter: machine}
	rr := httptest.NewRecorder()
	a.port(rr, httptest.NewRequest(http.MethodPost, "/api/v1/port", strings.NewReader(`{"enabled":true}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	if machine.Snapshot(time.Now()).Enabled {
		t.Fatal("manual open enabled the limiter")
	}
}

func TestUpdateLimiterSettings(t *testing.T) {
	f := &fakeController{status: PortStatus{Interface: "eth1", AdminUp: true, Enabled: true}}
	machine := newLimiterMachine(defaultLimiterConfig())
	a := &app{controller: f, limiter: machine}
	rr := httptest.NewRecorder()
	a.limiterSettings(rr, httptest.NewRequest(
		http.MethodPost,
		"/api/v1/limiter",
		strings.NewReader(`{"enabled":true,"max_watch_minutes":35,"block_seconds":14}`),
	))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	snapshot := machine.Snapshot(time.Now())
	if !snapshot.Enabled || snapshot.WatchLimitMinutes != 35 || snapshot.BlockSeconds != 14 {
		t.Fatalf("unexpected limiter settings: %+v", snapshot)
	}
}

func TestUpdateLimiterSettingsKeepsBlockSecondsForOldClient(t *testing.T) {
	f := &fakeController{}
	machine := newLimiterMachine(defaultLimiterConfig())
	a := &app{controller: f, limiter: machine}
	rr := httptest.NewRecorder()
	a.limiterSettings(rr, httptest.NewRequest(
		http.MethodPost,
		"/api/v1/limiter",
		strings.NewReader(`{"enabled":true,"max_watch_minutes":30}`),
	))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := machine.Snapshot(time.Now()).BlockSeconds; got != 6 {
		t.Fatalf("block_seconds=%d, want 6", got)
	}
}

func TestUpdateLimiterSettingsRejectsInvalidMinutes(t *testing.T) {
	a := &app{controller: &fakeController{}, limiter: newLimiterMachine(defaultLimiterConfig())}
	rr := httptest.NewRecorder()
	a.limiterSettings(rr, httptest.NewRequest(
		http.MethodPost,
		"/api/v1/limiter",
		strings.NewReader(`{"enabled":true,"max_watch_minutes":0}`),
	))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", rr.Code)
	}
}

func TestUpdateLimiterSettingsRejectsInvalidBlockSeconds(t *testing.T) {
	for _, seconds := range []int{0, 26} {
		a := &app{controller: &fakeController{}, limiter: newLimiterMachine(defaultLimiterConfig())}
		body := fmt.Sprintf(`{"enabled":true,"max_watch_minutes":20,"block_seconds":%d}`, seconds)
		rr := httptest.NewRecorder()
		a.limiterSettings(rr, httptest.NewRequest(http.MethodPost, "/api/v1/limiter", strings.NewReader(body)))
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("block_seconds=%d status=%d body=%s", seconds, rr.Code, rr.Body.String())
		}
	}
}

func TestEmbeddedPageContainsLimiterControls(t *testing.T) {
	page, err := webFS.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	for _, required := range []string{
		`id="limiter-enabled"`,
		`id="max-watch-minutes"`,
		`id="block-seconds"`,
		`id="save-limiter"`,
		`/api/v1/limiter`,
		`min="1" max="25"`,
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("page missing %q", required)
		}
	}
}

func TestAdminUpFromFlags(t *testing.T) {
	tests := []struct {
		name  string
		flags string
		want  bool
	}{
		{name: "up with other flags", flags: "0x1003\n", want: true},
		{name: "down with broadcast and multicast", flags: "0x1002\n", want: false},
		{name: "zero", flags: "0x0\n", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := adminUpFromFlags([]byte(tt.flags))
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("adminUpFromFlags(%q)=%v, want %v", tt.flags, got, tt.want)
			}
		})
	}
}

func TestAdminUpFromFlagsRejectsInvalidValue(t *testing.T) {
	if _, err := adminUpFromFlags([]byte("invalid")); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestSimulatedControllerRetainsState(t *testing.T) {
	fixed := time.Date(2026, 7, 23, 20, 0, 0, 0, time.UTC)
	c := newIPController("eth1", "/bin/ip", false)
	c.now = func() time.Time { return fixed }

	if err := c.SetEnabled(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	got, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || !got.AdminUp {
		t.Fatalf("unexpected simulated status: %+v", got)
	}
	if got.LastChange != fixed {
		t.Fatalf("last_change=%v, want %v", got.LastChange, fixed)
	}
	if got.CapabilityCheck != "simulation" || got.OperState != "simulated" {
		t.Fatalf("unexpected simulation metadata: %+v", got)
	}
}

func TestRealControllerStatusParsesIFFUpAndRetainsLastChange(t *testing.T) {
	root := t.TempDir()
	ifacePath := filepath.Join(root, "eth1")
	if err := os.Mkdir(ifacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(ifacePath, "flags"), "0x1002\n")
	writeTestFile(t, filepath.Join(ifacePath, "operstate"), "down\n")
	// A contradictory sysfs value proves that real mode no longer reads it.
	writeTestFile(t, filepath.Join(ifacePath, "carrier"), "1\n")

	lastChange := time.Date(2026, 7, 23, 20, 1, 0, 0, time.UTC)
	c := newIPController("eth1", "/bin/ip", true)
	c.sysClassNet = root
	c.last.LastChange = lastChange
	var commandName string
	var commandArgs []string
	c.commandOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
		commandName = name
		commandArgs = append([]string(nil), args...)
		return []byte("method return\n   variant       byte 0\n"), nil
	}

	got, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.AdminUp || got.Enabled {
		t.Fatalf("DOWN flags were reported as UP: %+v", got)
	}
	if got.OperState != "down" || got.Carrier != "0" {
		t.Fatalf("unexpected link details: %+v", got)
	}
	if got.CarrierSource != "dbus_lan2_status" ||
		got.CapabilityCheck != "interface_and_dbus_visible" {
		t.Fatalf("unexpected carrier metadata: %+v", got)
	}
	if got.LastChange != lastChange {
		t.Fatalf("last_change=%v, want %v", got.LastChange, lastChange)
	}
	if commandName != "/usr/bin/dbus-send" ||
		!containsString(commandArgs, "string:LAN2Status") ||
		!containsString(commandArgs, "--reply-timeout=3000") {
		t.Fatalf("unexpected DBus command: %q %q", commandName, commandArgs)
	}
}

func TestParseLAN2Status(t *testing.T) {
	for _, tt := range []struct {
		name   string
		output string
		want   string
	}{
		{name: "link up", output: "method return\n variant byte 1\n", want: "1"},
		{name: "link down", output: "method return\n variant byte 0\n", want: "0"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseLAN2Status([]byte(tt.output))
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("status=%q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseLAN2StatusRejectsUnexpectedOutput(t *testing.T) {
	for _, output := range []string{
		"variant byte 2",
		"variant string 1",
		"variant byte 0 byte 1",
		"",
	} {
		if _, err := parseLAN2Status([]byte(output)); err == nil {
			t.Fatalf("expected error for %q", output)
		}
	}
}

func TestRealControllerStatusReportsDBusFailureWithoutSysfsFallback(t *testing.T) {
	root := t.TempDir()
	ifacePath := filepath.Join(root, "eth1")
	if err := os.Mkdir(ifacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(ifacePath, "flags"), "0x1003\n")
	writeTestFile(t, filepath.Join(ifacePath, "operstate"), "unknown\n")
	writeTestFile(t, filepath.Join(ifacePath, "carrier"), "1\n")

	c := newIPController("eth1", "/bin/ip", true)
	c.sysClassNet = root
	c.commandOutput = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("connection failed"), errors.New("exit status 1")
	}

	got, err := c.Status(context.Background())
	if err == nil || !strings.Contains(err.Error(), "read LAN2Status via DBus") {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Carrier != "" {
		t.Fatalf("DBus failure fell back to carrier=%q", got.Carrier)
	}
	if !strings.Contains(got.LastError, "connection failed") {
		t.Fatalf("missing command output: %+v", got)
	}
}

func TestRealControllerStatusReportsReadError(t *testing.T) {
	c := newIPController("missing", "/bin/ip", true)
	c.sysClassNet = t.TempDir()

	got, err := c.Status(context.Background())
	if err == nil {
		t.Fatal("expected missing interface error")
	}
	if got.LastError == "" {
		t.Fatalf("missing last_error: %+v", got)
	}
}

func TestStatusIncludesControllerError(t *testing.T) {
	a := &app{controller: &fakeController{err: errors.New("status failed")}}
	rr := httptest.NewRecorder()
	a.status(rr, httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	var got PortStatus
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.LastError != "status failed" {
		t.Fatalf("last_error=%q", got.LastError)
	}
}

func TestSetPortReturnsControllerError(t *testing.T) {
	a := &app{controller: &fakeController{err: errors.New("set failed")}}
	rr := httptest.NewRecorder()
	a.port(rr, httptest.NewRequest(http.MethodPost, "/api/v1/port", strings.NewReader(`{"enabled":true}`)))

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d", rr.Code)
	}
}

func TestHealthReportsControllerError(t *testing.T) {
	t.Setenv("IPTV_CONTROL_REAL", "1")
	a := &app{controller: &fakeController{
		status: PortStatus{Interface: "eth1"},
		err:    errors.New("status failed"),
	}}
	rr := httptest.NewRecorder()
	a.health(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	var got map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["ok"] != false || got["error"] != "status failed" || got["real_control"] != true {
		t.Fatalf("unexpected health response: %#v", got)
	}
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
