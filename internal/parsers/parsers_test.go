package parsers

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kohanmathers/buggy/internal/config"
)

const mcCrash = `---- Minecraft Crash Report ----
// Who set us up the TNT?

Time: 2026-10-01 12:00:00
Description: Rendering overlay

java.lang.NullPointerException: Cannot invoke "Object.toString()" because "x" is null
	at java.base/java.util.Objects.requireNonNull(Objects.java:233)
	at net.minecraft.client.gui.MapScreen.render(MapScreen.java:120)
	at net.minecraft.client.render.GameRenderer.render(GameRenderer.java:880)
	at net.minecraft.client.MinecraftClient.render(MinecraftClient.java:1200)

-- System Details --
	Minecraft Version: 1.21.3
	Operating System: Windows 11 (amd64) version 10.0
	Java Version: 21.0.4, Microsoft
	Fabric Mods:
		fabric-api: Fabric API 0.106.1
		sodium: Sodium 0.6.0
`

func TestModdingPack(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.yml"), config.StarterYAML, 0o644)
	os.MkdirAll(filepath.Join(dir, "config.d"), 0o755)
	pack, err := os.ReadFile("../../examples/config.d/modding.yml")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "config.d", "modding.yml"), pack, 0o644)
	cfg, err := config.Load(filepath.Join(dir, "config.yml"))
	if err != nil {
		t.Fatal(err)
	}
	r := Run(cfg, "APP", []Input{{Name: "crash-2026.txt", Content: mcCrash}})
	want := map[string]string{"game_version": "1.21.3", "platform": "windows", "mod_loader": "fabric"}
	for k, v := range want {
		if got := r.Fields[k]; len(got) == 0 || got[0] != v {
			t.Errorf("%s = %v, want %s", k, got, v)
		}
	}
	if len(r.Fields["mods"]) != 2 {
		t.Errorf("mods = %v", r.Fields["mods"])
	}
	if r.Fingerprint == "" || r.Signature != "java.lang.NullPointerException at net.minecraft.client.gui.MapScreen.render" {
		t.Errorf("fingerprint %q signature %q", r.Fingerprint, r.Signature)
	}
	r2 := Run(cfg, "APP", []Input{{Content: "noise\n" + mcCrash + "\nmore noise"}})
	if r2.Fingerprint != r.Fingerprint {
		t.Errorf("fingerprint not stable: %s vs %s", r.Fingerprint, r2.Fingerprint)
	}
	t.Logf("labels=%v matched=%v", r.Labels, r.Matched)
}
