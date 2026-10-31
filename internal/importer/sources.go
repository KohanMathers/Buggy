package importer

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kohanmathers/buggy/internal/app"
)

func Jira(ctx context.Context, a *app.App, opts Options) (int, error) {
	if opts.URL == "" || opts.Query == "" {
		return 0, fmt.Errorf("--url and --jql are required")
	}
	r, err := newRunner(ctx, a, opts, "jira")
	if err != nil {
		return 0, err
	}
	hdr := map[string]string{}
	if opts.User != "" {
		hdr["Authorization"] = "Basic " + base64.StdEncoding.EncodeToString([]byte(opts.User+":"+opts.Token))
	} else if opts.Token != "" {
		hdr["Authorization"] = "Bearer " + opts.Token
	}
	cl := &http.Client{Timeout: 60 * time.Second}
	base := strings.TrimRight(opts.URL, "/")
	n := 0
	for start := 0; ; {
		var page struct {
			Total  int `json:"total"`
			Issues []struct {
				Key    string `json:"key"`
				Fields struct {
					Summary     string `json:"summary"`
					Description string `json:"description"`
					Environment string `json:"environment"`
					Status      struct {
						Name     string `json:"name"`
						Category struct {
							Key string `json:"key"`
						} `json:"statusCategory"`
					} `json:"status"`
					Resolution *struct {
						Name string `json:"name"`
					} `json:"resolution"`
					Reporter *struct {
						Name        string `json:"name"`
						AccountID   string `json:"accountId"`
						DisplayName string `json:"displayName"`
					} `json:"reporter"`
					Created        string   `json:"created"`
					Updated        string   `json:"updated"`
					ResolutionDate string   `json:"resolutiondate"`
					Labels         []string `json:"labels"`
					Versions       []struct {
						Name string `json:"name"`
					} `json:"versions"`
					FixVersions []struct {
						Name string `json:"name"`
					} `json:"fixVersions"`
					Votes struct {
						Votes int64 `json:"votes"`
					} `json:"votes"`
					Comment struct {
						Comments []struct {
							Author struct {
								Name        string `json:"name"`
								DisplayName string `json:"displayName"`
							} `json:"author"`
							Body    string `json:"body"`
							Created string `json:"created"`
						} `json:"comments"`
					} `json:"comment"`
					IssueLinks []struct {
						Type struct {
							Name string `json:"name"`
						} `json:"type"`
						OutwardIssue *struct {
							Key string `json:"key"`
						} `json:"outwardIssue"`
					} `json:"issuelinks"`
				} `json:"fields"`
			} `json:"issues"`
		}
		u := fmt.Sprintf("%s/rest/api/2/search?jql=%s&startAt=%d&maxResults=100&fields=%s", base, url.QueryEscape(opts.Query), start,
			"summary,description,environment,status,resolution,reporter,created,updated,resolutiondate,labels,versions,fixVersions,votes,comment,issuelinks")
		if err := getJSON(ctx, cl, u, hdr, &page); err != nil {
			return n, err
		}
		if len(page.Issues) == 0 {
			break
		}
		for _, ji := range page.Issues {
			f := ji.Fields
			_, num, _ := strings.Cut(ji.Key, "-")
			in := importIssue{SourceKey: ji.Key, Title: f.Summary, Status: f.Status.Name, StatusCat: f.Status.Category.Key,
				Created: parseTime(f.Created), Updated: parseTime(f.Updated), Resolved: parseTime(f.ResolutionDate),
				Labels: f.Labels, Votes: f.Votes.Votes}
			in.Number, _ = strconv.ParseInt(num, 10, 64)
			in.Description = jiraToMarkdown(f.Description)
			if f.Environment != "" {
				in.Description += "\n\n**Environment**\n\n" + jiraToMarkdown(f.Environment)
			}
			if f.Resolution != nil {
				in.Resolution = f.Resolution.Name
			}
			if f.Reporter != nil {
				in.Reporter = f.Reporter.Name
				if in.Reporter == "" {
					in.Reporter = f.Reporter.DisplayName
				}
				in.ReporterName = f.Reporter.DisplayName
			}
			for _, v := range f.Versions {
				in.Affects = append(in.Affects, v.Name)
			}
			for _, v := range f.FixVersions {
				in.Fixes = append(in.Fixes, v.Name)
			}
			for _, c := range f.Comment.Comments {
				author := c.Author.Name
				if author == "" {
					author = c.Author.DisplayName
				}
				in.Comments = append(in.Comments, importComment{Author: author, Body: jiraToMarkdown(c.Body), Created: parseTime(c.Created)})
			}
			for _, l := range f.IssueLinks {
				if l.OutwardIssue == nil {
					continue
				}
				kind := map[string]string{"duplicate": "duplicates", "blocks": "blocks", "cloners": "clones", "relates": "relates"}[strings.ToLower(l.Type.Name)]
				if kind == "" {
					kind = "relates"
				}
				in.Links = append(in.Links, importLink{Kind: kind, To: l.OutwardIssue.Key})
			}
			created, err := r.save(ctx, in)
			if err != nil {
				return n, fmt.Errorf("%s: %w", ji.Key, err)
			}
			if created {
				n++
				r.opts.Log("imported %s → %s", ji.Key, r.keyMap[ji.Key])
			}
			if opts.Limit > 0 && n >= opts.Limit {
				r.finish(ctx)
				return n, nil
			}
		}
		start += len(page.Issues)
		if start >= page.Total {
			break
		}
	}
	r.finish(ctx)
	return n, nil
}

