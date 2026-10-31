// Command buggy is a self-hosted, YAML-configured bug tracker.
package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kohanmathers/buggy/internal/app"
	"github.com/kohanmathers/buggy/internal/backup"
	"github.com/kohanmathers/buggy/internal/config"
	"github.com/kohanmathers/buggy/internal/importer"
	"github.com/kohanmathers/buggy/internal/server"
	"github.com/kohanmathers/buggy/internal/storage"
	"github.com/kohanmathers/buggy/internal/store"
	"github.com/kohanmathers/buggy/web"

	"crypto/rand"

	"gopkg.in/yaml.v3"
)

var version = "1.0.0"

const usage = `Buggy: a self-hosted bug tracker.

Usage:
  buggy <command> [flags]

Commands:
  serve                 Run the web server (default)
  init [path]           Write a starter config.yml
  config validate       Check the configuration and exit
  config defaults       Print the built-in default configuration
  config show           Print the effective (merged) configuration
  user create <name>    Create a user (--password, --email, --role)
  user role <name> <r>  Change a user's role
  user password <name>  Reset a user's password (--password)
  user recovery-codes <name>   Issue new password recovery codes for a user
  user kick|lock|ban|unban|mute|unmute <name> [--for 7d] [--reason "..."]
                        Account controls (lock and mute take --for)
  backup <file.tar.gz>  Back up database rows, attachments and config
  restore <file.tar.gz> Restore a backup into an EMPTY database (any driver)
  import jira           Import from a Jira instance (see --help)
  import github         Import from a GitHub repository (see --help)
  reindex               Rebuild the full-text search index
  automations run       Run scheduled automations once
  test-email <to>       Send a test email using notifications.email
  version               Print the version

Global flags (before or after the command):
  --config PATH   Config file (env BUGGY_CONFIG, default ./config.yml)
`

