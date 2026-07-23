package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeController struct { status PortStatus; set []bool; err error }
func (f *fakeController) Status(context.Context) (PortStatus, error) { return f.status, f.err }
func (f *fakeController) SetEnabled(_ context.Context, enabled bool) error { f.set = append(f.set, enabled); f.status.Enabled = enabled; f.status.AdminUp = enabled; return f.err }

func TestStatus(t *testing.T) {
	f := &fakeController{status: PortStatus{Interface: "eth1", AdminUp: true, Enabled: true}}
	a := &app{controller: f}; rr := httptest.NewRecorder(); a.status(rr, httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))
	if rr.Code != http.StatusOK { t.Fatalf("status=%d", rr.Code) }
	var got PortStatus; if err := json.NewDecoder(rr.Body).Decode(&got); err != nil { t.Fatal(err) }
	if got.Interface != "eth1" || !got.AdminUp { t.Fatalf("unexpected status: %+v", got) }
}

func TestSetPort(t *testing.T) {
	f := &fakeController{status: PortStatus{Interface: "eth1"}}; a := &app{controller: f}
	rr := httptest.NewRecorder(); a.port(rr, httptest.NewRequest(http.MethodPost, "/api/v1/port", strings.NewReader(`{"enabled":true}`)))
	if rr.Code != http.StatusOK || len(f.set) != 1 || !f.set[0] { t.Fatalf("code=%d calls=%v", rr.Code, f.set) }
}

func TestSetPortRejectsInvalidBody(t *testing.T) {
	a := &app{controller: &fakeController{}}; rr := httptest.NewRecorder()
	a.port(rr, httptest.NewRequest(http.MethodPost, "/api/v1/port", strings.NewReader(`{}`)))
	if rr.Code != http.StatusBadRequest { t.Fatalf("status=%d", rr.Code) }
}

func TestSetPortRejectsGet(t *testing.T) {
	a := &app{controller: &fakeController{}}; rr := httptest.NewRecorder()
	a.port(rr, httptest.NewRequest(http.MethodGet, "/api/v1/port", nil))
	if rr.Code != http.StatusMethodNotAllowed { t.Fatalf("status=%d", rr.Code) }
}
