// Package config defines Buggy's YAML configuration.
package config

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Instance      Instance                     `yaml:"instance"`
	Server        Server                       `yaml:"server"`
	Database      Database                     `yaml:"database"`
	Storage       Storage                      `yaml:"storage"`
	Auth          Auth                         `yaml:"auth"`
	Roles         map[string]Role              `yaml:"roles"`
	Fields        []Field                      `yaml:"fields"`
	Labels        []Label                      `yaml:"labels"`
	Workflows     map[string]Workflow          `yaml:"workflows"`
	Forms         map[string]Form              `yaml:"forms"`
	Projects      []Project                    `yaml:"projects"`
	Parsers       []Parser                     `yaml:"parsers"`
	Automations   []Automation                 `yaml:"automations"`
	Canned        []CannedResponse             `yaml:"canned_responses"`
	Notifications Notifications                `yaml:"notifications"`
	Webhooks      []Webhook                    `yaml:"webhooks"`
	Git           []GitIntegration             `yaml:"git"`
	Spam          Spam                         `yaml:"spam"`
	Reputation    map[string]int               `yaml:"reputation"`
	Text          map[string]string            `yaml:"text"`
	Locales       map[string]map[string]string `yaml:"locales"`

	Files []string `yaml:"-"`

	fieldIdx   map[string]*Field
	projectIdx map[string]*Project
	labelIdx   map[string]*Label
	parserIdx  map[string]*Parser
	permCache  map[string]map[string]bool
}

type Instance struct {
	Name          string `yaml:"name"`
	Title         string `yaml:"title"`
	Description   string `yaml:"description"`
	URL           string `yaml:"url"`
	Logo          string `yaml:"logo"`
	DefaultAvatar string `yaml:"default_avatar"`
	Favicon       string `yaml:"favicon"`
	Theme         Theme  `yaml:"theme"`
	CustomCSS     string `yaml:"custom_css"`
	CustomCSSFile string `yaml:"custom_css_file"`
	CustomHead    string `yaml:"custom_head"`
	FooterText    string `yaml:"footer_text"`
	FooterLinks   []Link `yaml:"footer_links"`
	Announcement  string `yaml:"announcement"`
	DefaultLocale string `yaml:"default_locale"`
	IssuesPerPage int    `yaml:"issues_per_page"`
}

type Theme struct {
	Primary    string            `yaml:"primary"`
	Accent     string            `yaml:"accent"`
	Mode       string            `yaml:"mode"`
	Radius     string            `yaml:"radius"`
	Font       string            `yaml:"font"`
	MonoFont   string            `yaml:"mono_font"`
	FontSize   string            `yaml:"font_size"`
	FontURL    string            `yaml:"font_url"`
	Colors     map[string]string `yaml:"colors"`
	DarkColors map[string]string `yaml:"dark_colors"`
}

var ThemeColors = map[string]string{
	"primary":             "--primary",
	"accent":              "--accent",
	"background":          "--bg",
	"background_sunken":   "--bg-sunken",
	"surface":             "--surface",
	"surface_alt":         "--surface-2",
	"hover":               "--hover",
	"selected":            "--selected",
	"border":              "--border",
	"border_strong":       "--border-strong",
	"text":                "--text",
	"text_subtle":         "--subtle",
	"text_muted":          "--muted",
	"link":                "--link",
	"success":             "--ok",
	"warning":             "--warn",
	"danger":              "--danger",
	"info":                "--info-bg",
	"topbar":              "--topbar",
	"topbar_text":         "--topbar-text",
	"sidebar":             "--sidebar",
	"sidebar_text":        "--sidebar-text",
	"status_todo":         "--lz-todo-bg",
	"status_todo_text":    "--lz-todo-fg",
	"status_active":       "--lz-progress-bg",
	"status_active_text":  "--lz-progress-fg",
	"status_waiting":      "--lz-waiting-bg",
	"status_waiting_text": "--lz-waiting-fg",
	"status_done":         "--lz-done-bg",
	"status_done_text":    "--lz-done-fg",
	"status_danger":       "--lz-red-bg",
	"status_danger_text":  "--lz-red-fg",
}

