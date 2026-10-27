package usbhid

import (
	"bytes"
	"os"
	"strings"
)

// Container names the container Currently runs in ("lxc", "docker",
// "podman", ...), or returns "" on a host.
func Container() string {
	if b, err := os.ReadFile("/run/systemd/container"); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s
		}
	}
	if env, err := os.ReadFile("/proc/1/environ"); err == nil {
		for _, kv := range bytes.Split(env, []byte{0}) {
			if v, ok := bytes.CutPrefix(kv, []byte("container=")); ok && len(v) > 0 {
				return string(v)
			}
		}
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return "docker"
	}
	if _, err := os.Stat("/run/.containerenv"); err == nil {
		return "podman"
	}
	return ""
}