var (
	jiraCode    = regexp.MustCompile(`(?s)\{(?:code|noformat)(?::([a-zA-Z]+))?[^}]*\}(.*?)\{(?:code|noformat)\}`)
	jiraHeading = regexp.MustCompile(`(?m)^h([1-6])\.\s+`)
	jiraLink    = regexp.MustCompile(`\[([^|\]]+)\|([^\]]+)\]`)
	jiraBold    = regexp.MustCompile(`(^|\s)\*([^*\n]+)\*(\s|$)`)
	jiraMono    = regexp.MustCompile(`\{\{([^}]+)\}\}`)
	jiraList    = regexp.MustCompile(`(?m)^#\s+`)
	jiraQuote   = regexp.MustCompile(`(?s)\{quote\}(.*?)\{quote\}`)
)

func jiraToMarkdown(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = jiraCode.ReplaceAllString(s, "```$1\n$2\n```")
	s = jiraQuote.ReplaceAllStringFunc(s, func(m string) string {
		inner := jiraQuote.FindStringSubmatch(m)[1]
		return "> " + strings.ReplaceAll(strings.TrimSpace(inner), "\n", "\n> ")
	})
	s = jiraHeading.ReplaceAllStringFunc(s, func(m string) string {
		n, _ := strconv.Atoi(m[1:2])
		return strings.Repeat("#", n) + " "
	})
	s = jiraLink.ReplaceAllString(s, "[$1]($2)")
	s = jiraBold.ReplaceAllString(s, "$1**$2**$3")
	s = jiraMono.ReplaceAllString(s, "`$1`")
	s = jiraList.ReplaceAllString(s, "1. ")
	return s
}

func GitHub(ctx context.Context, a *app.App, opts Options) (int, error) {
	if opts.Repo == "" {
		return 0, fmt.Errorf("--repo owner/name is required")
	}
	r, err := newRunner(ctx, a, opts, "github")
	if err != nil {
		return 0, err
	}
	hdr := map[string]string{"Accept": "application/vnd.github+json"}
	if opts.Token != "" {
		hdr["Authorization"] = "Bearer " + opts.Token
	}
	cl := &http.Client{Timeout: 60 * time.Second}
	n := 0
	for page := 1; ; page++ {
		var list []struct {
			Number      int64  `json:"number"`
			Title       string `json:"title"`
			Body        string `json:"body"`
			State       string `json:"state"`
			StateReason string `json:"state_reason"`
			User        struct {
				Login string `json:"login"`
			} `json:"user"`
			Labels []struct {
				Name string `json:"name"`
			} `json:"labels"`
			Milestone *struct {
				Title string `json:"title"`
			} `json:"milestone"`
			Comments    int            `json:"comments"`
			CommentsURL string         `json:"comments_url"`
			CreatedAt   string         `json:"created_at"`
			UpdatedAt   string         `json:"updated_at"`
			ClosedAt    string         `json:"closed_at"`
			Reactions   map[string]any `json:"reactions"`
			PullRequest any            `json:"pull_request"`
		}
		u := fmt.Sprintf("https://api.github.com/repos/%s/issues?state=all&per_page=100&page=%d&direction=asc&sort=created", opts.Repo, page)
		if err := getJSON(ctx, cl, u, hdr, &list); err != nil {
			return n, err
		}
		if len(list) == 0 {
			break
		}
		for _, gi := range list {
			if gi.PullRequest != nil {
				continue
			}
			in := importIssue{SourceKey: "#" + strconv.FormatInt(gi.Number, 10), Number: gi.Number, Title: gi.Title,
				Description: gi.Body, Reporter: gi.User.Login, ReporterName: gi.User.Login,
				Created: parseTime(gi.CreatedAt), Updated: parseTime(gi.UpdatedAt), Resolved: parseTime(gi.ClosedAt)}
			if gi.State == "closed" {
				in.StatusCat = "done"
				in.Status = "closed"
				in.Resolution = gi.StateReason
				if in.Resolution == "" {
					in.Resolution = "completed"
				}
			} else {
				in.Status, in.StatusCat = "open", "new"
			}
			for _, l := range gi.Labels {
				in.Labels = append(in.Labels, l.Name)
			}
			if gi.Milestone != nil && gi.State == "closed" && in.Resolution == "completed" {
				in.Fixes = []string{gi.Milestone.Title}
			}
			if v, ok := gi.Reactions["+1"].(float64); ok {
				in.Votes = int64(v)
			}
			if gi.Comments > 0 {
				var cs []struct {
					User struct {
						Login string `json:"login"`
					} `json:"user"`
					Body      string `json:"body"`
					CreatedAt string `json:"created_at"`
				}
				for cp := 1; ; cp++ {
					var batch = cs[:0:0]
					if err := getJSON(ctx, cl, fmt.Sprintf("%s?per_page=100&page=%d", gi.CommentsURL, cp), hdr, &batch); err != nil {
						return n, err
					}
					cs = append(cs, batch...)
					if len(batch) < 100 {
						break
					}
				}
				for _, c := range cs {
					in.Comments = append(in.Comments, importComment{Author: c.User.Login, Body: c.Body, Created: parseTime(c.CreatedAt)})
				}
			}
			created, err := r.save(ctx, in)
			if err != nil {
				return n, fmt.Errorf("#%d: %w", gi.Number, err)
			}
			if created {
				n++
				r.opts.Log("imported #%d → %s", gi.Number, r.keyMap[in.SourceKey])
			}
			if opts.Limit > 0 && n >= opts.Limit {
				return n, nil
			}
		}
	}
	return n, nil
}