var ColorRe = regexp.MustCompile(`^(#[0-9a-fA-F]{3,8}|[a-zA-Z]+|(rgb|rgba|hsl|hsla)\([\d\s.,%/deg]+\))$`)

type Link struct {
	Label string `yaml:"label"`
	URL   string `yaml:"url"`
}

type Server struct {
	Listen             string   `yaml:"listen"`
	Dev                bool     `yaml:"dev"`
	WebDir             string   `yaml:"web_dir"`
	ThemeDir           string   `yaml:"theme_dir"`
	DataDir            string   `yaml:"data_dir"`
	SecretKey          string   `yaml:"secret_key"`
	TrustProxy         bool     `yaml:"trust_proxy"`
	CookieSecure       string   `yaml:"cookie_secure"`
	Metrics            bool     `yaml:"metrics"`
	MetricsToken       string   `yaml:"metrics_token"`
	AutomationInterval Duration `yaml:"automation_interval"`
	ReloadInterval     Duration `yaml:"reload_interval"`
}

type Database struct {
	Driver string `yaml:"driver"`
	DSN    string `yaml:"dsn"`
}

type Storage struct {
	Driver       string   `yaml:"driver"`
	Path         string   `yaml:"path"`
	MaxSize      ByteSize `yaml:"max_size"`
	AllowedTypes []string `yaml:"allowed_types"`
	ParseMaxSize ByteSize `yaml:"parse_max_size"`
	S3           S3       `yaml:"s3"`
}

type S3 struct {
	Endpoint  string `yaml:"endpoint"`
	Bucket    string `yaml:"bucket"`
	Region    string `yaml:"region"`
	AccessKey string `yaml:"access_key"`
	SecretKey string `yaml:"secret_key"`
	UseSSL    bool   `yaml:"use_ssl"`
	Prefix    string `yaml:"prefix"`
}

type Auth struct {
	Local          LocalAuth  `yaml:"local"`
	Providers      []Provider `yaml:"providers"`
	FirstUserAdmin bool       `yaml:"first_user_admin"`
	Admins         []string   `yaml:"admins"`
	DefaultRole    string     `yaml:"default_role"`
	AnonymousRole  string     `yaml:"anonymous_role"`
	SessionTTL     Duration   `yaml:"session_ttl"`
}

type LocalAuth struct {
	Enabled      bool   `yaml:"enabled"`
	Registration string `yaml:"registration"`
	MinPassword  int    `yaml:"min_password"`
}

type Provider struct {
	ID            string   `yaml:"id"`
	Type          string   `yaml:"type"`
	Name          string   `yaml:"name"`
	ClientID      string   `yaml:"client_id"`
	ClientSecret  string   `yaml:"client_secret"`
	Issuer        string   `yaml:"issuer"`
	AuthURL       string   `yaml:"auth_url"`
	TokenURL      string   `yaml:"token_url"`
	UserInfoURL   string   `yaml:"userinfo_url"`
	Scopes        []string `yaml:"scopes"`
	IDField       string   `yaml:"id_field"`
	UsernameField string   `yaml:"username_field"`
	NameField     string   `yaml:"name_field"`
	EmailField    string   `yaml:"email_field"`
	AvatarField   string   `yaml:"avatar_field"`
	DefaultRole   string   `yaml:"default_role"`
	Icon          string   `yaml:"icon"`
}

type Role struct {
	Label       string   `yaml:"label"`
	Permissions []string `yaml:"permissions"`
	Inherits    []string `yaml:"inherits"`
	Badge       string   `yaml:"badge"`
	Color       string   `yaml:"color"`
}

type Field struct {
	ID             string   `yaml:"id"`
	Label          string   `yaml:"label"`
	Type           string   `yaml:"type"`
	Help           string   `yaml:"help"`
	Placeholder    string   `yaml:"placeholder"`
	Options        []Option `yaml:"options"`
	Required       bool     `yaml:"required"`
	Default        string   `yaml:"default"`
	Private        bool     `yaml:"private"`
	ShowInList     bool     `yaml:"show_in_list"`
	Projects       []string `yaml:"projects"`
	EditPermission string   `yaml:"edit_permission"`
	Environment    bool     `yaml:"environment"`
	Pattern        string   `yaml:"pattern"`
	Min            *float64 `yaml:"min"`
	Max            *float64 `yaml:"max"`

	pattern *regexp.Regexp
}

