//go:build !windows

package plugin

import (
	"os"
	"time"
)

// waitParent blocks until the process pid exits. When a parent dies, its
// children are re-parented, so the parent PID changes.
func waitParent(pid int) {
	for os.Getppid() == pid {
		time.Sleep(2 * time.Second)
	}
}
