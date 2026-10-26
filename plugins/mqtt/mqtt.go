// Command nutng-output-mqtt is the MQTT output plugin: it publishes UPS
// state and events to an MQTT broker, with Home Assistant discovery, and
// accepts commands over MQTT when the output has allow_control.
//
// Topics, with the default prefix "nutng":
//
//	nutng/status                    online/offline (this plugin; retained, last will)
//	nutng/<ups>/available           online/offline (data from the UPS; retained)
//	nutng/<ups>/state               JSON: status, state and every variable (retained)
//	nutng/<ups>/event               JSON: each notable event
//	nutng/<ups>/command/<command>   publish here to run a command; the payload is its parameter
//	nutng/<ups>/command/result      JSON: the outcome of each command
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"

	"github.com/nut-ng/nut-ng/pkg/sdk"
)

// Version of the plugin.
const Version = "0.1.0"

// Output is the MQTT output.
type Output struct{}

// Info implements sdk.Output.
func (Output) Info() sdk.Info {
	return sdk.Info{Name: "mqtt", Kind: sdk.KindOutput, Version: Version,
		Description: "Publishes UPS state and events to MQTT, with Home Assistant discovery"}
}

// Options configure the output.
type Options struct {
	// Broker is the broker URL: tcp://, ssl:// or ws://host:port.
	Broker      string `yaml:"broker"`
	Username    string `yaml:"username"`
	Password    string `yaml:"password"`
	PasswordEnv string `yaml:"password_env"`
	ClientID    string `yaml:"client_id"`
	TopicPrefix string `yaml:"topic_prefix"`
	QoS         int    `yaml:"qos"`
	// HomeAssistant publishes discovery configs under DiscoveryPrefix.
	HomeAssistant   *bool  `yaml:"home_assistant"`
	DiscoveryPrefix string `yaml:"discovery_prefix"`
	// MinInterval limits how often each UPS's state is published.
	MinInterval time.Duration `yaml:"min_interval"`
	TLSInsecure bool          `yaml:"tls_insecure"`
}

var topicRE = regexp.MustCompile(`^[^#+]+$`)

func parse(cfg sdk.Config) (Options, error) {
	host, _ := os.Hostname()
	o := Options{TopicPrefix: "nutng", QoS: 1, DiscoveryPrefix: "homeassistant", MinInterval: time.Second,
		ClientID: "nutng-" + sanitize(host)}
	if err := cfg.Decode(&o); err != nil {
		return o, err
	}
	var errs []error
	if o.Broker == "" {
		errs = append(errs, errors.New(`broker is required, such as "tcp://192.168.1.5:1883"`))
	} else if !strings.Contains(o.Broker, "://") {
		errs = append(errs, fmt.Errorf(`broker %q needs a scheme: "tcp://%s"`, o.Broker, o.Broker))
	}
	if o.QoS < 0 || o.QoS > 2 {
		errs = append(errs, errors.New("qos must be 0, 1 or 2"))
	}
	if !topicRE.MatchString(o.TopicPrefix) || !topicRE.MatchString(o.DiscoveryPrefix) {
		errs = append(errs, errors.New("topic_prefix and discovery_prefix may not be empty or contain + or #"))
	}
	if o.Password != "" && o.PasswordEnv != "" {
		errs = append(errs, errors.New("set either password or password_env, not both"))
	}
	if o.PasswordEnv != "" && os.Getenv(o.PasswordEnv) == "" {
		errs = append(errs, fmt.Errorf("password_env names %s, which is not set", o.PasswordEnv))
	}
	if o.MinInterval < 0 {
		errs = append(errs, errors.New("min_interval must not be negative"))
	}
	return o, errors.Join(errs...)
}

// ValidateConfig implements sdk.Output.
func (Output) ValidateConfig(cfg sdk.Config) error {
	_, err := parse(cfg)
	return err
}

var unsafeRE = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

func sanitize(s string) string { return strings.Trim(unsafeRE.ReplaceAllString(s, "_"), "_") }

// publisher holds the state of one run.
type publisher struct {
	o      Options
	host   sdk.Host
	client paho.Client
	log    *slog.Logger

	mu         sync.Mutex
	dirty      map[string]bool
	lastPub    map[string]time.Time
	discovered map[string]bool // "ups/var" discovery configs sent
	available  map[string]string
}

