// Package parsers runs the YAML-configured log/crash parsers over issue text and attachments.
package parsers

import (
	"crypto/sha1"
	"encoding/hex"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/kohanmathers/buggy/internal/config"
)

type Input struct {
	Name    string
	Content string
}

type Result struct {
	Fields      map[string][]string `json:"fields"`
	Labels      []string            `json:"labels"`
	Fingerprint string              `json:"fingerprint,omitempty"`
	Signature   string              `json:"signature,omitempty"`
	Matched     []string            `json:"matched"`
}

var cache sync.Map

func re(p string) *regexp.Regexp {
	if v, ok := cache.Load(p); ok {
		return v.(*regexp.Regexp)
	}
	r, err := regexp.Compile(p)
	if err != nil {
		r = regexp.MustCompile(`$^`)
	}
	cache.Store(p, r)
	return r
}

func inProject(p *config.Parser, project string) bool {
	if len(p.Projects) == 0 {
		return true
	}
	for _, k := range p.Projects {
		if strings.EqualFold(k, project) {
			return true
		}
	}
	return false
}

func nameMatches(p *config.Parser, name string) bool {
	if len(p.Match.Filename) == 0 {
		return true
	}
	if name == "" {
		return false
	}
	base := strings.ToLower(filepath.Base(name))
	for _, g := range p.Match.Filename {
		if ok, _ := filepath.Match(strings.ToLower(g), base); ok {
			return true
		}
	}
	return false
}

func Run(cfg *config.Config, project string, inputs []Input) Result {
	res := Result{Fields: map[string][]string{}}
	labels := map[string]bool{}
	for pi := range cfg.Parsers {
		p := &cfg.Parsers[pi]
		if !inProject(p, project) {
			continue
		}
		matched := false
		for _, in := range inputs {
			if in.Content == "" || !nameMatches(p, in.Name) {
				continue
			}
			if p.Match.Content != "" && !re(p.Match.Content).MatchString(in.Content) {
				continue
			}
			matched = true
			for _, ex := range p.Extract {
				if len(res.Fields[ex.Field]) > 0 {
					continue
				}
				vals := extract(cfg.Field(ex.Field), ex, in.Content)
				if len(vals) > 0 {
					res.Fields[ex.Field] = vals
				}
			}
			if p.Fingerprint != nil && res.Fingerprint == "" {
				res.Fingerprint, res.Signature = fingerprint(p.Fingerprint, in.Content)
			}
		}
		if matched {
			res.Matched = append(res.Matched, p.Name)
			for _, l := range p.Labels {
				if !labels[l] {
					labels[l] = true
					res.Labels = append(res.Labels, l)
				}
			}
		}
	}
	return res
}

func extract(f *config.Field, ex config.Extract, content string) []string {
	r := re(ex.Regex)
	n := 1
	if ex.All {
		n = -1
	}
	var out []string
	seen := map[string]bool{}
	for _, m := range r.FindAllStringSubmatch(content, n) {
		if ex.Group >= len(m) {
			continue
		}
		v := strings.TrimSpace(m[ex.Group])
		if v == "" {
			continue
		}
		for k, mapped := range ex.Map {
			if strings.EqualFold(k, v) {
				v = mapped
				break
			}
		}
		if f != nil && (f.Type == "select" || f.Type == "multiselect") {
			norm := ""
			for _, o := range f.Options {
				if strings.EqualFold(o.Value, v) || strings.EqualFold(o.Label, v) {
					norm = o.Value
					break
				}
			}
			if norm == "" {
				continue
			}
			v = norm
		}
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
		if f != nil && !f.Multi() && f.Type != "textarea" && len(out) == 1 && !ex.All {
			break
		}
	}
	return out
}

func fingerprint(fp *config.Fingerprint, content string) (string, string) {
	exc := ""
	if fp.Exception != "" {
		if m := re(fp.Exception).FindStringSubmatch(content); m != nil {
			exc = m[len(m)-1]
		}
	}
	var frames []string
	if fp.Frames != "" {
	outer:
		for _, m := range re(fp.Frames).FindAllStringSubmatch(content, -1) {
			f := m[len(m)-1]
			for _, ig := range fp.Ignore {
				if re(ig).MatchString(f) {
					continue outer
				}
			}
			frames = append(frames, f)
			if len(frames) >= fp.Count {
				break
			}
		}
	}
	if exc == "" && len(frames) == 0 {
		return "", ""
	}
	h := sha1.Sum([]byte(exc + "\n" + strings.Join(frames, "\n")))
	sig := exc
	if len(frames) > 0 {
		if sig != "" {
			sig += " at "
		}
		sig += frames[0]
	}
	return hex.EncodeToString(h[:])[:12], sig
}
