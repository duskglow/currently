package main

import (
	"crypto/tls"

	"github.com/nut-ng/nut-ng/pkg/plugin"
)

// insecureTLS skips certificate checks, for brokers with self-signed
// certificates (tls_insecure: true).
func insecureTLS() *tls.Config {
	return &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} //nolint:gosec // opt-in
}

func main() { plugin.ServeOutput(Output{}) }
