package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStarterValid(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yml")
	os.WriteFile(p, StarterYAML, 0o644)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Project("app") == nil || cfg.Field("platform") == nil {
		t.Fatal("index missing")
	}
	if !cfg.RoleHas("moderator", "issue.vote") || cfg.RoleHas("user", "triage") || !cfg.RoleHas("admin", "anything") {
		t.Fatal("perms wrong")
	}
	if len(cfg.ProjectForm("APP").Steps) != 3 {
		t.Fatal("form")
	}
}

func TestDefaultsOnly(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Workflow("default") == nil {
		t.Fatal("no default workflow")
	}
}

func TestConfigDAndErrors(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yml")
	os.WriteFile(p, []byte("projects:\n  - key: A\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "config.d"), 0o755)
	os.WriteFile(filepath.Join(dir, "config.d", "x.yml"), []byte("projects:\n  - key: B\n    workflow: nope\n"), 0o644)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error")
	}
	t.Log(err)
	os.WriteFile(p, []byte("instanse: {}\n"), 0o644)
	os.Remove(filepath.Join(dir, "config.d", "x.yml"))
	if _, err := Load(p); err == nil {
		t.Fatal("expected unknown field error")
	} else {
		t.Log(err)
	}
}

func TestThemeValidation(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yml")
	os.WriteFile(p, []byte("instance:\n  theme:\n    colors: { nope: red, topbar: \"url(evil)\" }\n    font_url: \"javascript:x\"\n"), 0o644)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected theme validation errors")
	}
	for _, want := range []string{"colors.nope: unknown colour", "colors.topbar", "font_url"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q:\n%v", want, err)
		}
	}
}
