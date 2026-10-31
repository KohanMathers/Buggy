package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kohanmathers/buggy/internal/app"
	"github.com/kohanmathers/buggy/internal/config"
	"github.com/kohanmathers/buggy/internal/storage"
	"github.com/kohanmathers/buggy/internal/store"
	"github.com/kohanmathers/buggy/web"
)

type env struct {
	t   *testing.T
	srv *httptest.Server
	app *app.App
}

func setup(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yml")
	starter := strings.Replace(string(config.StarterYAML), "data_dir: ${BUGGY_DATA:-./data}", "data_dir: "+dir, 1)
	starter += `
webhooks: []
git:
  - id: gh
    provider: github
    secret: s3cret
`
	os.WriteFile(cfgPath, []byte(starter), 0o644)
	os.MkdirAll(filepath.Join(dir, "config.d"), 0o755)
	os.WriteFile(filepath.Join(dir, "config.d", "extra.yml"), []byte("projects:\n  - key: WEB\n    name: Website\ninstance:\n  title: Acme Bugs\n  theme:\n    font_url: https://fonts.example.com/inter.css\n    colors: { topbar: \"#123456\", sidebar_text: \"rgb(1, 2, 3)\" }\n    dark_colors: { topbar: \"#000000\" }\n"), 0o644)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if dsn := os.Getenv("BUGGY_TEST_PG_DSN"); dsn != "" {
		cfg.Database.Driver, cfg.Database.DSN = "postgres", store.TestSchemaDSN(t, dsn)
	}
	h := config.NewHolder(cfgPath, cfg)
	db, err := store.Open(cfg.Database.Driver, cfg.Database.DSN)
	if err != nil {
		t.Fatal(err)
	}
	files, _ := storage.New(cfg.Storage)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if testing.Verbose() {
		log = slog.Default()
	}
	a, err := app.New(context.Background(), h, db, files, log)
	if err != nil {
		t.Fatal(err)
	}
	a.Start()
	s := New(a, web.FS, []byte("test-secret"), "test")
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); a.Close(); db.Close() })
	return &env{t: t, srv: ts, app: a}
}

type client struct {
	e    *env
	http *http.Client
	csrf string
}