func (f *Field) Multi() bool { return f.Type == "multiselect" }

func (f *Field) Option(v string) *Option {
	for i := range f.Options {
		if f.Options[i].Value == v {
			return &f.Options[i]
		}
	}
	return nil
}

func (f *Field) OptionLabel(v string) string {
	if o := f.Option(v); o != nil && o.Label != "" {
		return o.Label
	}
	return v
}

type Option struct {
	Value       string `yaml:"value"`
	Label       string `yaml:"label"`
	Color       string `yaml:"color"`
	Description string `yaml:"description"`
}

func (o *Option) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		o.Value, o.Label = n.Value, n.Value
		return nil
	}
	type raw Option
	var r raw
	if err := n.Decode(&r); err != nil {
		return err
	}
	*o = Option(r)
	if o.Label == "" {
		o.Label = o.Value
	}
	return nil
}

type Label struct {
	Name        string `yaml:"name"`
	Color       string `yaml:"color"`
	Description string `yaml:"description"`
	Private     bool   `yaml:"private"`
	Restricted  bool   `yaml:"restricted"`
}

func (l *Label) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		l.Name = n.Value
		return nil
	}
	type raw Label
	var r raw
	if err := n.Decode(&r); err != nil {
		return err
	}
	*l = Label(r)
	return nil
}

type Workflow struct {
	Initial             string       `yaml:"initial"`
	States              []State      `yaml:"states"`
	Transitions         []Transition `yaml:"transitions"`
	Resolutions         []Resolution `yaml:"resolutions"`
	DuplicateState      string       `yaml:"duplicate_state"`
	DuplicateResolution string       `yaml:"duplicate_resolution"`
}

func (w *Workflow) State(id string) *State {
	for i := range w.States {
		if w.States[i].ID == id {
			return &w.States[i]
		}
	}
	return nil
}

func (w *Workflow) Resolution(id string) *Resolution {
	for i := range w.Resolutions {
		if w.Resolutions[i].ID == id {
			return &w.Resolutions[i]
		}
	}
	return nil
}

func (w *Workflow) Transition(id string) *Transition {
	for i := range w.Transitions {
		if w.Transitions[i].ID == id {
			return &w.Transitions[i]
		}
	}
	return nil
}

func (w *Workflow) FirstStateIn(cat string) string {
	for _, s := range w.States {
		if s.Category == cat {
			return s.ID
		}
	}
	return ""
}

func (w *Workflow) StatesIn(cat string) []string {
	var out []string
	for _, s := range w.States {
		if s.Category == cat {
			out = append(out, s.ID)
		}
	}
	return out
}

type State struct {
	ID              string `yaml:"id"`
	Label           string `yaml:"label"`
	Category        string `yaml:"category"`
	Color           string `yaml:"color"`
	Explain         string `yaml:"explain"`
	OnReporterReply string `yaml:"on_reporter_reply"`
}

func (s State) Closed() bool { return s.Category == "closed" }

type Transition struct {
	ID             string   `yaml:"id"`
	Label          string   `yaml:"label"`
	From           []string `yaml:"from"`
	To             string   `yaml:"to"`
	Permission     string   `yaml:"permission"`
	Reporter       bool     `yaml:"reporter"`
	RequireComment bool     `yaml:"require_comment"`
	Resolutions    []string `yaml:"resolutions"`
	Style          string   `yaml:"style"`
}

func (t *Transition) AllowedFrom(state string) bool {
	if len(t.From) == 0 {
		return true
	}
	for _, f := range t.From {
		if f == "*" || f == state {
			return true
		}
	}
	return false
}

type Resolution struct {
	ID      string `yaml:"id"`
	Label   string `yaml:"label"`
	Explain string `yaml:"explain"`
	Color   string `yaml:"color"`
	Valid   *bool  `yaml:"valid"`
}