// Run implements sdk.Output.
func (Output) Run(ctx context.Context, host sdk.Host, cfg sdk.Config, log *slog.Logger) error {
	o, err := parse(cfg)
	if err != nil {
		return err
	}
	p := &publisher{o: o, host: host, log: log, dirty: map[string]bool{}, lastPub: map[string]time.Time{},
		discovered: map[string]bool{}, available: map[string]string{}}

	opts := paho.NewClientOptions().AddBroker(o.Broker).SetClientID(o.ClientID).
		SetAutoReconnect(true).SetConnectRetry(true).SetConnectRetryInterval(5*time.Second).
		SetWill(p.topic("status"), "offline", byte(o.QoS), true).
		SetOnConnectHandler(func(c paho.Client) { p.onConnect() }).
		SetConnectionLostHandler(func(_ paho.Client, err error) { log.Warn("MQTT connection lost", "error", err) })
	if o.Username != "" {
		opts.SetUsername(o.Username)
	}
	if pw := o.Password; pw != "" || o.PasswordEnv != "" {
		if o.PasswordEnv != "" {
			pw = os.Getenv(o.PasswordEnv)
		}
		opts.SetPassword(pw)
	}
	if o.TLSInsecure {
		opts.SetTLSConfig(insecureTLS())
	}
	p.client = paho.NewClient(opts)
	tok := p.client.Connect()
	// With connect retry, Connect returns at once and keeps trying.
	tok.WaitTimeout(10 * time.Second)
	if err := tok.Error(); err != nil {
		return fmt.Errorf("connect to MQTT broker %s: %w", o.Broker, err)
	}
	defer func() {
		p.publish(p.topic("status"), "offline", true)
		p.client.Disconnect(500)
	}()

	events := host.Subscribe(ctx, 512)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	refresh := time.NewTicker(time.Minute)
	defer refresh.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case e, ok := <-events:
			if !ok {
				return errors.New("lost the event stream from nutng")
			}
			p.handle(e)
		case <-tick.C:
			p.flush(false)
		case <-refresh.C:
			p.flush(true)
		}
	}
}

func (p *publisher) topic(parts ...string) string {
	return p.o.TopicPrefix + "/" + strings.Join(parts, "/")
}

func (p *publisher) publish(topic string, payload any, retain bool) {
	var data []byte
	switch v := payload.(type) {
	case string:
		data = []byte(v)
	case []byte:
		data = v
	default:
		var err error
		if data, err = json.Marshal(v); err != nil {
			p.log.Warn("MQTT payload", "error", err)
			return
		}
	}
	if !p.client.IsConnectionOpen() {
		return // published again in full on reconnect
	}
	p.client.Publish(topic, byte(p.o.QoS), retain, data)
}

// onConnect runs on every (re)connection: announce, subscribe to
// commands, and publish everything.
func (p *publisher) onConnect() {
	p.log.Info("connected to MQTT broker", "broker", p.o.Broker)
	p.publish(p.topic("status"), "online", true)
	p.client.Subscribe(p.topic("+", "command", "+"), byte(p.o.QoS), p.onCommand)
	p.mu.Lock()
	p.discovered = map[string]bool{}
	p.available = map[string]string{}
	p.mu.Unlock()
	p.flush(true)
}

func (p *publisher) handle(e sdk.Event) {
	if e.UPS == "" {
		return
	}
	p.mu.Lock()
	p.dirty[e.UPS] = true
	p.mu.Unlock()
	if e.Type.Notable() {
		p.publish(p.topic(sanitize(e.UPS), "event"), map[string]any{
			"type": e.Type, "message": e.Message, "time": e.Time.UTC().Format(time.RFC3339),
		}, false)
	}
}

// flush publishes the state of UPSes that changed (or all, with force).
func (p *publisher) flush(force bool) {
	now := time.Now()
	for _, u := range p.host.ListUPS() {
		p.mu.Lock()
		due := force || (p.dirty[u.Name] && now.Sub(p.lastPub[u.Name]) >= p.o.MinInterval)
		if due {
			p.dirty[u.Name] = false
			p.lastPub[u.Name] = now
		}
		p.mu.Unlock()
		if !due {
			continue
		}
		name := sanitize(u.Name)
		avail := "offline"
		if u.State == sdk.StateConnected {
			avail = "online"
		}
		p.mu.Lock()
		changed := p.available[u.Name] != avail
		p.available[u.Name] = avail
		p.mu.Unlock()
		if changed || force {
			p.publish(p.topic(name, "available"), avail, true)
		}
		vars := map[string]string{}
		for k, v := range u.Vars {
			vars[k] = v.Value
		}
		p.publish(p.topic(name, "state"), map[string]any{
			"status":      u.Value("ups.status"),
			"status_text": u.Status().Describe(),
			"state":       u.State,
			"updated":     u.LastUpdate.UTC().Format(time.RFC3339),
			"vars":        vars,
		}, true)
		if p.homeAssistant() {
			p.discover(u)
		}
	}
}

func (p *publisher) homeAssistant() bool { return p.o.HomeAssistant == nil || *p.o.HomeAssistant }

// sensor describes a Home Assistant sensor for a variable.
type sensor struct {
	name, deviceClass, unit, stateClass string
}

