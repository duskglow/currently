package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	mochi "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"

	"github.com/nut-ng/nut-ng/drivers/simulator"
	"github.com/nut-ng/nut-ng/internal/core"
	"github.com/nut-ng/nut-ng/internal/plugins"
	"github.com/nut-ng/nut-ng/pkg/sdk"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// broker runs an in-process MQTT broker and returns its URL.
func broker(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	s := mochi.New(&mochi.Options{InlineClient: true, Logger: quiet})
	_ = s.AddHook(new(auth.AllowHook), nil)
	if err := s.AddListener(listeners.NewTCP(listeners.Config{ID: "t", Address: addr})); err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Serve() }()
	t.Cleanup(func() { s.Close() })
	return "tcp://" + addr
}

// watcher records the last message on every topic.
type watcher struct {
	mu   sync.Mutex
	msgs map[string][]string
	c    paho.Client
}

func watch(t *testing.T, url string) *watcher {
	t.Helper()
	w := &watcher{msgs: map[string][]string{}}
	w.c = paho.NewClient(paho.NewClientOptions().AddBroker(url).SetClientID("test-watcher"))
	if tok := w.c.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("watcher connect: %v", tok.Error())
	}
	w.c.Subscribe("#", 1, func(_ paho.Client, m paho.Message) {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.msgs[m.Topic()] = append(w.msgs[m.Topic()], string(m.Payload()))
	})
	t.Cleanup(func() { w.c.Disconnect(100) })
	return w
}

func (w *watcher) last(topic string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if l := w.msgs[topic]; len(l) > 0 {
		return l[len(l)-1]
	}
	return ""
}

func (w *watcher) all(topic string) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.msgs[topic]...)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func startCore(t *testing.T) *core.Core {
	t.Helper()
	c := core.New(quiet)
	if err := c.AddUPS(core.UPSSpec{Name: "rack", Driver: simulator.New(), Config: sdk.Config{"interval": "200ms"}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = c.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	waitFor(t, "the simulator", func() bool { u, _ := c.GetUPS("rack"); return u.State == sdk.StateConnected })
	return c
}

func run(t *testing.T, host sdk.Host, cfg sdk.Config) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- (Output{}).Run(ctx, host, cfg, quiet) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	})
	return cancel
}

func TestPublishesStateEventsAndDiscovery(t *testing.T) {
	url := broker(t)
	w := watch(t, url)
	c := startCore(t)
	stop := run(t, c, sdk.Config{"broker": url, "client_id": "nutng-test"})

	waitFor(t, "plugin status", func() bool { return w.last("nutng/status") == "online" })
	waitFor(t, "UPS availability", func() bool { return w.last("nutng/rack/available") == "online" })
	waitFor(t, "state", func() bool { return w.last("nutng/rack/state") != "" })
	var st struct {
		Status string            `json:"status"`
		Vars   map[string]string `json:"vars"`
	}
	if err := json.Unmarshal([]byte(w.last("nutng/rack/state")), &st); err != nil {
		t.Fatal(err)
	}
	if st.Status != "OL" || st.Vars["battery.charge"] != "100" {
		t.Errorf("state %+v", st)
	}

	waitFor(t, "discovery", func() bool { return w.last("homeassistant/sensor/nutng_rack/battery_charge/config") != "" })
	var disc map[string]any
	_ = json.Unmarshal([]byte(w.last("homeassistant/sensor/nutng_rack/battery_charge/config")), &disc)
	if disc["device_class"] != "battery" || disc["state_topic"] != "nutng/rack/state" || disc["unique_id"] != "nutng_rack_battery_charge" {
		t.Errorf("discovery %v", disc)
	}
	if !strings.Contains(w.last("homeassistant/binary_sensor/nutng_rack/power/config"), `"device_class":"power"`) {
		t.Errorf("binary sensor %s", w.last("homeassistant/binary_sensor/nutng_rack/power/config"))
	}

	if err := c.RunCommand(context.Background(), "rack", "sim.power.fail", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the on_battery event", func() bool {
		for _, e := range w.all("nutng/rack/event") {
			if strings.Contains(e, `"type":"on_battery"`) {
				return true
			}
		}
		return false
	})
	waitFor(t, "the OB state", func() bool { return strings.Contains(w.last("nutng/rack/state"), `"status":"OB`) })

	// Commands over MQTT, allowed for this output.
	w.c.Publish("nutng/rack/command/beeper.disable", 1, false, "")
	waitFor(t, "the command result", func() bool { return strings.Contains(w.last("nutng/rack/command/result"), `"ok":true`) })

	stop()
	waitFor(t, "offline on stop", func() bool { return w.last("nutng/status") == "offline" })
}

func TestCommandsRefusedWithoutAllowControl(t *testing.T) {
	url := broker(t)
	w := watch(t, url)
	c := startCore(t)
	run(t, plugins.ReadOnlyHost{Host: c}, sdk.Config{"broker": url, "client_id": "nutng-ro", "home_assistant": false})
	waitFor(t, "state", func() bool { return w.last("nutng/rack/state") != "" })
	w.c.Publish("nutng/rack/command/beeper.disable", 1, false, "")
	waitFor(t, "the refusal", func() bool { return strings.Contains(w.last("nutng/rack/command/result"), "allow_control") })
	u, _ := c.GetUPS("rack")
	if u.Value("ups.beeper.status") != "enabled" {
		t.Error("a read-only output changed the UPS")
	}
	if w.last("homeassistant/sensor/nutng_rack/battery_charge/config") != "" {
		t.Error("discovery published with home_assistant: false")
	}
}

func TestValidate(t *testing.T) {
	if err := (Output{}).ValidateConfig(sdk.Config{"broker": "tcp://h:1883"}); err != nil {
		t.Error(err)
	}
	for _, cfg := range []sdk.Config{{}, {"broker": "h:1883"}, {"broker": "tcp://h", "qos": 3},
		{"broker": "tcp://h", "topic_prefix": "a/#"}, {"broker": "tcp://h", "brokr": "x"}} {
		if err := (Output{}).ValidateConfig(cfg); err == nil {
			t.Errorf("%v accepted", cfg)
		}
	}
}