func (r *Resolution) IsValid() bool { return r.Valid == nil || *r.Valid }

type Form struct {
	Intro          string     `yaml:"intro"`
	Steps          []FormStep `yaml:"steps"`
	DuplicateCheck *bool      `yaml:"duplicate_check"`
	SubmitLabel    string     `yaml:"submit_label"`
}

func (f *Form) CheckDuplicates() bool { return f.DuplicateCheck == nil || *f.DuplicateCheck }

type FormStep struct {
	Title       string      `yaml:"title"`
	Description string      `yaml:"description"`
	Fields      []FormField `yaml:"fields"`
}

type FormField struct {
	Field       string     `yaml:"field"`
	Required    *bool      `yaml:"required"`
	Label       string     `yaml:"label"`
	Help        string     `yaml:"help"`
	Placeholder string     `yaml:"placeholder"`
	Template    string     `yaml:"template"`
	ShowIf      *Condition `yaml:"show_if"`
}

func (f *FormField) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		f.Field = n.Value
		return nil
	}
	type raw FormField
	var r raw
	if err := n.Decode(&r); err != nil {
		return err
	}
	*f = FormField(r)
	return nil
}

type Condition struct {
	Field    string   `yaml:"field"`
	Equals   string   `yaml:"equals"`
	In       []string `yaml:"in"`
	NotEmpty bool     `yaml:"not_empty"`
}

func (c *Condition) Match(get func(string) []string) bool {
	if c == nil {
		return true
	}
	vals := get(c.Field)
	if c.NotEmpty {
		for _, v := range vals {
			if strings.TrimSpace(v) != "" {
				return true
			}
		}
		return false
	}
	want := c.In
	if c.Equals != "" {
		want = append([]string{c.Equals}, want...)
	}
	for _, v := range vals {
		for _, w := range want {
			if v == w {
				return true
			}
		}
	}
	return false
}

type Project struct {
	Key              string            `yaml:"key"`
	Name             string            `yaml:"name"`
	Description      string            `yaml:"description"`
	Icon             string            `yaml:"icon"`
	Color            string            `yaml:"color"`
	Workflow         string            `yaml:"workflow"`
	Form             string            `yaml:"form"`
	Fields           []string          `yaml:"fields"`
	Versions         []VersionConf     `yaml:"versions"`
	Private          bool              `yaml:"private"`
	Members          map[string]string `yaml:"members"`
	DefaultAssignee  string            `yaml:"default_assignee"`
	PrivateByDefault bool              `yaml:"private_by_default"`
	Archived         bool              `yaml:"archived"`
}

type VersionConf struct {
	Name        string `yaml:"name"`
	Released    bool   `yaml:"released"`
	ReleaseDate string `yaml:"release_date"`
	Archived    bool   `yaml:"archived"`
}

func (v *VersionConf) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		v.Name = n.Value
		return nil
	}
	type raw VersionConf
	var r raw
	if err := n.Decode(&r); err != nil {
		return err
	}
	*v = VersionConf(r)
	return nil
}

type Parser struct {
	ID          string       `yaml:"id"`
	Name        string       `yaml:"name"`
	Projects    []string     `yaml:"projects"`
	Match       ParserMatch  `yaml:"match"`
	Extract     []Extract    `yaml:"extract"`
	Labels      []string     `yaml:"labels"`
	Fingerprint *Fingerprint `yaml:"fingerprint"`
}

type ParserMatch struct {
	Filename []string `yaml:"filename"`
	Content  string   `yaml:"content"`
}

type Extract struct {
	Field string            `yaml:"field"`
	Regex string            `yaml:"regex"`
	Group int               `yaml:"group"`
	All   bool              `yaml:"all"`
	Map   map[string]string `yaml:"map"`
}

type Fingerprint struct {
	Exception string   `yaml:"exception"`
	Frames    string   `yaml:"frames"`
	Count     int      `yaml:"count"`
	Ignore    []string `yaml:"ignore"`
}

