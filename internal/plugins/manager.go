package plugins

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/duskglow/currently/internal/registry"
	"github.com/duskglow/currently/pkg/sdk"
)

// nameRE matches plugin executables: currently-driver-<name> or
// currently-output-<name>, with .exe on Windows.
var nameRE = regexp.MustCompile(`^currently-(driver|output)-([A-Za-z0-9][A-Za-z0-9_.-]*?)(\.exe)?$`)

// Status describes one plugin.
type Status struct {
	Name        string   `json:"name"`
	Kind        sdk.Kind `json:"kind"`
	Version     string   `json:"version,omitempty"`
	Description string   `json:"description,omitempty"`
	// Source is the executable path or command line.
	Source  string `json:"source"`
	Running bool   `json:"running"`
	// Starts counts launches; more than 1 means it was restarted.
	Starts int    `json:"starts"`
	Error  string `json:"error,omitempty"`
}

// Manager owns the plugin processes.
type Manager struct {
	log     *slog.Logger
	mu      sync.Mutex
	entries []*entry
}

type entry struct {
	p    *process
	info sdk.Info
	err  string // load error
}

// Options say where to find plugins.
type Options struct {
	// Dirs are searched for plugin executables, in order.
	Dirs []string
	// Commands are plugins started with an interpreter, such as
	// ["python3", "/opt/currently/myplugin.py"].
	Commands [][]string
}

// Find lists plugin executables in dirs, ignoring missing directories.
// When the same file name appears twice, the first directory wins.
func Find(dirs []string) [][]string {
	seen := map[string]bool{}
	var out [][]string
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		sort.Strings(names)
		for _, n := range names {
			if !nameRE.MatchString(n) || seen[n] {
				continue
			}
			if runtime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(n), ".exe") {
				continue
			}
			path := filepath.Join(dir, n)
			if fi, err := os.Stat(path); err != nil || fi.IsDir() || (runtime.GOOS != "windows" && fi.Mode()&0o111 == 0) {
				continue
			}
			seen[n] = true
			out = append(out, []string{path})
		}
	}
	return out
}

// Load starts every plugin, learns what it is, and registers it in reg.
// Plugins that fail to start are reported in Statuses, not as errors, so
// one broken plugin does not stop Currently.
func Load(ctx context.Context, opts Options, reg *registry.Registry, log *slog.Logger) *Manager {
	m := &Manager{log: log}
	cmds := append(Find(opts.Dirs), opts.Commands...)
	var wg sync.WaitGroup
	entries := make([]*entry, len(cmds))
	for i, c := range cmds {
		if len(c) == 0 {
			continue
		}
		source := strings.Join(c, " ")
		e := &entry{p: newProcess(c, source, guessName(c), log)}
		entries[i] = e
		wg.Add(1)
		go func() {
			defer wg.Done()
			info, err := e.p.describe(ctx)
			if err != nil {
				e.err = err.Error()
				e.p.kill()
				return
			}
			e.info = info
			e.p.name.Store(info.Name)
		}()
	}
	wg.Wait()

	for _, e := range entries {
		if e == nil {
			continue
		}
		m.entries = append(m.entries, e)
		if e.err != "" {
			log.Warn("plugin failed to start", "source", e.p.source, "error", e.err)
			continue
		}
		var err error
		switch e.info.Kind {
		case sdk.KindDriver:
			err = add(func() { reg.AddDriver(&Driver{p: e.p, info: e.info}) }, reg.HasDriver(e.info.Name))
		case sdk.KindOutput:
			err = add(func() { reg.AddOutput(&Output{p: e.p, info: e.info}) }, reg.HasOutput(e.info.Name))
		}
		if err != nil {
			e.err = fmt.Sprintf("%s %q: %v", e.info.Kind, e.info.Name, err)
			log.Warn("plugin not used", "source", e.p.source, "error", e.err)
			e.p.kill()
			continue
		}
		log.Info("plugin loaded", "name", e.info.Name, "kind", e.info.Kind, "version", e.info.Version, "source", e.p.source)
	}
	return m
}

// guessName returns the plugin name from its file name, for logs written
// before the plugin has described itself.
func guessName(cmd []string) string {
	base := filepath.Base(cmd[len(cmd)-1])
	if m := nameRE.FindStringSubmatch(base); m != nil {
		return m[2]
	}
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func add(register func(), exists bool) error {
	if exists {
		return fmt.Errorf("another driver or output already has this name")
	}
	register()
	return nil
}

// Statuses reports every plugin found.
func (m *Manager) Statuses() []Status {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Status, 0, len(m.entries))
	for _, e := range m.entries {
		running, starts, lastErr := e.p.state()
		st := Status{Name: e.info.Name, Kind: e.info.Kind, Version: e.info.Version, Description: e.info.Description,
			Source: e.p.source, Running: running, Starts: starts, Error: e.err}
		if st.Error == "" {
			st.Error = lastErr
		}
		out = append(out, st)
	}
	return out
}

// StopUnused stops the processes of plugins whose names are not in used.
// They start again if something needs them later.
func (m *Manager) StopUnused(used map[string]bool) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.entries {
		if e.err == "" && !used[e.info.Name] {
			m.log.Debug("stopping unused plugin", "plugin", e.info.Name)
			e.p.stop()
		}
	}
}

// Close stops every plugin process.
func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.entries {
		e.p.kill()
	}
}
