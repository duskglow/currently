package sdk

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/duskglow/currently/internal/suggest"
)

// Config holds a plugin's settings from the config file: every key in the
// UPS (or output) block that the core itself does not use.
type Config map[string]any

// Decode copies the settings into v, a pointer to a struct whose fields carry
// yaml tags. Unknown keys are reported with a "did you mean" hint, so a typo
// in the config file is caught instead of silently ignored. Durations accept
// strings such as "30s" or "5m".
func (c Config) Decode(v any) error {
	known := yamlFieldNames(v)
	var unknown []string
	for key := range c {
		if _, ok := known[key]; !ok {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		names := make([]string, 0, len(known))
		for k := range known {
			names = append(names, k)
		}
		sort.Strings(names)
		msgs := make([]string, len(unknown))
		for i, key := range unknown {
			msgs[i] = fmt.Sprintf("unknown option %q%s", key, suggest.DidYouMean(key, names))
		}
		return fmt.Errorf("%s; valid options are: %s", strings.Join(msgs, "; "), strings.Join(names, ", "))
	}
	if len(c) == 0 {
		return nil
	}
	raw, err := yaml.Marshal(map[string]any(c))
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("%s", strings.TrimPrefix(err.Error(), "yaml: "))
	}
	return nil
}

// yamlFieldNames returns the yaml keys of the struct v points to.
func yamlFieldNames(v any) map[string]struct{} {
	names := map[string]struct{}{}
	t := reflect.TypeOf(v)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		return names
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		switch name {
		case "-":
			continue
		case "":
			name = strings.ToLower(f.Name)
		}
		names[name] = struct{}{}
	}
	return names
}