type Automation struct {
	ID       string   `yaml:"id"`
	Name     string   `yaml:"name"`
	On       []string `yaml:"on"`
	Query    string   `yaml:"query"`
	Idle     Duration `yaml:"idle"`
	Actions  Actions  `yaml:"do"`
	Disabled bool     `yaml:"disabled"`
}

type Actions struct {
	Transition   string            `yaml:"transition"`
	Resolution   string            `yaml:"resolution"`
	Comment      string            `yaml:"comment"`
	AddLabels    []string          `yaml:"add_labels"`
	RemoveLabels []string          `yaml:"remove_labels"`
	SetFields    map[string]string `yaml:"set_fields"`
	Assign       string            `yaml:"assign"`
	Private      *bool             `yaml:"private"`
}

type CannedResponse struct {
	ID         string   `yaml:"id"`
	Name       string   `yaml:"name"`
	Body       string   `yaml:"body"`
	Transition string   `yaml:"transition"`
	Resolution string   `yaml:"resolution"`
	AddLabels  []string `yaml:"add_labels"`
	Restricted bool     `yaml:"restricted"`
}

type Notifications struct {
	Email    Email `yaml:"email"`
	Mentions bool  `yaml:"mentions"`
}

type Email struct {
	Enabled  bool   `yaml:"enabled"`
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	From     string `yaml:"from"`
	TLS      string `yaml:"tls"`
}

type Webhook struct {
	Name           string   `yaml:"name"`
	URL            string   `yaml:"url"`
	Format         string   `yaml:"format"`
	Events         []string `yaml:"events"`
	Projects       []string `yaml:"projects"`
	Secret         string   `yaml:"secret"`
	IncludePrivate bool     `yaml:"include_private"`
}

type GitIntegration struct {
	ID              string   `yaml:"id"`
	Provider        string   `yaml:"provider"`
	Secret          string   `yaml:"secret"`
	Projects        []string `yaml:"projects"`
	CloseKeywords   []string `yaml:"close_keywords"`
	CloseState      string   `yaml:"close_state"`
	CloseResolution string   `yaml:"close_resolution"`
}

type Spam struct {
	RateLimits          map[string]int `yaml:"rate_limits"`
	Captcha             Captcha        `yaml:"captcha"`
	FirstPostModeration bool           `yaml:"first_post_moderation"`
	BlockedWords        []string       `yaml:"blocked_words"`
	MinAccountAge       Duration       `yaml:"min_account_age"`
}

type Captcha struct {
	Provider string   `yaml:"provider"`
	SiteKey  string   `yaml:"site_key"`
	Secret   string   `yaml:"secret"`
	On       []string `yaml:"on"`
}

func (c Captcha) Enabled(action string) bool {
	if c.Provider == "" {
		return false
	}
	for _, a := range c.On {
		if a == action {
			return true
		}
	}
	return false
}

type Duration time.Duration

var durRe = regexp.MustCompile(`^(\d+(?:\.\d+)?)([dw])$`)

func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return 0, nil
	}
	if m := durRe.FindStringSubmatch(s); m != nil {
		n, _ := strconv.ParseFloat(m[1], 64)
		mult := 24 * time.Hour
		if m[2] == "w" {
			mult *= 7
		}
		return time.Duration(n * float64(mult)), nil
	}
	return time.ParseDuration(s)
}

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: invalid duration %q", n.Line, n.Value)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) D() time.Duration { return time.Duration(d) }

type ByteSize int64

func ParseByteSize(s string) (int64, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	mult := int64(1)
	for _, u := range []struct {
		suf string
		m   int64
	}{{"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10}, {"G", 1 << 30}, {"M", 1 << 20}, {"K", 1 << 10}, {"B", 1}} {
		if strings.HasSuffix(s, u.suf) {
			mult = u.m
			s = strings.TrimSpace(strings.TrimSuffix(s, u.suf))
			break
		}
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	return int64(n * float64(mult)), nil
}

func (b *ByteSize) UnmarshalYAML(n *yaml.Node) error {
	v, err := ParseByteSize(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: invalid size %q", n.Line, n.Value)
	}
	*b = ByteSize(v)
	return nil
}
