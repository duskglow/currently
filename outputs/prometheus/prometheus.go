// Package prometheus serves UPS state as Prometheus metrics in the text
// exposition format.
package prometheus

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/duskglow/currently/pkg/sdk"
)

// unitSuffix gives each unit its Prometheus base-unit suffix.
var unitSuffix = map[string]string{
	sdk.UnitPercent: "_percent",
	sdk.UnitSeconds: "_seconds",
	sdk.UnitVolts:   "_volts",
	sdk.UnitAmps:    "_amperes",
	sdk.UnitHertz:   "_hertz",
	sdk.UnitWatts:   "_watts",
	sdk.UnitVA:      "_voltamperes",
	sdk.UnitCelsius: "_celsius",
}

// statusFlags are exported as currently_ups_status{flag=...} 0/1 for every
// UPS, so alerts can match a flag that is off.
var statusFlags = []sdk.Flag{
	sdk.FlagOnline, sdk.FlagOnBattery, sdk.FlagLowBattery, sdk.FlagHighBattery,
	sdk.FlagReplaceBattery, sdk.FlagCharging, sdk.FlagDischarging, sdk.FlagBypass,
	sdk.FlagCalibrating, sdk.FlagOff, sdk.FlagOverload, sdk.FlagTrim, sdk.FlagBoost,
	sdk.FlagForcedShutdown,
}

// MetricName returns the metric for a numeric variable, such as
// currently_battery_charge_percent for battery.charge.
func MetricName(spec sdk.VarSpec) string {
	return "currently_" + strings.NewReplacer(".", "_", "-", "_").Replace(spec.Name) + unitSuffix[spec.Unit]
}

// Handler serves /metrics.
func Handler(host sdk.Host, version string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		Write(w, host.ListUPS(), version)
	})
}

type family struct {
	help, typ string
	samples   []string
}

// Write renders the metrics for a set of UPS snapshots.
func Write(w io.Writer, list []sdk.UPS, version string) {
	fams := map[string]*family{}
	add := func(name, typ, help string, labels map[string]string, value float64) {
		f := fams[name]
		if f == nil {
			f = &family{help: help, typ: typ}
			fams[name] = f
		}
		f.samples = append(f.samples, name+labelString(labels)+" "+formatValue(value))
	}

	add("currently_build_info", "gauge", "Currently version.", map[string]string{"version": version}, 1)
	for _, u := range list {
		ups := map[string]string{"ups": u.Name}
		up := 0.0
		if u.State == sdk.StateConnected {
			up = 1
		}
		add("currently_ups_up", "gauge", "1 when Currently is receiving data from the UPS.", ups, up)
		add("currently_ups_info", "gauge", "UPS identity; always 1.", map[string]string{
			"ups": u.Name, "driver": u.Driver, "model": u.Model(),
			"serial": u.Value("device.serial"), "description": u.Description,
		}, 1)
		add("currently_ups_driver_restarts_total", "counter", "Times the driver was reopened after a failure.", ups, float64(u.Restarts))
		if !u.LastUpdate.IsZero() {
			add("currently_ups_last_update_timestamp_seconds", "gauge", "Time of the last data from the UPS.", ups,
				float64(u.LastUpdate.UnixNano())/1e9)
		}
		if up == 0 {
			continue // do not export stale values as current
		}

		st := u.Status()
		for _, f := range statusFlags {
			v := 0.0
			if st.Has(f) {
				v = 1
			}
			add("currently_ups_status", "gauge", "1 when the UPS status has the flag (OL online, OB on battery, LB low battery, ...).",
				map[string]string{"ups": u.Name, "flag": string(f)}, v)
		}

		for name, v := range u.Vars {
			spec, ok := sdk.LookupVar(name)
			if !ok || !spec.Numeric {
				continue
			}
			f, ok := v.Float()
			if !ok {
				continue
			}
			add(MetricName(spec), "gauge", spec.Description+".", ups, f)
		}
	}

	names := make([]string, 0, len(fams))
	for n := range fams {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		f := fams[n]
		sort.Strings(f.samples)
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", n, escapeHelp(f.help), n, f.typ)
		for _, s := range f.samples {
			fmt.Fprintln(w, s)
		}
	}
}

func labelString(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + `="` + escapeLabel(labels[k]) + `"`
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func escapeLabel(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

func escapeHelp(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(s)
}

func formatValue(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }
