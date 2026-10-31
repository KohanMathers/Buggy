package config

import (
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

type Holder struct {
	path      string
	cur       atomic.Pointer[Config]
	mu        sync.Mutex
	lastErr   error
	lastLoad  time.Time
	mtimes    map[string]time.Time
	listeners []func(*Config)
	overrides []func(*Config)
}

func (h *Holder) Override(fn func(*Config)) {
	h.mu.Lock()
	h.overrides = append(h.overrides, fn)
	h.mu.Unlock()
	cfg := *h.cur.Load()
	fn(&cfg)
	h.cur.Store(&cfg)
}

func NewHolder(path string, cfg *Config) *Holder {
	h := &Holder{path: path, lastLoad: time.Now()}
	h.cur.Store(cfg)
	h.mtimes = h.snapshot()
	return h
}

func (h *Holder) Get() *Config { return h.cur.Load() }
func (h *Holder) Path() string { return h.path }

func (h *Holder) Status() (error, time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastErr, h.lastLoad
}

func (h *Holder) OnReload(fn func(*Config)) {
	h.mu.Lock()
	h.listeners = append(h.listeners, fn)
	h.mu.Unlock()
}

func (h *Holder) snapshot() map[string]time.Time {
	m := map[string]time.Time{}
	for _, f := range ConfigFiles(h.path) {
		if st, err := os.Stat(f); err == nil {
			m[f] = st.ModTime()
		}
	}
	return m
}

func (h *Holder) changed(now map[string]time.Time) bool {
	if len(now) != len(h.mtimes) {
		return true
	}
	for f, t := range now {
		if !h.mtimes[f].Equal(t) {
			return true
		}
	}
	return false
}

func (h *Holder) Reload() error {
	cfg, err := Load(h.path)
	h.mu.Lock()
	h.mtimes = h.snapshot()
	h.lastErr = err
	if err != nil {
		h.mu.Unlock()
		return err
	}
	h.lastLoad = time.Now()
	for _, fn := range h.overrides {
		fn(cfg)
	}
	listeners := append([]func(*Config){}, h.listeners...)
	h.mu.Unlock()
	h.cur.Store(cfg)
	for _, fn := range listeners {
		fn(cfg)
	}
	return nil
}

func (h *Holder) Watch(stop <-chan struct{}) {
	iv := h.Get().Server.ReloadInterval.D()
	if iv <= 0 {
		iv = 2 * time.Second
	}
	t := time.NewTicker(iv)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			snap := h.snapshot()
			h.mu.Lock()
			ch := h.changed(snap)
			h.mu.Unlock()
			if !ch {
				continue
			}
			if err := h.Reload(); err != nil {
				slog.Error("config reload failed; keeping previous config", "err", err)
			} else {
				slog.Info("config reloaded")
			}
		}
	}
}
