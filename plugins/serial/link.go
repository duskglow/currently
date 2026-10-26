package main

import (
	"errors"
	"io"
	"strings"
	"sync"
	"time"
)

// port is the part of a serial port the protocols use (go.bug.st/serial's
// Port, or a fake in tests).
type port interface {
	io.ReadWriter
	SetReadTimeout(time.Duration) error
	ResetInputBuffer() error
	Close() error
}

// errTimeout means the UPS did not answer in time.
var errTimeout = errors.New("no answer from the UPS")

// link sends commands and reads replies over a port. One command runs at a
// time.
type link struct {
	mu      sync.Mutex
	p       port
	timeout time.Duration
	// pause between the characters of multi-character commands, which
	// some UPSes need.
	pause time.Duration
}

func newLink(p port, timeout time.Duration) *link {
	_ = p.SetReadTimeout(50 * time.Millisecond)
	return &link{p: p, timeout: timeout}
}

// send discards anything unread and writes s.
func (l *link) send(s string) error {
	_ = l.p.ResetInputBuffer()
	if l.pause == 0 || len(s) < 2 {
		_, err := l.p.Write([]byte(s))
		return err
	}
	for i := range len(s) {
		if _, err := l.p.Write([]byte{s[i]}); err != nil {
			return err
		}
		time.Sleep(l.pause)
	}
	return nil
}

// readUntil reads until the reply ends with term, or the timeout. It
// returns the reply without term.
func (l *link) readUntil(term string, timeout time.Duration) (string, error) {
	var b strings.Builder
	buf := make([]byte, 128)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		n, err := l.p.Read(buf)
		if n > 0 {
			b.Write(buf[:n])
			if s := b.String(); strings.Contains(s, term) {
				return s[:strings.Index(s, term)], nil
			}
			continue
		}
		if err != nil {
			return b.String(), err
		}
	}
	if b.Len() > 0 {
		return b.String(), errTimeout
	}
	return "", errTimeout
}

// ask sends a command and returns the reply line.
func (l *link) ask(cmd, term string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.send(cmd); err != nil {
		return "", err
	}
	return l.readUntil(term, l.timeout)
}
