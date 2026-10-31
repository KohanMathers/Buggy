package config

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	idRe         = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	projectKeyRe = regexp.MustCompile(`^[A-Z][A-Z0-9]{0,15}$`)
	FieldTypes   = []string{"text", "textarea", "number", "select", "multiselect", "version", "user", "bool", "date", "url"}
	Builtins     = []string{"title", "description", "affects_versions", "attachments", "labels", "private"}
	Events       = []string{"issue.created", "issue.updated", "issue.transitioned", "issue.commented", "issue.attachment", "issue.duplicate", "issue.voted", "issue.confirmed", "issue.linked", "schedule"}
	Categories   = []string{"open", "active", "waiting", "closed"}
)

var KnownPermissions = []string{
	"issue.view", "issue.create", "issue.edit_own", "issue.edit", "issue.delete",
	"issue.transition", "issue.assign", "issue.label", "issue.link", "issue.duplicate",
	"issue.vote", "issue.watch", "issue.confirm", "issue.view_private", "issue.bulk",
	"comment.create", "comment.edit_own", "comment.edit", "comment.delete", "comment.restricted",
	"attachment.upload", "attachment.delete", "canned.use", "moderation", "triage",
	"version.manage", "user.manage", "admin", "api.token", "search.save", "search.save_public",
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func (c *Config) Validate() []string {
	var errs []string
	add := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }

	switch c.Database.Driver {
	case "sqlite", "postgres":
	default:
		add("database.driver: must be sqlite or postgres, got %q", c.Database.Driver)
	}
	if c.Database.Driver == "postgres" && c.Database.DSN == "" {
		add("database.dsn: required for postgres")
	}
	switch c.Storage.Driver {
	case "local":
	case "s3":
		if c.Storage.S3.Bucket == "" || c.Storage.S3.Endpoint == "" {
			add("storage.s3: endpoint and bucket are required")
		}
	default:
		add("storage.driver: must be local or s3, got %q", c.Storage.Driver)
	}
	if !contains([]string{"auto", "light", "dark"}, c.Instance.Theme.Mode) {
		add("instance.theme.mode: must be auto, light or dark")
	}
	th := c.Instance.Theme
	for _, col := range []struct{ name, v string }{{"primary", th.Primary}, {"accent", th.Accent}} {
		if col.v != "" && !ColorRe.MatchString(col.v) {
			add("instance.theme.%s: %q is not a colour (use #rrggbb, rgb(...), hsl(...) or a CSS colour name)", col.name, col.v)
		}
	}
	for section, m := range map[string]map[string]string{"colors": th.Colors, "dark_colors": th.DarkColors} {
		for k, v := range m {
			if _, ok := ThemeColors[k]; !ok {
				known := make([]string, 0, len(ThemeColors))
				for name := range ThemeColors {
					known = append(known, name)
				}
				sort.Strings(known)
				add("instance.theme.%s.%s: unknown colour (known: %s)", section, k, strings.Join(known, ", "))
			} else if !ColorRe.MatchString(v) {
				add("instance.theme.%s.%s: %q is not a colour", section, k, v)
			}
		}
	}
	if th.FontURL != "" && !strings.HasPrefix(th.FontURL, "https://") && !strings.HasPrefix(th.FontURL, "http://") && !strings.HasPrefix(th.FontURL, "/") {
		add("instance.theme.font_url: must be an http(s) URL or a path")
	}
	if !contains([]string{"open", "closed"}, c.Auth.Local.Registration) {
		add("auth.local.registration: must be open or closed")
	}
	if _, ok := c.Roles[c.Auth.DefaultRole]; !ok {
		add("auth.default_role: unknown role %q", c.Auth.DefaultRole)
	}
	if _, ok := c.Roles[c.Auth.AnonymousRole]; !ok {
		add("auth.anonymous_role: unknown role %q", c.Auth.AnonymousRole)
	}
	seenProv := map[string]bool{}
	for i, p := range c.Auth.Providers {
		at := fmt.Sprintf("auth.providers[%d]", i)
		if p.ID == "" {
			add("%s.id: required", at)
		} else if seenProv[p.ID] {
			add("%s.id: duplicate provider %q", at, p.ID)
		}
		seenProv[p.ID] = true
		if !contains([]string{"github", "discord", "google", "microsoft", "gitlab", "oidc", "oauth2"}, p.Type) {
			add("%s.type: must be one of github, discord, google, microsoft, gitlab, oidc, oauth2", at)
		}
		if p.ClientID == "" {
			add("%s.client_id: required", at)
		}
		if p.Type == "oidc" && p.Issuer == "" {
			add("%s.issuer: required for oidc", at)
		}
		if p.Type == "oauth2" && (p.AuthURL == "" || p.TokenURL == "" || p.UserInfoURL == "") {
			add("%s: auth_url, token_url and userinfo_url are required for oauth2", at)
		}
		if p.DefaultRole != "" {
			if _, ok := c.Roles[p.DefaultRole]; !ok {
				add("%s.default_role: unknown role %q", at, p.DefaultRole)
			}
		}
	}

	for name, r := range c.Roles {
		for _, parent := range r.Inherits {
			if _, ok := c.Roles[parent]; !ok {
				add("roles.%s.inherits: unknown role %q", name, parent)
			}
		}
		for _, p := range r.Permissions {
			if p == "*" || strings.HasSuffix(p, ".*") {
				continue
			}
			if !contains(KnownPermissions, p) {
				add("roles.%s.permissions: unknown permission %q (known: %s)", name, p, strings.Join(KnownPermissions, ", "))
			}
		}
	}

	fields := map[string]*Field{}
	for i := range c.Fields {
		f := &c.Fields[i]
		at := fmt.Sprintf("fields[%d] (%s)", i, f.ID)
		if !idRe.MatchString(f.ID) {
			add("%s.id: must be lowercase letters, digits and underscores", at)
		}
		if contains(Builtins, f.ID) {
			add("%s.id: %q is reserved for a built-in field", at, f.ID)
		}
		if fields[f.ID] != nil {
			add("%s.id: duplicate field", at)
		}
		fields[f.ID] = f
		if !contains(FieldTypes, f.Type) {
			add("%s.type: must be one of %s", at, strings.Join(FieldTypes, ", "))
		}
		if (f.Type == "select" || f.Type == "multiselect") && len(f.Options) == 0 {
			add("%s.options: select fields need options", at)
		}
		if f.Pattern != "" {
			if _, err := regexp.Compile(f.Pattern); err != nil {
				add("%s.pattern: %v", at, err)
			}
		}
		if f.EditPermission != "" && !contains(KnownPermissions, f.EditPermission) {
			add("%s.edit_permission: unknown permission %q", at, f.EditPermission)
		}
	}
	labels := map[string]bool{}
	for i, l := range c.Labels {
		if l.Name == "" {
			add("labels[%d].name: required", i)
		}
		if labels[l.Name] {
			add("labels[%d]: duplicate label %q", i, l.Name)
		}
		labels[l.Name] = true
	}

	if len(c.Workflows) == 0 {
		add("workflows: at least one workflow is required")
	}
	for name, wf := range c.Workflows {
		at := "workflows." + name
		states := map[string]bool{}
		for _, s := range wf.States {
			if !idRe.MatchString(s.ID) {
				add("%s.states: invalid id %q", at, s.ID)
			}
			if states[s.ID] {
				add("%s.states: duplicate state %q", at, s.ID)
			}
			states[s.ID] = true
			if !contains(Categories, s.Category) {
				add("%s.states.%s.category: must be one of %s", at, s.ID, strings.Join(Categories, ", "))
			}
		}
		for _, s := range wf.States {
			if s.OnReporterReply != "" && !states[s.OnReporterReply] {
				add("%s.states.%s.on_reporter_reply: unknown state %q", at, s.ID, s.OnReporterReply)
			}
		}
		if !states[wf.Initial] {
			add("%s.initial: unknown state %q", at, wf.Initial)
		}
		res := map[string]bool{}
		for _, r := range wf.Resolutions {
			if !idRe.MatchString(r.ID) {
				add("%s.resolutions: invalid id %q", at, r.ID)
			}
			res[r.ID] = true
		}
		tids := map[string]bool{}
		for _, t := range wf.Transitions {
			if tids[t.ID] {
				add("%s.transitions: duplicate id %q", at, t.ID)
			}
			tids[t.ID] = true
			if !states[t.To] {
				add("%s.transitions.%s.to: unknown state %q", at, t.ID, t.To)
			}
			for _, f := range t.From {
				if f != "*" && !states[f] {
					add("%s.transitions.%s.from: unknown state %q", at, t.ID, f)
				}
			}
			for _, r := range t.Resolutions {
				if !res[r] {
					add("%s.transitions.%s.resolutions: unknown resolution %q", at, t.ID, r)
				}
			}
			if t.Permission != "" && !contains(KnownPermissions, t.Permission) {
				add("%s.transitions.%s.permission: unknown permission %q", at, t.ID, t.Permission)
			}
		}
		if wf.DuplicateState != "" && !states[wf.DuplicateState] {
			add("%s.duplicate_state: unknown state %q", at, wf.DuplicateState)
		}
		if wf.DuplicateResolution != "" && !res[wf.DuplicateResolution] {
			add("%s.duplicate_resolution: unknown resolution %q", at, wf.DuplicateResolution)
		}
	}

	for name, f := range c.Forms {
		for si, s := range f.Steps {
			for _, ff := range s.Fields {
				if !contains(Builtins, ff.Field) && fields[ff.Field] == nil {
					add("forms.%s.steps[%d]: unknown field %q", name, si, ff.Field)
				}
				if ff.ShowIf != nil && !contains(Builtins, ff.ShowIf.Field) && fields[ff.ShowIf.Field] == nil {
					add("forms.%s.steps[%d].%s.show_if: unknown field %q", name, si, ff.Field, ff.ShowIf.Field)
				}
			}
		}
	}

	keys := map[string]bool{}
	for i, p := range c.Projects {
		at := fmt.Sprintf("projects[%d] (%s)", i, p.Key)
		if !projectKeyRe.MatchString(p.Key) {
			add("%s.key: must be 1-16 uppercase letters/digits starting with a letter", at)
		}
		if keys[p.Key] {
			add("%s.key: duplicate project", at)
		}
		keys[p.Key] = true
		if _, ok := c.Workflows[p.Workflow]; !ok {
			add("%s.workflow: unknown workflow %q", at, p.Workflow)
		}
		if p.Form != "" {
			if _, ok := c.Forms[p.Form]; !ok {
				add("%s.form: unknown form %q", at, p.Form)
			}
		}
		for _, f := range p.Fields {
			if fields[f] == nil {
				add("%s.fields: unknown field %q", at, f)
			}
		}
		for user, role := range p.Members {
			if _, ok := c.Roles[role]; !ok {
				add("%s.members.%s: unknown role %q", at, user, role)
			}
		}
	}
	for i := range c.Fields {
		for _, pk := range c.Fields[i].Projects {
			if !keys[strings.ToUpper(pk)] {
				add("fields[%d] (%s).projects: unknown project %q", i, c.Fields[i].ID, pk)
			}
		}
	}

	pids := map[string]bool{}
	for i, p := range c.Parsers {
		at := fmt.Sprintf("parsers[%d] (%s)", i, p.ID)
		if p.ID == "" {
			add("%s.id: required", at)
		}
		if pids[p.ID] {
			add("%s.id: duplicate parser", at)
		}
		pids[p.ID] = true
		if p.Match.Content != "" {
			if _, err := regexp.Compile(p.Match.Content); err != nil {
				add("%s.match.content: %v", at, err)
			}
		}
		for _, g := range p.Match.Filename {
			if _, err := filepath.Match(g, "x"); err != nil {
				add("%s.match.filename: bad glob %q", at, g)
			}
		}
		for j, e := range p.Extract {
			re, err := regexp.Compile(e.Regex)
			if err != nil {
				add("%s.extract[%d].regex: %v", at, j, err)
				continue
			}
			if e.Group > re.NumSubexp() {
				add("%s.extract[%d].group: regex has only %d groups", at, j, re.NumSubexp())
			}
			if fields[e.Field] == nil {
				add("%s.extract[%d].field: unknown field %q", at, j, e.Field)
			}
		}
		if fp := p.Fingerprint; fp != nil {
			for _, r := range append([]string{fp.Exception, fp.Frames}, fp.Ignore...) {
				if r == "" {
					continue
				}
				if _, err := regexp.Compile(r); err != nil {
					add("%s.fingerprint: %v", at, err)
				}
			}
		}
	}

	for i, a := range c.Automations {
		at := fmt.Sprintf("automations[%d] (%s)", i, a.ID)
		if len(a.On) == 0 {
			add("%s.on: at least one event is required (e.g. schedule, issue.created)", at)
		}
		for _, e := range a.On {
			if !contains(Events, e) {
				add("%s.on: unknown event %q (known: %s)", at, e, strings.Join(Events, ", "))
			}
		}
		for f := range a.Actions.SetFields {
			if fields[f] == nil {
				add("%s.do.set_fields: unknown field %q", at, f)
			}
		}
	}
	for i, w := range c.Webhooks {
		if w.URL == "" {
			add("webhooks[%d].url: required", i)
		}
		if !contains([]string{"json", "discord", "slack"}, w.Format) {
			add("webhooks[%d].format: must be json, discord or slack", i)
		}
		for _, e := range w.Events {
			if e != "*" && !contains(Events, e) {
				add("webhooks[%d].events: unknown event %q", i, e)
			}
		}
	}
	for i, g := range c.Git {
		if g.ID == "" {
			add("git[%d].id: required", i)
		}
		if !contains([]string{"github", "gitlab", "gitea", "generic"}, g.Provider) {
			add("git[%d].provider: must be github, gitlab, gitea or generic", i)
		}
	}
	cids := map[string]bool{}
	for i, cr := range c.Canned {
		if cr.ID == "" {
			add("canned_responses[%d].id: required", i)
		}
		if cids[cr.ID] {
			add("canned_responses[%d].id: duplicate %q", i, cr.ID)
		}
		cids[cr.ID] = true
	}
	if c.Spam.Captcha.Provider != "" && !contains([]string{"turnstile", "hcaptcha", "recaptcha"}, c.Spam.Captcha.Provider) {
		add("spam.captcha.provider: must be turnstile, hcaptcha or recaptcha")
	}
	if c.Notifications.Email.Enabled && (c.Notifications.Email.Host == "" || c.Notifications.Email.From == "") {
		add("notifications.email: host and from are required when enabled")
	}
	return errs
}
