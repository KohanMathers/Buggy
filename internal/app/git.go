package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/kohanmathers/buggy/internal/config"
	"github.com/kohanmathers/buggy/internal/markdown"
	"github.com/kohanmathers/buggy/internal/store"
)

type pushPayload struct {
	Repository struct {
		FullName string `json:"full_name"`
		HTMLURL  string `json:"html_url"`
	} `json:"repository"`
	Project struct {
		Path   string `json:"path_with_namespace"`
		WebURL string `json:"web_url"`
	} `json:"project"`
	Commits []struct {
		ID      string `json:"id"`
		Message string `json:"message"`
		URL     string `json:"url"`
		Author  struct {
			Name     string `json:"name"`
			Username string `json:"username"`
		} `json:"author"`
	} `json:"commits"`
}

func verifyHMAC(secret string, body []byte, sig string) bool {
	sig = strings.TrimPrefix(sig, "sha256=")
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	want := hex.EncodeToString(m.Sum(nil))
	return subtle.ConstantTimeCompare([]byte(want), []byte(sig)) == 1
}

type GitHookResult struct {
	Linked []string `json:"linked"`
	Closed []string `json:"closed"`
}

func (a *App) HandleGitHook(ctx context.Context, id string, h http.Header, body []byte) (*GitHookResult, error) {
	cfg := a.Cfg()
	var g *config.GitIntegration
	for i := range cfg.Git {
		if cfg.Git[i].ID == id {
			g = &cfg.Git[i]
		}
	}
	if g == nil {
		return nil, ErrNotFound
	}
	if g.Secret != "" {
		ok := false
		switch g.Provider {
		case "github":
			ok = verifyHMAC(g.Secret, body, h.Get("X-Hub-Signature-256"))
		case "gitea":
			ok = verifyHMAC(g.Secret, body, h.Get("X-Gitea-Signature")) || verifyHMAC(g.Secret, body, h.Get("X-Hub-Signature-256"))
		case "gitlab":
			ok = subtle.ConstantTimeCompare([]byte(h.Get("X-Gitlab-Token")), []byte(g.Secret)) == 1
		default:
			ok = verifyHMAC(g.Secret, body, h.Get("X-Buggy-Signature"))
		}
		if !ok {
			return nil, Errf(401, "invalid signature")
		}
	}
	if ev := h.Get("X-GitHub-Event"); ev == "ping" {
		return &GitHookResult{}, nil
	}
	var p pushPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, Errf(400, "invalid JSON: %v", err)
	}
	repo := p.Repository.FullName
	if repo == "" {
		repo = p.Project.Path
	}
	res := &GitHookResult{}
	sys := a.SystemActor()
	for _, c := range p.Commits {
		for _, ref := range markdown.IssueRefs(c.Message) {
			pk, _, _ := strings.Cut(ref, "-")
			if cfg.Project(pk) == nil || (len(g.Projects) > 0 && !matchListFold(g.Projects, pk)) {
				continue
			}
			iss, err := a.GetIssue(ctx, sys, ref)
			if err != nil {
				continue
			}
			author := c.Author.Name
			if c.Author.Username != "" {
				author = c.Author.Username
			}
			first, _, _ := strings.Cut(c.Message, "\n")
			added, err := a.DB.AddCommit(ctx, &store.Commit{IssueID: iss.ID, Repo: repo, SHA: c.ID, Message: first, URL: c.URL, Author: author})
			if err != nil || !added {
				continue
			}
			res.Linked = append(res.Linked, ref)
			a.DB.AddHistory(ctx, a.DB, iss.ID, sys.ID(), "commit", "", shortSHA(c.ID))
			if iss.ResolvedAt == 0 && closes(g.CloseKeywords, c.Message, ref) {
				wf := cfg.ProjectWorkflow(pk)
				state := g.CloseState
				if state == "" || wf.State(state) == nil {
					state = wf.FirstStateIn("closed")
				}
				if state == "" {
					continue
				}
				resolution := g.CloseResolution
				if wf.Resolution(resolution) == nil {
					resolution = ""
				}
				a.addComment(ctx, sys, iss, fmt.Sprintf("Fixed in [`%s`](%s) (%s) by %s: %s", shortSHA(c.ID), c.URL, repo, author, first), false, true)
				if err := a.applyState(ctx, sys, iss, state, resolution); err == nil {
					res.Closed = append(res.Closed, ref)
				}
			}
		}
	}
	return res, nil
}

func shortSHA(s string) string {
	if len(s) > 10 {
		return s[:10]
	}
	return s
}

func closes(keywords []string, msg, ref string) bool {
	if len(keywords) == 0 {
		return false
	}
	kw := make([]string, len(keywords))
	for i, k := range keywords {
		kw[i] = regexp.QuoteMeta(k)
	}
	re, err := regexp.Compile(`(?i)\b(?:` + strings.Join(kw, "|") + `)\b:?\s+(?:[A-Z][A-Z0-9]*-\d+[\s,]+(?:and\s+)?)*` + regexp.QuoteMeta(ref) + `\b`)
	if err != nil {
		return false
	}
	return re.MatchString(msg)
}