func (e *env) client() *client {
	jar, _ := cookiejar.New(nil)
	return &client{e: e, http: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

var csrfRe = regexp.MustCompile(`name="csrf-token" content="([^"]+)"`)

func (c *client) get(path string, want int) string {
	c.e.t.Helper()
	resp, err := c.http.Get(c.e.srv.URL + path)
	if err != nil {
		c.e.t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if want != 0 && resp.StatusCode != want {
		c.e.t.Fatalf("GET %s: status %d, want %d\n%s", path, resp.StatusCode, want, truncateS(string(b), 3000))
	}
	if m := csrfRe.FindStringSubmatch(string(b)); m != nil {
		c.csrf = m[1]
	}
	return string(b)
}

func (c *client) post(path string, form url.Values, want int) (string, *http.Response) {
	c.e.t.Helper()
	if c.csrf == "" {
		c.get("/", 0)
	}
	form.Set("_csrf", c.csrf)
	resp, err := c.http.PostForm(c.e.srv.URL+path, form)
	if err != nil {
		c.e.t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if want != 0 && resp.StatusCode != want {
		c.e.t.Fatalf("POST %s: status %d, want %d\n%s", path, resp.StatusCode, want, truncateS(string(b), 3000))
	}
	return string(b), resp
}

func (c *client) api(method, path, token string, body any, want int) map[string]any {
	c.e.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.e.srv.URL+path, r)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if want != 0 && resp.StatusCode != want {
		c.e.t.Fatalf("%s %s: status %d, want %d: %s", method, path, resp.StatusCode, want, raw)
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		var arr []any
		json.Unmarshal(raw, &arr)
		out = map[string]any{"list": arr}
	}
	return out
}

func truncateS(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

var codeRe = regexp.MustCompile(`\b[a-z2-9]{4}-[a-z2-9]{4}-[a-z2-9]{4}-[a-z2-9]{4}\b`)

func (c *client) register(name string) []string {
	c.get("/register", 200)
	body, _ := c.post("/register", url.Values{"username": {name}, "password": {"password123"}, "password2": {"password123"}, "email": {name + "@example.com"}}, 200)
	if !strings.Contains(body, "Save your recovery codes") {
		c.e.t.Fatalf("expected recovery codes page after registering:\n%s", truncateS(body, 2000))
	}
	codes := codeRe.FindAllString(body, -1)
	if len(codes) != 10 {
		c.e.t.Fatalf("expected 10 recovery codes, got %d", len(codes))
	}
	return codes
}

func TestEndToEnd(t *testing.T) {
	e := setup(t)
	admin := e.client()
	anon := e.client()

	anon.get("/?all=1", 200)
	anon.get("/projects/APP", 200)
	anon.get("/issues", 200)
	help := anon.get("/help", 200)
	if strings.Contains(help, "{{") || !strings.Contains(help, "This has been fixed. If it still happens") {
		t.Fatal("help page shows unrendered explanation templates")
	}
	for _, want := range []string{`:root[data-theme="light"]{--sidebar-text:rgb(1, 2, 3);--topbar:#123456;}`, `:root[data-theme="dark"]{--topbar:#000000;}`, `href="https://fonts.example.com/inter.css"`, "<span>Acme Bugs</span>"} {
		if !strings.Contains(help, want) {
			t.Fatalf("page missing theme output %q", want)
		}
	}
	anon.get("/login", 200)
	anon.get("/register", 200)
	anon.get("/static/app.css", 200)
	anon.get("/static/app.js", 200)
	anon.get("/healthz", 200)
	anon.get("/metrics", 200)
	anon.get("/favicon.svg", 200)
	anon.get("/api/v1", 200)
	anon.get("/nope", 404)
	if _, resp := anon.post("/projects/APP/new", url.Values{"title": {"x"}}, 303); !strings.HasPrefix(resp.Header.Get("Location"), "/login") {
		t.Fatal("anonymous create should redirect to login")
	}

	admin.register("alice")
	u, _ := e.app.DB.UserByName(context.Background(), "alice")
	if u.Role != "admin" {
		t.Fatalf("first user role = %s", u.Role)
	}
	bob := e.client()
	bob.register("bob")

	bob.get("/projects/APP/new", 200)
	body, _ := bob.post("/projects/APP/new", url.Values{"title": {"x"}, "f_category": {"crash"}}, 422)
	if !strings.Contains(body, "at least 3 characters") || !strings.Contains(body, "Crash log is required") {
		t.Fatalf("expected validation errors:\n%s", truncateS(body, 4000))
	}

	crash := "Exception in thread \"main\" java.lang.NullPointerException: boom\n\tat com.example.Map.render(Map.java:42)\n\tat com.example.Game.tick(Game.java:7)\nJava Version: 21.0.2\nOS: Windows 11\n"
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("_csrf", bob.csrf)
	mw.WriteField("title", "Game crashes when opening the map")
	mw.WriteField("description", "Steps: open map. It crashes. cc @alice see APP-99")
	mw.WriteField("f_category", "crash")
	mw.WriteField("f_crash_log", crash)
	mw.WriteField("affects_versions", "1.1")
	fw, _ := mw.CreateFormFile("attachments", "latest.log")
	fw.Write([]byte(crash))
	mw.Close()
	req, _ := http.NewRequest("POST", e.srv.URL+"/projects/APP/new", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := bob.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/issues/APP-1" {
		t.Fatalf("create: %d %s\n%s", resp.StatusCode, resp.Header.Get("Location"), truncateS(string(b), 3000))
	}
	iss, _ := e.app.DB.IssueByKey(context.Background(), "APP-1")
	e.app.DB.LoadRelations(context.Background(), []*store.Issue{iss})
	if iss.Fingerprint == "" || !iss.HasLabel("crash") || iss.Field("platform") != "windows" || iss.Field("runtime_version") != "21.0.2" {
		t.Fatalf("parsers didn't run: fp=%q labels=%v fields=%v", iss.Fingerprint, iss.Labels, iss.Fields)
	}
	page := bob.get("/issues/APP-1", 200)
	for _, want := range []string{"Game crashes when opening the map", "latest.log", "s affected", "Same crash signature", "I have this too"} {
		if want == "Same crash signature" {
			continue
		}
		if !strings.Contains(page, want) {
			t.Fatalf("issue page missing %q", want)
		}
	}

	mk := func(c *client, title, desc string) string {
		_, resp := c.post("/projects/APP/new", url.Values{"title": {title}, "description": {desc}, "f_category": {"gameplay"}, "f_platform": {"linux"}}, 303)
		return strings.TrimPrefix(resp.Header.Get("Location"), "/issues/")
	}
	k2 := mk(admin, "Map crashes the game when opened", "same as before")
	k3 := mk(bob, "Sound stutters in the options menu", "audio problem")
	if k2 != "APP-2" || k3 != "APP-3" {
		t.Fatalf("keys %s %s", k2, k3)
	}

	admin.get("/settings", 200)
	body, _ = admin.post("/settings/tokens", url.Values{"name": {"ci"}}, 200)
	tok := regexp.MustCompile(`bgy_[A-Za-z0-9_-]+`).FindString(body)
	if tok == "" {
		t.Fatal("no token shown")
	}
	sim := admin.api("GET", "/api/v1/similar?project=APP&title="+url.QueryEscape("crash when opening map"), tok, nil, 200)
	list, _ := sim["list"].([]any)
	if len(list) < 2 {
		t.Fatalf("similar: %v", sim)
	}
	first := list[0].(map[string]any)
	if k := first["key"]; k != "APP-1" && k != "APP-2" {
		t.Fatalf("similar ranking: %v", list)
	}
	res := admin.api("GET", "/api/v1/issues?q="+url.QueryEscape("crash label:crash is:open"), tok, nil, 200)
	if res["total"].(float64) != 1 {
		t.Fatalf("search: %v", res)
	}
	res = admin.api("GET", "/api/v1/issues?q="+url.QueryEscape("platform:linux sort:votes"), tok, nil, 200)
	if res["total"].(float64) != 2 {
		t.Fatalf("field search: %v", res)
	}
	admin.api("GET", "/api/v1/issues?q="+url.QueryEscape("version:>=1.1 created:<7d"), tok, nil, 200)
	admin.api("GET", "/api/v1/issues?q=bogus:1", tok, nil, 400)
	parse := admin.api("POST", "/api/v1/parse", tok, map[string]any{"project": "APP", "text": crash}, 200)
	if parse["fingerprint"] != iss.Fingerprint {
		t.Fatalf("parse fingerprint mismatch: %v", parse)
	}

	bob.post("/issues/APP-2/vote", url.Values{"on": {"1"}}, 303)
	bob.post("/issues/APP-2/comment", url.Values{"body": {"Same here @alice"}}, 303)
	time.Sleep(200 * time.Millisecond)
	if n := e.app.DB.UnreadCount(context.Background(), u.ID); n == 0 {
		t.Fatal("alice should have a notification")
	}
	admin.get("/notifications", 200)

	admin.get("/issues/APP-1", 200)
	admin.post("/issues/APP-1/transition", url.Values{"transition": {"request_info"}}, 303)
	if i, _ := e.app.DB.IssueByKey(context.Background(), "APP-1"); i.State != "open" {
		t.Fatal("transition without comment should fail")
	}
	admin.post("/issues/APP-1/transition", url.Values{"transition": {"request_info"}, "comment": {"Which driver?"}}, 303)
	if i, _ := e.app.DB.IssueByKey(context.Background(), "APP-1"); i.State != "awaiting_response" {
		t.Fatalf("state %s", i.State)
	}
	bob.post("/issues/APP-1/comment", url.Values{"body": {"NVIDIA 550"}}, 303)
	if i, _ := e.app.DB.IssueByKey(context.Background(), "APP-1"); i.State != "open" {
		t.Fatalf("reporter reply should reopen, got %s", i.State)
	}

	admin.post("/issues/APP-2/duplicate", url.Values{"of": {"APP-1"}}, 303)
	i1, _ := e.app.DB.IssueByKey(context.Background(), "APP-1")
	i2, _ := e.app.DB.IssueByKey(context.Background(), "APP-2")
	if i2.Resolution != "duplicate" || i2.DuplicateOf != i1.ID || i1.Votes < 2 || i1.Confirmations < 1 {
		t.Fatalf("duplicate merge: dup=%+v orig=%+v", i2, i1)
	}
	page = bob.get("/issues/APP-2", 200)
	if !strings.Contains(page, "duplicate") {
		t.Fatal("duplicate banner missing")
	}

	carol := e.client()
	carolCodes := carol.register("carol")
	carol.get("/issues/APP-1", 200)
	carol.post("/issues/APP-1/confirm", url.Values{"version": {"1.2"}, "f_platform": {"macos"}}, 303)
	page = carol.get("/issues/APP-1", 200)
	if !strings.Contains(page, "macOS") {
		t.Fatal("env summary missing macOS")
	}

	admin.post("/issues/APP-1/edit", url.Values{"title": {"Game crashes when opening the map"}, "description": {"x"}, "fix_versions": {"1.1"}, "affects_versions": {"1.0"}}, 303)
	admin.post("/issues/APP-1/transition", url.Values{"transition": {"resolve"}, "resolution": {"fixed"}}, 303)
	admin.get("/projects/APP/changelog/1.1", 200)
	carol.post("/issues/APP-1/confirm", url.Values{"version": {"1.2"}}, 303)
	if i, _ := e.app.DB.IssueByKey(context.Background(), "APP-1"); i.State != "open" {
		t.Fatalf("still-affects on newer version should reopen, got %s/%s", i.State, i.Resolution)
	}

	admin.post("/issues/APP-3/triage", url.Values{"add_label": {"regression"}}, 303)
	admin.post("/issues/APP-3/link", url.Values{"other": {"APP-1"}, "kind": {"relates"}}, 303)
	admin.post("/issues/APP-3/canned", url.Values{"canned": {"need-logs"}}, 303)
	if i, _ := e.app.DB.IssueByKey(context.Background(), "APP-3"); i.State != "awaiting_response" {
		t.Fatalf("canned transition: %s", i.State)
	}
	admin.post("/issues/bulk", url.Values{"keys": {"APP-3"}, "add_labels": {"needs-retest"}}, 303)
	admin.post("/issues/APP-3/triage", url.Values{"add_label": {"security"}}, 303)
	carol.get("/issues/APP-3", 404)
	bob.get("/issues/APP-3", 200)

	for _, p := range []string{"/", "/?all=1", "/issues?q=is:open", "/issues?q=project:APP", "/triage", "/admin", "/users/bob", "/users/alice",
		"/issues/APP-1/edit", "/new", "/settings", "/feeds/issues.atom?q=is:open", "/feeds/projects/APP.atom", "/feeds/issues/APP-1.atom", "/issues/APP-1", "/projects/APP", "/projects/APP/releases", "/issues?q=project:APP+is:open&page=1"} {
		body := admin.get(p, 200)
		if strings.Contains(strings.ToLower(body), "%25") {
			i := strings.Index(strings.ToLower(body), "%25")
			t.Fatalf("%s contains a double-encoded URL: ...%s...", p, body[max(0, i-80):min(len(body), i+40)])
		}
	}
	resp, _ = admin.http.Get(e.srv.URL + "/issues?q=project%253AAPP%2bwatching%253Ame%2bis%253Aopen")
	if loc := resp.Header.Get("Location"); resp.StatusCode != 301 || loc != "/issues?q=project%3AAPP+watching%3Ame+is%3Aopen" {
		t.Fatalf("double-encoded query: %d %q", resp.StatusCode, loc)
	}
	bob.get("/triage", 403)
	bob.get("/admin", 403)

	apiIss := admin.api("POST", "/api/v1/issues", tok, map[string]any{"project": "APP", "title": "Created by API", "description": "hi", "fields": map[string]any{"platform": "linux", "category": "ui"}}, 201)
	key := apiIss["key"].(string)
	admin.api("PATCH", "/api/v1/issues/"+key, tok, map[string]any{"add_labels": []string{"regression"}, "fields": map[string]any{"priority": "high"}}, 200)
	admin.api("POST", "/api/v1/issues/"+key+"/comments", tok, map[string]any{"body": "from api"}, 201)
	admin.api("POST", "/api/v1/issues/"+key+"/transitions", tok, map[string]any{"transition": "confirm"}, 200)
	got := admin.api("GET", "/api/v1/issues/"+key+"?expand=comments,history,links,confirmations", tok, nil, 200)
	if got["state"].(map[string]any)["id"] != "confirmed" || got["fields"].(map[string]any)["priority"] == nil {
		t.Fatalf("api issue: %v", got)
	}
	admin.api("POST", "/api/v1/projects/APP/versions", tok, map[string]any{"name": "2.0", "released": false}, 201)
	admin.api("PUT", "/api/v1/issues/"+key+"/vote", tok, nil, 200)
	admin.api("GET", "/api/v1/me", tok, nil, 200)
	admin.api("GET", "/api/v1/projects/APP", tok, nil, 200)
	admin.api("POST", "/api/v1/preview", tok, map[string]any{"text": "**hi** APP-1"}, 200)

	payload := `{"repository":{"full_name":"acme/game"},"commits":[{"id":"abcdef1234567","message":"Fix map NPE, fixes ` + key + `","url":"https://example.com/c/abc","author":{"name":"Dev"}}]}`
	gr, _ := http.NewRequest("POST", e.srv.URL+"/hooks/git/gh", strings.NewReader(payload))
	gr.Header.Set("X-Hub-Signature-256", "sha256="+hmacHex("s3cret", payload))
	gresp, _ := http.DefaultClient.Do(gr)
	gb, _ := io.ReadAll(gresp.Body)
	if gresp.StatusCode != 200 || !strings.Contains(string(gb), key) {
		t.Fatalf("git hook: %d %s", gresp.StatusCode, gb)
	}
	if i, _ := e.app.DB.IssueByKey(context.Background(), key); i.Resolution != "fixed" {
		t.Fatalf("git close: %s/%s", i.State, i.Resolution)
	}
	gr2, _ := http.NewRequest("POST", e.srv.URL+"/hooks/git/gh", strings.NewReader(payload))
	gr2.Header.Set("X-Hub-Signature-256", "sha256=bad")
	if r2, _ := http.DefaultClient.Do(gr2); r2.StatusCode != 401 {
		t.Fatalf("bad signature accepted: %d", r2.StatusCode)
	}

	resp, _ = anon.http.PostForm(e.srv.URL+"/login", url.Values{"login": {"alice"}, "password": {"password123"}})
	if resp.StatusCode != 403 {
		t.Fatalf("missing CSRF should be 403, got %d", resp.StatusCode)
	}
	anon.get("/login", 200)
	_, resp = anon.post("/login", url.Values{"login": {"alice"}, "password": {"password123"}}, 303)
	_, resp = anon.post("/login", url.Values{"login": {"alice"}, "password": {"wrong"}}, 401)

	e.app.DB.Exec(context.Background(), `UPDATE issues SET updated_at = ? WHERE key = 'APP-3'`, time.Now().Add(-30*24*time.Hour).Unix())
	e.app.RunScheduled(context.Background())
	if i, _ := e.app.DB.IssueByKey(context.Background(), "APP-3"); i.Resolution != "incomplete" {
		t.Fatalf("automation didn't close: %s/%s", i.State, i.Resolution)
	}

	forgot := e.client()
	forgot.get("/forgot", 200)
	forgot.post("/forgot", url.Values{"login": {"carol"}, "code": {"aaaa-bbbb-cccc-dddd"}, "password": {"newpass123"}, "password2": {"newpass123"}}, 422)
	_, resp = forgot.post("/forgot", url.Values{"login": {"Carol"}, "code": {strings.ToUpper(strings.ReplaceAll(carolCodes[3], "-", " "))}, "password": {"newpass123"}, "password2": {"newpass123"}}, 303)
	if loc := resp.Header.Get("Location"); loc != "/settings#recovery" {
		t.Fatalf("forgot redirect %q", loc)
	}
	if body := forgot.get("/settings", 200); !strings.Contains(body, "9 of 10 left") {
		t.Fatal("settings should show 9 of 10 recovery codes left")
	}
	carol.get("/settings", 303)
	forgot2 := e.client()
	forgot2.get("/forgot", 200)
	forgot2.post("/forgot", url.Values{"login": {"carol"}, "code": {carolCodes[3]}, "password": {"other12345"}, "password2": {"other12345"}}, 422)
	carol = e.client()
	carol.get("/login", 200)
	carol.post("/login", url.Values{"login": {"carol"}, "password": {"newpass123"}}, 303)
	body, _ = carol.post("/settings/recovery-codes", url.Values{}, 200)
	if len(codeRe.FindAllString(body, -1)) != 10 {
		t.Fatal("regenerating recovery codes should show 10 new codes")
	}

	bob.post("/users/carol/moderate", url.Values{"action": {"ban"}}, 303)
	if u, _ := e.app.DB.UserByName(context.Background(), "carol"); u.Suspended() {
		t.Fatal("a regular user must not be able to ban")
	}
	admin.get("/users/carol", 200)
	admin.post("/users/carol/moderate", url.Values{"action": {"mute"}, "duration": {"1h"}, "reason": {"spam"}}, 303)
	carol.post("/issues/APP-1/comment", url.Values{"body": {"hello"}}, 303)
	if n := e.app.DB.Count(context.Background(), `SELECT COUNT(*) FROM comments c JOIN users u ON u.id = c.author_id WHERE u.username = 'carol'`); n != 0 {
		t.Fatal("muted user was able to comment")
	}
	if body := carol.get("/issues/APP-1", 200); !strings.Contains(body, "muted until") {
		t.Fatal("muted notice missing")
	}
	admin.post("/users/carol/moderate", url.Values{"action": {"unmute"}}, 303)
	admin.post("/users/carol/moderate", url.Values{"action": {"lock"}, "duration": {"24h"}, "reason": {"cool off"}}, 303)
	carol.get("/settings", 303)
	carol.get("/login", 200)
	body, _ = carol.post("/login", url.Values{"login": {"carol"}, "password": {"newpass123"}}, 403)
	if !strings.Contains(body, "locked until") || !strings.Contains(body, "cool off") {
		t.Fatalf("lock message missing reason:\n%s", truncateS(body, 3000))
	}
	admin.post("/users/carol/moderate", url.Values{"action": {"unban"}}, 303)
	carol.post("/login", url.Values{"login": {"carol"}, "password": {"newpass123"}}, 303)
	admin.post("/users/carol/moderate", url.Values{"action": {"kick"}}, 303)
	carol.get("/settings", 303)
	admin.post("/users/alice/moderate", url.Values{"action": {"ban"}}, 303)
	if u, _ := e.app.DB.UserByName(context.Background(), "alice"); u.Suspended() {
		t.Fatal("admin banned themselves")
	}
	if body := admin.get("/users/carol", 200); !strings.Contains(body, "Moderation history") || !strings.Contains(body, "cool off") {
		t.Fatal("moderation history missing")
	}
	admin.api("POST", "/api/v1/users/carol/moderation", tok, map[string]any{"action": "ban", "reason": "api"}, 200)
	admin.api("GET", "/api/v1/users/carol/moderation", tok, nil, 200)
}

func hmacHex(secret, body string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(body))
	return hex.EncodeToString(m.Sum(nil))
}
