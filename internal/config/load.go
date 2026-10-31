package config

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed defaults.yml
var defaultsYAML []byte

//go:embed starter.yml
var StarterYAML []byte

var envRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)

func ExpandEnv(b []byte) []byte {
	return envRe.ReplaceAllFunc(b, func(m []byte) []byte {
		sub := envRe.FindSubmatch(m)
		if v, ok := os.LookupEnv(string(sub[1])); ok && v != "" {
			return []byte(v)
		}
		return sub[2]
	})
}

func ConfigFiles(path string) []string {
	files := []string{path}
	dir := filepath.Join(filepath.Dir(path), "config.d")
	var extra []string
	for _, pat := range []string{"*.yml", "*.yaml"} {
		m, _ := filepath.Glob(filepath.Join(dir, pat))
		extra = append(extra, m...)
	}
	sort.Strings(extra)
	return append(files, extra...)
}

type ValidationError struct{ Problems []string }

func (v *ValidationError) Error() string {
	return "invalid configuration:\n  - " + strings.Join(v.Problems, "\n  - ")
}

func Load(path string) (*Config, error) {
	cfg, err := decodeDefaults()
	if err != nil {
		return nil, fmt.Errorf("built-in defaults: %w", err)
	}
	var merged any
	var used []string
	for _, f := range ConfigFiles(path) {
		raw, err := os.ReadFile(f)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) && f == path {
				continue
			}
			return nil, err
		}
		used = append(used, f)
		var doc any
		if err := yaml.Unmarshal(ExpandEnv(raw), &doc); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		merged = mergeValues(merged, doc)
	}
	if merged != nil {
		out, err := yaml.Marshal(merged)
		if err != nil {
			return nil, err
		}
		dec := yaml.NewDecoder(bytes.NewReader(out))
		dec.KnownFields(true)
		if err := dec.Decode(cfg); err != nil {
			hint := path
			if len(used) > 1 {
				hint = strings.Join(used, " + ")
			}
			return nil, fmt.Errorf("%s: %w", hint, err)
		}
	}
	cfg.Files = used
	if len(used) == 0 {
		cfg.Files = []string{path}
	}
	cfg.applyDerived(path)
	if errs := cfg.Validate(); len(errs) > 0 {
		return nil, &ValidationError{Problems: errs}
	}
	cfg.index()
	return cfg, nil
}

