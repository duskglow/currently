package plugin

import "golang.org/x/sys/windows"

// waitParent blocks until the process pid exits.
func waitParent(pid int) {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return // already gone, or not ours to watch
	}
	defer func() { _ = windows.CloseHandle(h) }()
	_, _ = windows.WaitForSingleObject(h, windows.INFINITE)
}
