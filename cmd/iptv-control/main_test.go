package main

import (
	"context"
	"encoding/json"
	"errors"
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
	writeTestFile(t, filepath.Join(ifacePath, "carrier"), "0\n")

	lastChange := time.Date(2026, 7, 23, 20, 1, 0, 0, time.UTC)
	c := newIPController("eth1", "/bin/ip", true)
	c.sysClassNet = root
	c.last.LastChange = lastChange

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
	if got.LastChange != lastChange {
		t.Fatalf("last_change=%v, want %v", got.LastChange, lastChange)
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
