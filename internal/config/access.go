package config

import (
	"regexp"
	"strings"
)

func (c *Config) Project(key string) *Project { return c.projectIdx[strings.ToUpper(key)] }
func (c *Config) Field(id string) *Field      { return c.fieldIdx[id] }
func (c *Config) Label(name string) *Label    { return c.labelIdx[name] }
func (c *Config) Parser(id string) *Parser    { return c.parserIdx[id] }

func (c *Config) Workflow(name string) *Workflow {
	if wf, ok := c.Workflows[name]; ok {
		return &wf
	}
	return nil
}

func (c *Config) ProjectWorkflow(key string) *Workflow {
	p := c.Project(key)
	if p == nil {
		return c.Workflow("default")
	}
	return c.Workflow(p.Workflow)
}

func (c *Config) ProjectFields(key string) []*Field {
	p := c.Project(key)
	var out []*Field
	seen := map[string]bool{}
	for i := range c.Fields {
		f := &c.Fields[i]
		ok := len(f.Projects) == 0
		for _, pk := range f.Projects {
			if strings.EqualFold(pk, key) {
				ok = true
			}
		}
		if p != nil {
			for _, id := range p.Fields {
				if id == f.ID {
					ok = true
				}
			}
		}
		if ok && !seen[f.ID] {
			seen[f.ID] = true
			out = append(out, f)
		}
	}
	return out
}

func (c *Config) ProjectForm(key string) *Form {
	if p := c.Project(key); p != nil && p.Form != "" {
		if f, ok := c.Forms[p.Form]; ok {
			return &f
		}
	}
	if f, ok := c.Forms["default"]; ok {
		return &f
	}
	fields := []FormField{{Field: "title"}, {Field: "description"}, {Field: "affects_versions"}}
	for _, f := range c.ProjectFields(key) {
		if f.EditPermission == "" {
			fields = append(fields, FormField{Field: f.ID})
		}
	}
	fields = append(fields, FormField{Field: "attachments"})
	return &Form{Steps: []FormStep{{Fields: fields}}}
}

func (c *Config) Perms(role string) map[string]bool { return c.permCache[role] }

func (c *Config) RoleHas(role, perm string) bool {
	return HasPerm(c.permCache[role], perm)
}

func HasPerm(set map[string]bool, perm string) bool {
	if set == nil {
		return false
	}
	if set["*"] || set["admin"] || set[perm] {
		return true
	}
	if i := strings.IndexByte(perm, '.'); i > 0 && set[perm[:i]+".*"] {
		return true
	}
	return false
}

func (c *Config) T(locale, key, def string) string {
	if m, ok := c.Locales[locale]; ok {
		if v, ok := m[key]; ok {
			return v
		}
	}
	if v, ok := c.Text[key]; ok {
		return v
	}
	return def
}

func (f *Field) Matches(v string) bool {
	if f.pattern == nil {
		if f.Pattern == "" {
			return true
		}
		f.pattern = regexp.MustCompile(f.Pattern)
	}
	return f.pattern.MatchString(v)
}

func (c *Config) IsAdmin(username string) bool {
	for _, a := range c.Auth.Admins {
		if strings.EqualFold(a, username) {
			return true
		}
	}
	return false
}

func (c *Config) ProjectKeys() []string {
	out := make([]string, len(c.Projects))
	for i, p := range c.Projects {
		out[i] = p.Key
	}
	return out
}
