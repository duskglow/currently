package rest

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nut-ng/nut-ng/drivers/simulator"
	"github.com/nut-ng/nut-ng/internal/core"
	"github.com/nut-ng/nut-ng/pkg/sdk"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// startCore runs a core with one simulated UPS named "sim".
func startCore(t *testing.T) *core.Core {
	t.Helper()
	c := core.New(quiet)
	if err := c.AddUPS(core.UPSSpec{Name: "sim", Driver: simulator.New(), Config: sdk.Config{"interval": "1h"}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = c.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(3 * time.Second)
	for {
		if u, _ := c.GetUPS("sim"); u.State == sdk.StateConnected {
			return c
		}
		if time.Now().After(deadline) {
			t.Fatal("simulator did not connect")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func do(t *testing.T, h http.Handler, method, path, body string, header ...string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func TestReadEndpoints(t *testing.T) {
	h := NewHandler(startCore(t), Options{Version: "test", Log: quiet})

	rec, body := do(t, h, "GET", "/api/v1/ups", "")
	if rec.Code != 200 || len(body["ups"].([]any)) != 1 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	rec, body = do(t, h, "GET", "/api/v1/ups/sim", "")
	if rec.Code != 200 || body["state"] != "connected" {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}
	rec, body = do(t, h, "GET", "/api/v1/ups/sim/vars/battery.charge", "")
	if rec.Code != 200 || body["value"] != "100" || body["unit"] != "%" {
		t.Fatalf("var: %d %s", rec.Code, rec.Body)
	}
	rec, body = do(t, h, "GET", "/api/v1/ups/smi", "")
	if rec.Code != 404 || !strings.Contains(body["error"].(string), `did you mean "sim"`) {
		t.Fatalf("typo: %d %s", rec.Code, rec.Body)
	}
	rec, _ = do(t, h, "GET", "/api/v1/ups/sim/commands", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "sim.power.fail") {
		t.Fatalf("commands: %d %s", rec.Code, rec.Body)
	}
	rec, _ = do(t, h, "GET", "/api/v1/ups/sim/history/battery.charge", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"samples"`) {
		t.Fatalf("history: %d %s", rec.Code, rec.Body)
	}
	rec, _ = do(t, h, "GET", "/api/v1/openapi.yaml", "")
	if rec.Code != 200 || !strings.HasPrefix(rec.Body.String(), "openapi:") {
		t.Fatalf("openapi: %d", rec.Code)
	}
	rec, _ = do(t, h, "GET", "/nope", "")
	if rec.Code != 404 {
		t.Fatalf("unknown path: %d", rec.Code)
	}
}

func TestMutationAuth(t *testing.T) {
	c := startCore(t)
	path := "/api/v1/ups/sim/commands/beeper.disable"

	open := NewHandler(c, Options{Log: quiet})
	if rec, _ := do(t, open, "POST", path, ""); rec.Code != 403 {
		t.Errorf("no token configured: %d, want 403", rec.Code)
	}

	withToken := NewHandler(c, Options{Token: "s3cret", Log: quiet})
	if rec, _ := do(t, withToken, "POST", path, ""); rec.Code != 401 {
		t.Errorf("missing token: %d, want 401", rec.Code)
	}
	if rec, _ := do(t, withToken, "POST", path, "", "Authorization", "Bearer wrong"); rec.Code != 401 {
		t.Errorf("wrong token: %d, want 401", rec.Code)
	}
	if rec, _ := do(t, withToken, "POST", path, "", "Authorization", "Bearer s3cret"); rec.Code != 200 {
		t.Errorf("right token: %d, want 200", rec.Code)
	}

	trusted := NewHandler(c, Options{Trusted: true, Log: quiet})
	if rec, _ := do(t, trusted, "POST", path, ""); rec.Code != 200 {
		t.Errorf("trusted: %d, want 200", rec.Code)
	}
	if rec, _ := do(t, trusted, "POST", "/api/v1/ups/sim/commands/load.off", ""); rec.Code != 404 {
		t.Errorf("unsupported command: %d, want 404", rec.Code)
	}
	if rec, _ := do(t, trusted, "PUT", "/api/v1/ups/sim/vars/ups.load", `{"value":"80"}`); rec.Code != 200 {
		t.Errorf("set var: %d, want 200", rec.Code)
	}
	if rec, _ := do(t, trusted, "PUT", "/api/v1/ups/sim/vars/ups.load", `{"value":"lots"}`); rec.Code != 400 {
		t.Errorf("bad value: %d, want 400", rec.Code)
	}
	if rec, _ := do(t, trusted, "PUT", "/api/v1/ups/sim/vars/ups.status", `{"value":"OB"}`); rec.Code != 400 {
		t.Errorf("read-only var: %d, want 400", rec.Code)
	}
}

func TestEventStream(t *testing.T) {
	c := startCore(t)
	srv := httptest.NewServer(NewHandler(c, Options{Trusted: true, Log: quiet}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/v1/events?notable=1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}

	if err := c.RunCommand(ctx, "sim", "sim.power.fail", ""); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() && !(seen["command"] && seen["on_battery"]) {
		if typ, ok := strings.CutPrefix(sc.Text(), "event: "); ok {
			if typ == "update" {
				t.Error("update event despite notable=1")
			}
			seen[typ] = true
		}
	}
	if !seen["command"] || !seen["on_battery"] {
		t.Errorf("saw %v, want command and on_battery", seen)
	}
}