func decodeDefaults() (*Config, error) {
	cfg := &Config{}
	if err := yaml.Unmarshal(defaultsYAML, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func mergeValues(a, b any) any {
	switch bv := b.(type) {
	case map[string]any:
		am, ok := a.(map[string]any)
		if !ok {
			return bv
		}
		out := make(map[string]any, len(am)+len(bv))
		for k, v := range am {
			out[k] = v
		}
		for k, v := range bv {
			out[k] = mergeValues(am[k], v)
		}
		return out
	case []any:
		if al, ok := a.([]any); ok {
			return append(append([]any{}, al...), bv...)
		}
		return bv
	case nil:
		return a
	default:
		return bv
	}
}

func (c *Config) applyDerived(path string) {
	if c.Server.DataDir == "" {
		c.Server.DataDir = "./data"
	}
	if c.Database.Driver == "" {
		c.Database.Driver = "sqlite"
	}
	if c.Database.Driver == "sqlite" && c.Database.DSN == "" {
		c.Database.DSN = filepath.Join(c.Server.DataDir, "buggy.db")
	}
	if c.Storage.Driver == "" {
		c.Storage.Driver = "local"
	}
	if c.Storage.Path == "" {
		c.Storage.Path = filepath.Join(c.Server.DataDir, "attachments")
	}
	if c.Storage.MaxSize == 0 {
		c.Storage.MaxSize = 25 << 20
	}
	if c.Storage.ParseMaxSize == 0 {
		c.Storage.ParseMaxSize = 4 << 20
	}
	c.Instance.URL = strings.TrimRight(c.Instance.URL, "/")
	if c.Instance.IssuesPerPage <= 0 {
		c.Instance.IssuesPerPage = 50
	}
	if c.Instance.CustomCSSFile != "" && !filepath.IsAbs(c.Instance.CustomCSSFile) {
		c.Instance.CustomCSSFile = filepath.Join(filepath.Dir(path), c.Instance.CustomCSSFile)
	}
	for i := range c.Projects {
		p := &c.Projects[i]
		p.Key = strings.ToUpper(p.Key)
		if p.Workflow == "" {
			p.Workflow = "default"
		}
		if p.Name == "" {
			p.Name = p.Key
		}
	}
	for i := range c.Fields {
		f := &c.Fields[i]
		if f.Label == "" {
			f.Label = f.ID
		}
		if f.Type == "" {
			f.Type = "text"
		}
	}
	for name, wf := range c.Workflows {
		for i := range wf.States {
			if wf.States[i].Label == "" {
				wf.States[i].Label = wf.States[i].ID
			}
			if wf.States[i].Category == "" {
				wf.States[i].Category = "open"
			}
		}
		for i := range wf.Transitions {
			t := &wf.Transitions[i]
			if t.ID == "" {
				t.ID = t.To
			}
			if t.Label == "" {
				t.Label = t.ID
			}
			if t.Permission == "" && !t.Reporter {
				t.Permission = "issue.transition"
			}
		}
		for i := range wf.Resolutions {
			if wf.Resolutions[i].Label == "" {
				wf.Resolutions[i].Label = wf.Resolutions[i].ID
			}
		}
		if wf.Initial == "" && len(wf.States) > 0 {
			wf.Initial = wf.States[0].ID
		}
		c.Workflows[name] = wf
	}
	for i := range c.Parsers {
		for j := range c.Parsers[i].Extract {
			if c.Parsers[i].Extract[j].Group == 0 {
				c.Parsers[i].Extract[j].Group = 1
			}
		}
		if fp := c.Parsers[i].Fingerprint; fp != nil && fp.Count == 0 {
			fp.Count = 5
		}
	}
	for i := range c.Automations {
		if c.Automations[i].ID == "" {
			c.Automations[i].ID = fmt.Sprintf("automation-%d", i+1)
		}
	}
	for i := range c.Git {
		g := &c.Git[i]
		if len(g.CloseKeywords) == 0 {
			g.CloseKeywords = []string{"fix", "fixes", "fixed", "close", "closes", "closed", "resolve", "resolves", "resolved"}
		}
		if g.CloseResolution == "" {
			g.CloseResolution = "fixed"
		}
	}
	for i := range c.Webhooks {
		if c.Webhooks[i].Format == "" {
			c.Webhooks[i].Format = "json"
		}
	}
	if c.Notifications.Email.Port == 0 {
		c.Notifications.Email.Port = 587
	}
}

func (c *Config) index() {
	c.fieldIdx = map[string]*Field{}
	for i := range c.Fields {
		f := &c.Fields[i]
		if f.Pattern != "" {
			f.pattern = regexp.MustCompile(f.Pattern)
		}
		c.fieldIdx[f.ID] = f
	}
	c.projectIdx = map[string]*Project{}
	for i := range c.Projects {
		c.projectIdx[c.Projects[i].Key] = &c.Projects[i]
	}
	c.labelIdx = map[string]*Label{}
	for i := range c.Labels {
		c.labelIdx[c.Labels[i].Name] = &c.Labels[i]
	}
	c.parserIdx = map[string]*Parser{}
	for i := range c.Parsers {
		c.parserIdx[c.Parsers[i].ID] = &c.Parsers[i]
	}
	c.permCache = map[string]map[string]bool{}
	for name := range c.Roles {
		c.permCache[name] = c.resolvePerms(name, map[string]bool{})
	}
}

func (c *Config) resolvePerms(role string, seen map[string]bool) map[string]bool {
	out := map[string]bool{}
	if seen[role] {
		return out
	}
	seen[role] = true
	r, ok := c.Roles[role]
	if !ok {
		return out
	}
	for _, parent := range r.Inherits {
		for p := range c.resolvePerms(parent, seen) {
			out[p] = true
		}
	}
	for _, p := range r.Permissions {
		out[p] = true
	}
	return out
}
