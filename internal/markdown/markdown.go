// Package markdown renders user content safely and links issue keys and @mentions.
package markdown

import (
	"bytes"
	"html/template"
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
)

var (
	md = goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithRendererOptions(html.WithHardWraps()),
	)
	policy = func() *bluemonday.Policy {
		p := bluemonday.UGCPolicy()
		p.AllowAttrs("class").Matching(regexp.MustCompile(`^language-[\w+-]+$`)).OnElements("code")
		p.AllowAttrs("type", "checked", "disabled").OnElements("input")
		p.RequireNoFollowOnLinks(true)
		p.AddTargetBlankToFullyQualifiedLinks(true)
		return p
	}()
	keyRe     = regexp.MustCompile(`\b([A-Z][A-Z0-9]{0,15})-(\d+)\b`)
	mentionRe = regexp.MustCompile(`(^|[^\w@/.])@([A-Za-z0-9_][A-Za-z0-9_.-]{0,38}[A-Za-z0-9_]|[A-Za-z0-9_])`)
)

func Render(src string, isProject func(string) bool) template.HTML {
	var buf bytes.Buffer
	if err := md.Convert([]byte(src), &buf); err != nil {
		return template.HTML(template.HTMLEscapeString(src))
	}
	out := policy.SanitizeBytes(buf.Bytes())
	return template.HTML(linkify(string(out), isProject))
}

func Mentions(src string) []string {
	var out []string
	seen := map[string]bool{}
	inFence := false
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		line = stripInlineCode(line)
		for _, m := range mentionRe.FindAllStringSubmatch(line, -1) {
			u := strings.ToLower(m[2])
			if !seen[u] {
				seen[u] = true
				out = append(out, m[2])
			}
		}
	}
	return out
}

func IssueRefs(src string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range keyRe.FindAllString(src, -1) {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

func stripInlineCode(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		if r == '`' {
			in = !in
			continue
		}
		if !in {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func linkify(h string, isProject func(string) bool) string {
	var b strings.Builder
	skip := 0
	for len(h) > 0 {
		lt := strings.IndexByte(h, '<')
		if lt < 0 {
			b.WriteString(rewrite(h, skip > 0, isProject))
			break
		}
		b.WriteString(rewrite(h[:lt], skip > 0, isProject))
		gt := strings.IndexByte(h[lt:], '>')
		if gt < 0 {
			b.WriteString(h[lt:])
			break
		}
		tag := h[lt : lt+gt+1]
		name := ""
		if f := strings.Fields(strings.Trim(tag, "<>/")); len(f) > 0 {
			name = strings.ToLower(f[0])
		}
		if name == "a" || name == "code" || name == "pre" {
			if strings.HasPrefix(tag, "</") {
				if skip > 0 {
					skip--
				}
			} else {
				skip++
			}
		}
		b.WriteString(tag)
		h = h[lt+gt+1:]
	}
	return b.String()
}

func rewrite(text string, skip bool, isProject func(string) bool) string {
	if skip || text == "" {
		return text
	}
	text = keyRe.ReplaceAllStringFunc(text, func(m string) string {
		sub := keyRe.FindStringSubmatch(m)
		if isProject != nil && isProject(sub[1]) {
			return `<a class="issue-ref" href="/issues/` + m + `">` + m + `</a>`
		}
		return m
	})
	return mentionRe.ReplaceAllString(text, `$1<a class="mention" href="/users/$2">@$2</a>`)
}

func Excerpt(src string, n int) string {
	s := strings.Join(strings.Fields(strings.NewReplacer("#", "", "*", "", "`", "", ">", "", "_", " ").Replace(src)), " ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
