package suggest

import "testing"

func TestClosest(t *testing.T) {
	candidates := []string{"simulator", "usbhid", "exec"}
	tests := []struct {
		word, want string
	}{
		{"simulator", "simulator"},
		{"simulater", "simulator"},
		{"SIMULATOR", "simulator"},
		{"usbhd", "usbhid"},
		{"snmp", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := Closest(tt.word, candidates); got != tt.want {
			t.Errorf("Closest(%q) = %q, want %q", tt.word, got, tt.want)
		}
	}
}

func TestDidYouMean(t *testing.T) {
	if got := DidYouMean("drvier", []string{"driver", "description"}); got != ` (did you mean "driver"?)` {
		t.Errorf("got %q", got)
	}
	if got := DidYouMean("zzz", []string{"driver"}); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