var sensors = map[string]sensor{
	"battery.charge":        {"Battery charge", "battery", "%", "measurement"},
	"battery.runtime":       {"Battery runtime", "duration", "s", "measurement"},
	"battery.voltage":       {"Battery voltage", "voltage", "V", "measurement"},
	"input.voltage":         {"Input voltage", "voltage", "V", "measurement"},
	"output.voltage":        {"Output voltage", "voltage", "V", "measurement"},
	"input.frequency":       {"Input frequency", "frequency", "Hz", "measurement"},
	"ups.load":              {"Load", "", "%", "measurement"},
	"ups.realpower":         {"Power", "power", "W", "measurement"},
	"ups.power":             {"Apparent power", "apparent_power", "VA", "measurement"},
	"ups.temperature":       {"Temperature", "temperature", "°C", "measurement"},
	"battery.temperature":   {"Battery temperature", "temperature", "°C", "measurement"},
	"ups.status":            {"Status", "", "", ""},
	"ups.test.result":       {"Self-test result", "", "", ""},
	"ups.beeper.status":     {"Beeper", "", "", ""},
	"ups.realpower.nominal": {"Rated power", "power", "W", ""},
}

// discover publishes Home Assistant discovery configs for variables not yet
// announced.
func (p *publisher) discover(u sdk.UPS) {
	name := sanitize(u.Name)
	device := map[string]any{
		"identifiers":  []string{"nutng_" + name},
		"name":         u.Name,
		"manufacturer": firstNonEmpty(u.Value("device.mfr"), u.Value("ups.mfr")),
		"model":        firstNonEmpty(u.Value("device.model"), u.Value("ups.model")),
		"via_device":   "nutng",
	}
	if s := u.Value("device.serial"); s != "" {
		device["serial_number"] = s
	}
	availability := []map[string]string{{"topic": p.topic("status")}, {"topic": p.topic(name, "available")}}
	state := p.topic(name, "state")

	vars := make([]string, 0, len(u.Vars))
	for k := range u.Vars {
		vars = append(vars, k)
	}
	sort.Strings(vars)
	for _, v := range vars {
		s, ok := sensors[v]
		key := u.Name + "/" + v
		p.mu.Lock()
		done := p.discovered[key]
		p.discovered[key] = true
		p.mu.Unlock()
		if !ok || done {
			continue
		}
		obj := sanitize(strings.ReplaceAll(v, ".", "_"))
		cfg := map[string]any{
			"name": s.name, "unique_id": "nutng_" + name + "_" + obj, "object_id": "nutng_" + name + "_" + obj,
			"state_topic": state, "value_template": fmt.Sprintf("{{ value_json.vars[%q] }}", v),
			"availability": availability, "availability_mode": "all", "device": device,
		}
		if v == "ups.status" {
			cfg["value_template"] = "{{ value_json.status_text }}"
		}
		if s.deviceClass != "" {
			cfg["device_class"] = s.deviceClass
		}
		if s.unit != "" {
			cfg["unit_of_measurement"] = s.unit
		}
		if s.stateClass != "" {
			cfg["state_class"] = s.stateClass
		}
		p.publish(fmt.Sprintf("%s/sensor/nutng_%s/%s/config", p.o.DiscoveryPrefix, name, obj), cfg, true)
	}

	// Binary sensors from the status flags.
	for _, b := range []struct{ obj, name, class, tmpl string }{
		{"power", "Mains power", "power", `{{ 'ON' if 'OL' in value_json.status.split() else 'OFF' }}`},
		{"low_battery", "Low battery", "battery", `{{ 'ON' if 'LB' in value_json.status.split() else 'OFF' }}`},
		{"replace_battery", "Replace battery", "problem", `{{ 'ON' if 'RB' in value_json.status.split() else 'OFF' }}`},
	} {
		key := u.Name + "/#" + b.obj
		p.mu.Lock()
		done := p.discovered[key]
		p.discovered[key] = true
		p.mu.Unlock()
		if done || u.Value("ups.status") == "" {
			continue
		}
		p.publish(fmt.Sprintf("%s/binary_sensor/nutng_%s/%s/config", p.o.DiscoveryPrefix, name, b.obj), map[string]any{
			"name": b.name, "unique_id": "nutng_" + name + "_" + b.obj, "object_id": "nutng_" + name + "_" + b.obj,
			"state_topic": state, "value_template": b.tmpl, "device_class": b.class,
			"availability": availability, "availability_mode": "all", "device": device,
		}, true)
	}
}

// onCommand runs a command published to <prefix>/<ups>/command/<name>.
func (p *publisher) onCommand(_ paho.Client, m paho.Message) {
	parts := strings.Split(strings.TrimPrefix(m.Topic(), p.o.TopicPrefix+"/"), "/")
	if len(parts) != 3 || parts[1] != "command" || parts[2] == "result" {
		return
	}
	upsTopic, cmd := parts[0], parts[2]
	ups := upsTopic
	for _, u := range p.host.ListUPS() {
		if sanitize(u.Name) == upsTopic {
			ups = u.Name
		}
	}
	err := p.host.RunCommand(context.Background(), ups, cmd, strings.TrimSpace(string(m.Payload())))
	result := map[string]any{"command": cmd, "ok": err == nil, "time": time.Now().UTC().Format(time.RFC3339)}
	if err != nil {
		result["error"] = err.Error()
		p.log.Warn("MQTT command failed", "ups", ups, "command", cmd, "error", err)
	} else {
		p.log.Info("MQTT command run", "ups", ups, "command", cmd)
	}
	p.publish(p.topic(upsTopic, "command", "result"), result, false)
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}