func main() {
	args := os.Args[1:]
	cfgPath := os.Getenv("BUGGY_CONFIG")
	if cfgPath == "" {
		cfgPath = "config.yml"
	}
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--config" || a == "-config" || a == "-c":
			if i+1 < len(args) {
				cfgPath = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--config="):
			cfgPath = strings.TrimPrefix(a, "--config=")
		case a == "-h" || a == "--help" || a == "help":
			if len(rest) == 0 {
				fmt.Print(usage)
				return
			}
			rest = append(rest, a)
		default:
			rest = append(rest, a)
		}
	}
	cmd := "serve"
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		cmd, rest = rest[0], rest[1:]
	}
	setupLogging()
	var err error
	switch cmd {
	case "serve":
		err = serve(cfgPath, rest)
	case "init":
		err = initConfig(cfgPath, rest)
	case "config":
		err = configCmd(cfgPath, rest)
	case "user":
		err = userCmd(cfgPath, rest)
	case "backup":
		err = backupCmd(cfgPath, rest)
	case "restore":
		err = restoreCmd(cfgPath, rest)
	case "import":
		err = importCmd(cfgPath, rest)
	case "reindex":
		err = withApp(cfgPath, func(a *app.App) error {
			n, err := a.DB.ReindexAll(context.Background())
			fmt.Printf("reindexed %d issues\n", n)
			return err
		})
	case "automations":
		err = withApp(cfgPath, func(a *app.App) error {
			a.RunScheduled(context.Background())
			fmt.Println("scheduled automations ran")
			return nil
		})
	case "test-email":
		err = testEmail(cfgPath, rest)
	case "migrate":
		err = withApp(cfgPath, func(a *app.App) error {
			fmt.Printf("database schema is at version %d\n", a.DB.SchemaVersion(context.Background()))
			return nil
		})
	case "version", "--version", "-v":
		fmt.Println("buggy", version)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func setupLogging() {
	level := slog.LevelInfo
	switch strings.ToLower(os.Getenv("BUGGY_LOG")) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	var h slog.Handler = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	if os.Getenv("BUGGY_LOG_FORMAT") == "json" {
		h = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	}
	slog.SetDefault(slog.New(h))
}

func loadConfig(path string) (*config.Config, error) {
	cfg, err := config.Load(path)
	if err != nil {
		var ve *config.ValidationError
		if errors.As(err, &ve) {
			return nil, err
		}
		return nil, fmt.Errorf("loading %s: %w", path, err)
	}
	return cfg, nil
}

func openApp(path string) (*app.App, *config.Holder, error) {
	cfg, err := loadConfig(path)
	if err != nil {
		return nil, nil, err
	}
	h := config.NewHolder(path, cfg)
	db, err := store.Open(cfg.Database.Driver, cfg.Database.DSN)
	if err != nil {
		return nil, nil, err
	}
	files, err := storage.New(cfg.Storage)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	a, err := app.New(context.Background(), h, db, files, slog.Default())
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return a, h, nil
}

func withApp(path string, fn func(*app.App) error) error {
	a, _, err := openApp(path)
	if err != nil {
		return err
	}
	defer a.DB.Close()
	return fn(a)
}

func serve(cfgPath string, args []string) error {
	fl := flag.NewFlagSet("serve", flag.ExitOnError)
	dev := fl.Bool("dev", false, "load templates/static from --web-dir on every request (for editing HTML)")
	webDir := fl.String("web-dir", "", "web directory for --dev (default ./web)")
	listen := fl.String("listen", "", "listen address, overrides server.listen")
	fl.Parse(args)

	if _, err := os.Stat(cfgPath); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(cfgPath, config.StarterYAML, 0o644); err == nil {
			slog.Info("no config found; wrote a starter config", "path", cfgPath)
		} else {
			slog.Warn("no config found and could not write a starter config; running with built-in defaults", "path", cfgPath, "err", err)
		}
	}
	a, h, err := openApp(cfgPath)
	if err != nil {
		return err
	}
	defer a.DB.Close()
	h.Override(func(c *config.Config) {
		if *dev {
			c.Server.Dev = true
		}
		if *webDir != "" {
			c.Server.WebDir = *webDir
		}
		if *listen != "" {
			c.Server.Listen = *listen
		}
	})
	cfg := h.Get()
	if cfg.Server.Dev {
		if _, err := os.Stat(filepath.Join(cfg.Server.WebDir, "templates", "layout.html")); err != nil {
			return fmt.Errorf("dev mode: %s/templates/layout.html not found (set --web-dir)", cfg.Server.WebDir)
		}
		slog.Info("dev mode: templates and static files are read from disk", "dir", cfg.Server.WebDir)
	}
	secret, err := secretKey(a, cfg)
	if err != nil {
		return err
	}
	webFS, err := fs.Sub(web.FS, ".")
	if err != nil {
		return err
	}
	srv := server.New(a, webFS, secret, version)
	a.Start()
	stop := make(chan struct{})
	go h.Watch(stop)

	hs := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       5 * time.Minute,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	errc := make(chan error, 1)
	go func() {
		slog.Info("buggy is running", "version", version, "listen", cfg.Server.Listen, "url", cfg.Instance.URL,
			"database", cfg.Database.Driver, "config", strings.Join(cfg.Files, ", "))
		errc <- hs.ListenAndServe()
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case s := <-sig:
		slog.Info("shutting down", "signal", s.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	hs.Shutdown(ctx)
	close(stop)
	a.Close()
	return nil
}

func secretKey(a *app.App, cfg *config.Config) ([]byte, error) {
	if cfg.Server.SecretKey != "" {
		return []byte(cfg.Server.SecretKey), nil
	}
	ctx := context.Background()
	if v, err := a.DB.GetKV(ctx, "secret_key"); err == nil && v != "" {
		return hex.DecodeString(v)
	}
	b := make([]byte, 32)
	rand.Read(b)
	if err := a.DB.SetKV(ctx, "secret_key", hex.EncodeToString(b)); err != nil {
		return nil, err
	}
	return b, nil
}

func initConfig(cfgPath string, args []string) error {
	path := cfgPath
	if len(args) > 0 {
		path = args[0]
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	}
	if dir := filepath.Dir(path); dir != "." {
		os.MkdirAll(dir, 0o755)
	}
	if err := os.WriteFile(path, config.StarterYAML, 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s. Edit it, then run `buggy serve --config %s`\n", path, path)
	return nil
}

func configCmd(cfgPath string, args []string) error {
	sub := "validate"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "validate", "check":
		cfg, err := loadConfig(cfgPath)
		if err != nil {
			return err
		}
		fmt.Printf("✓ configuration is valid (%s)\n", strings.Join(cfg.Files, ", "))
		fmt.Printf("  %d projects, %d fields, %d workflows, %d parsers, %d automations, %d roles\n",
			len(cfg.Projects), len(cfg.Fields), len(cfg.Workflows), len(cfg.Parsers), len(cfg.Automations), len(cfg.Roles))
		return nil
	case "defaults":
		cfg, err := config.Load(filepath.Join(os.TempDir(), "buggy-nonexistent-config.yml"))
		if err != nil {
			return err
		}
		return dumpYAML(cfg)
	case "show":
		cfg, err := loadConfig(cfgPath)
		if err != nil {
			return err
		}
		return dumpYAML(cfg)
	}
	return fmt.Errorf("unknown config subcommand %q (validate, defaults, show)", sub)
}

func dumpYAML(v any) error {
	enc := yaml.NewEncoder(os.Stdout)
	enc.SetIndent(2)
	return enc.Encode(v)
}

func userCmd(cfgPath string, args []string) error {
	if len(args) < 2 {
		return errors.New("usage: buggy user create|role|password <username> [...]")
	}
	sub, name := args[0], args[1]
	fl := flag.NewFlagSet("user", flag.ExitOnError)
	pw := fl.String("password", "", "password (prompted from BUGGY_PASSWORD if empty)")
	email := fl.String("email", "", "email address")
	role := fl.String("role", "", "role (default: auth.default_role)")
	reason := fl.String("reason", "", "reason shown to the user (kick/lock/ban/mute)")
	forDur := fl.String("for", "", "duration for lock/mute, e.g. 1h, 7d, 2w (mute without --for is indefinite)")
	var extra []string
	if sub == "role" && len(args) > 2 {
		extra = args[2:3]
		fl.Parse(args[3:])
	} else {
		fl.Parse(args[2:])
	}
	if *pw == "" {
		*pw = os.Getenv("BUGGY_PASSWORD")
	}
	return withApp(cfgPath, func(a *app.App) error {
		ctx := context.Background()
		switch sub {
		case "create":
			if *pw == "" {
				*pw = app.RandomToken(12)
				fmt.Println("generated password:", *pw)
			}
			if *role != "" {
				if _, ok := a.Cfg().Roles[*role]; !ok {
					return fmt.Errorf("unknown role %q", *role)
				}
			}
			u, err := a.CreateLocalUser(ctx, name, *email, *pw, *role)
			if err != nil {
				return err
			}
			fmt.Printf("created user %s (role %s)\n", u.Username, u.Role)
		case "role":
			if len(extra) == 0 {
				return errors.New("usage: buggy user role <username> <role>")
			}
			r := extra[0]
			if _, ok := a.Cfg().Roles[r]; !ok {
				return fmt.Errorf("unknown role %q", r)
			}
			u, err := a.DB.UserByName(ctx, name)
			if err != nil {
				return fmt.Errorf("user %s not found", name)
			}
			u.Role = r
			if err := a.DB.UpdateUser(ctx, u); err != nil {
				return err
			}
			fmt.Printf("%s is now %s\n", u.Username, r)
		case "password":
			if *pw == "" {
				return errors.New("--password is required")
			}
			u, err := a.DB.UserByName(ctx, name)
			if err != nil {
				return fmt.Errorf("user %s not found", name)
			}
			h, err := app.HashPassword(*pw)
			if err != nil {
				return err
			}
			u.PasswordHash = h
			if err := a.DB.UpdateUser(ctx, u); err != nil {
				return err
			}
			a.DB.DeleteUserSessions(ctx, u.ID)
			fmt.Println("password updated")
		case "kick", "lock", "ban", "unban", "mute", "unmute", "shadowban", "unshadowban", "trust", "untrust", "revoke_tokens":
			d, err := app.ParseModDuration(*forDur)
			if err != nil {
				return err
			}
			u, err := a.ModerateUser(ctx, a.SystemActor(), name, app.ModerationRequest{Action: sub, Duration: d, Reason: *reason})
			if err != nil {
				return err
			}
			fmt.Printf("%s: %s done (suspended=%v muted=%v)\n", u.Username, sub, u.Suspended(), u.Muted())
		case "recovery-codes":
			u, err := a.DB.UserByName(ctx, name)
			if err != nil {
				return fmt.Errorf("user %s not found", name)
			}
			codes, err := a.IssueRecoveryCodes(ctx, u)
			if err != nil {
				return err
			}
			fmt.Printf("New recovery codes for %s (old codes no longer work):\n\n", u.Username)
			for _, c := range codes {
				fmt.Println("  " + c)
			}
		default:
			return fmt.Errorf("unknown user subcommand %q", sub)
		}
		return nil
	})
}

func backupCmd(cfgPath string, args []string) error {
	if len(args) < 1 {
		return errors.New("usage: buggy backup <file.tar.gz>")
	}
	return withApp(cfgPath, func(a *app.App) error {
		st, err := backup.Create(context.Background(), a.DB, a.Files, a.Cfg().Files, args[0])
		if err != nil {
			return err
		}
		fmt.Printf("backup written to %s (%d rows, %d files)\n", args[0], st.Rows, st.Files)
		return nil
	})
}

func restoreCmd(cfgPath string, args []string) error {
	if len(args) < 1 {
		return errors.New("usage: buggy restore <file.tar.gz>")
	}
	return withApp(cfgPath, func(a *app.App) error {
		st, err := backup.Restore(context.Background(), a.DB, a.Files, args[0])
		if err != nil {
			return err
		}
		fmt.Printf("restored %d rows and %d files; search index rebuilt\n", st.Rows, st.Files)
		return nil
	})
}

func importCmd(cfgPath string, args []string) error {
	if len(args) < 1 {
		return errors.New("usage: buggy import jira|github [flags]")
	}
	src := args[0]
	fl := flag.NewFlagSet("import "+src, flag.ExitOnError)
	project := fl.String("project", "", "target buggy project key (required)")
	var opts importer.Options
	switch src {
	case "jira":
		fl.StringVar(&opts.URL, "url", "", "Jira base URL, e.g. https://bugs.example.com")
		fl.StringVar(&opts.Query, "jql", "", "JQL query, e.g. 'project = MC ORDER BY key'")
		fl.StringVar(&opts.User, "user", "", "Jira username/email (optional)")
		fl.StringVar(&opts.Token, "token", "", "Jira API token/password (optional)")
	case "github":
		fl.StringVar(&opts.Repo, "repo", "", "owner/repo")
		fl.StringVar(&opts.Token, "token", os.Getenv("GITHUB_TOKEN"), "GitHub token (env GITHUB_TOKEN)")
	default:
		return fmt.Errorf("unknown import source %q", src)
	}
	fl.IntVar(&opts.Limit, "limit", 0, "maximum issues to import (0 = all)")
	fl.BoolVar(&opts.KeepNumbers, "keep-numbers", true, "keep original issue numbers when possible")
	statusMap := fl.String("status-map", "", "map source statuses to states, e.g. 'Open=open,Resolved=resolved'")
	resMap := fl.String("resolution-map", "", "map source resolutions, e.g. \"Won't Fix=wont_fix\"")
	fl.Parse(args[1:])
	if *project == "" {
		return errors.New("--project is required")
	}
	opts.Project = strings.ToUpper(*project)
	opts.StatusMap = parseMap(*statusMap)
	opts.ResolutionMap = parseMap(*resMap)
	return withApp(cfgPath, func(a *app.App) error {
		opts.Log = func(format string, v ...any) { fmt.Printf(format+"\n", v...) }
		var n int
		var err error
		if src == "jira" {
			n, err = importer.Jira(context.Background(), a, opts)
		} else {
			n, err = importer.GitHub(context.Background(), a, opts)
		}
		fmt.Printf("imported %d issues\n", n)
		return err
	})
}

func parseMap(s string) map[string]string {
	m := map[string]string{}
	for _, kv := range strings.Split(s, ",") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
	}
	return m
}

func testEmail(cfgPath string, args []string) error {
	if len(args) < 1 {
		return errors.New("usage: buggy test-email <address>")
	}
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		return err
	}
	if !cfg.Notifications.Email.Enabled {
		return errors.New("notifications.email.enabled is false")
	}
	if err := app.SendMail(cfg.Notifications.Email, args[0], cfg.Instance.Name+" test email", "If you can read this, email notifications work."); err != nil {
		return err
	}
	fmt.Println("sent")
	return nil
}
