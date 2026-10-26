// Package scaffold creates new plugin projects for `nutng plugin new`.
package scaffold

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"text/template"
)

// "all:" keeps __init__.py and _gen/, which embed skips by default.
//
//go:embed all:templates all:python
var files embed.FS

// Params describe the plugin to create.
type Params struct {
	Kind string // "driver" or "output"
	Name string
	Lang string // "go" or "python"
	// SDKPath, for Go, points go.mod at a local nut-ng checkout.
	SDKPath string
	// NutngVersion is the nut-ng module version to require (Go).
	NutngVersion string
	// Python is the interpreter to show in the example config.
	Python string

	// Filled in by Create.
	AbsDir string
	Script string
	Exe    string
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// Create writes the project into dir, which must not exist or be empty,
// and returns the files written.
func Create(dir string, p Params) ([]string, error) {
	switch {
	case p.Kind != "driver" && p.Kind != "output":
		return nil, fmt.Errorf("kind must be driver or output, not %q", p.Kind)
	case p.Lang != "go" && p.Lang != "python":
		return nil, fmt.Errorf("language must be go or python, not %q", p.Lang)
	case !nameRE.MatchString(p.Name):
		return nil, fmt.Errorf("plugin name %q must be lowercase letters, digits, '-' or '_'", p.Name)
	}
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return nil, fmt.Errorf("%s already exists and is not empty", dir)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	p.AbsDir = filepath.ToSlash(abs)
	p.Script = p.AbsDir + "/plugin.py"
	if runtime.GOOS == "windows" {
		p.Exe = ".exe"
	}
	if p.Python == "" {
		p.Python = "python3"
		if runtime.GOOS == "windows" {
			p.Python = "python"
		}
	}
	if p.NutngVersion == "" {
		p.NutngVersion = "v0.0.0"
	}
	if p.SDKPath != "" {
		if p.SDKPath, err = filepath.Abs(p.SDKPath); err != nil {
			return nil, err
		}
		p.SDKPath = filepath.ToSlash(p.SDKPath)
	}

	var out []string
	write := func(name string, data []byte) error {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(name, "plugin.py") {
			mode = 0o755
		}
		out = append(out, path)
		return os.WriteFile(path, data, mode)
	}
	render := func(tmpl string) ([]byte, error) {
		src, err := files.ReadFile(tmpl)
		if err != nil {
			return nil, err
		}
		t, err := template.New(tmpl).Parse(string(src))
		if err != nil {
			return nil, err
		}
		var b bytes.Buffer
		if err := t.Execute(&b, p); err != nil {
			return nil, err
		}
		return b.Bytes(), nil
	}

	var plan [][2]string // template, destination
	if p.Lang == "go" {
		plan = [][2]string{{"templates/go/go.mod.tmpl", "go.mod"}, {"templates/go/" + p.Kind + ".go.tmpl", "main.go"},
			{"templates/go/README.md.tmpl", "README.md"}}
	} else {
		plan = [][2]string{{"templates/python/" + p.Kind + ".py.tmpl", "plugin.py"},
			{"templates/python/requirements.txt", "requirements.txt"}, {"templates/python/README.md.tmpl", "README.md"}}
	}
	for _, step := range plan {
		data, err := render(step[0])
		if err != nil {
			return out, err
		}
		if err := write(step[1], data); err != nil {
			return out, err
		}
	}
	if p.Lang == "python" {
		err := fs.WalkDir(files, "python", func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "__pycache__" {
					return fs.SkipDir
				}
				return nil
			}
			data, err := files.ReadFile(path)
			if err != nil {
				return err
			}
			return write(strings.TrimPrefix(path, "python/"), data)
		})
		if err != nil {
			return out, err
		}
	}
	return out, nil
}
