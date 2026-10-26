package sdk

import (
	"strings"
	"testing"
	"time"
)

func TestParseStatus(t *testing.T) {
	s := ParseStatus("ol  chrg")
	if !s.Has(FlagOnline) || !s.Has(FlagCharging) || s.Has(FlagOnBattery) {
		t.Fatalf("unexpected flags %v", s)
	}
	if got := s.String(); got != "OL CHRG" {
		t.Errorf("String() = %q", got)
	}
	if got := s.Describe(); got != "online, charging" {
		t.Errorf("Describe() = %q", got)
	}
	if got := ParseStatus("").Describe(); got != "unknown" {
		t.Errorf("empty Describe() = %q", got)
	}
}

func TestConfigDecode(t *testing.T) {
	type opts struct {
		Model    string        `yaml:"model"`
		Interval time.Duration `yaml:"interval"`
		Load     float64       `yaml:"load"`
	}
	var o opts
	err := Config{"model": "X1", "interval": "5s", "load": 40}.Decode(&o)
	if err != nil {
		t.Fatal(err)
	}
	if o.Model != "X1" || o.Interval != 5*time.Second || o.Load != 40 {
		t.Errorf("decoded %+v", o)
	}

	err = Config{"modle": "X1"}.Decode(&o)
	if err == nil || !strings.Contains(err.Error(), `did you mean "model"?`) {
		t.Errorf("want did-you-mean error, got %v", err)
	}

	err = Config{"interval": "soon"}.Decode(&o)
	if err == nil {
		t.Error("want error for bad duration")
	}
}

func TestFormatSeconds(t *testing.T) {
	tests := map[string]string{"45": "45s", "125": "2m05s", "3900": "1h05m", "n/a": "n/a"}
	for in, want := range tests {
		if got := FormatSeconds(in); got != want {
			t.Errorf("FormatSeconds(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUPSModel(t *testing.T) {
	u := UPS{Vars: map[string]Variable{
		"device.mfr":   {Value: "APC"},
		"device.model": {Value: "Back-UPS 1500"},
	}}
	if got := u.Model(); got != "APC Back-UPS 1500" {
		t.Errorf("Model() = %q", got)
	}
	u.Vars["device.model"] = Variable{Value: "APC Smart-UPS"}
	if got := u.Model(); got != "APC Smart-UPS" {
		t.Errorf("Model() = %q", got)
	}
}
