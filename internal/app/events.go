package app

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/kohanmathers/buggy/internal/config"
	"github.com/kohanmathers/buggy/internal/markdown"
	"github.com/kohanmathers/buggy/internal/store"
)

type Event struct {
	Type    string
	Issue   *store.Issue
	Actor   *store.User
	Comment *store.Comment
	Text    string
	Data    map[string]any
	Time    time.Time
}

func (a *App) emit(e Event) {
	e.Time = time.Now()
	if e.Issue != nil {
		cp := *e.Issue
		e.Issue = &cp
	}
	select {
	case a.events <- e:
	default:
		a.Log.Warn("event queue full; dropping event", "type", e.Type)
	}
}

func (a *App) eventLoop() {
	defer a.wg.Done()
	for {
		select {
		case e := <-a.events:
			a.handleEvent(e)
		case <-a.stop:
			for {
				select {
				case e := <-a.events:
					a.handleEvent(e)
				default:
					return
				}
			}
		}
	}
}

func (a *App) handleEvent(e Event) {
	defer func() {
		if r := recover(); r != nil {
			a.Log.Error("event handler panic", "type", e.Type, "panic", r)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	eventsTotal.inc(e.Type)
	a.notify(ctx, e)
	a.sendWebhooks(e)
	if e.Actor == nil || e.Actor.ID != a.System.ID {
		a.runEventAutomations(ctx, e)
	}
}

func (a *App) Summary(e Event) string {
	who := "Someone"
	if e.Actor != nil {
		who = "@" + e.Actor.Username
	}
	cfg := a.Cfg()
	wf := cfg.ProjectWorkflow(e.Issue.ProjectKey())
	label := func(id string) string {
		if s := wf.State(id); s != nil {
			return s.Label
		}
		return id
	}
	switch e.Type {
	case "issue.created":
		return who + " reported " + e.Issue.Key
	case "issue.commented":
		return who + " commented on " + e.Issue.Key
	case "issue.transitioned":
		s := fmt.Sprintf("%s moved %s from %s to %s", who, e.Issue.Key, label(fmt.Sprint(e.Data["from"])), label(fmt.Sprint(e.Data["to"])))
		if r, _ := e.Data["resolution"].(string); r != "" {
			if res := wf.Resolution(r); res != nil {
				r = res.Label
			}
			s += " (" + r + ")"
		}
		return s
	case "issue.duplicate":
		return fmt.Sprintf("%s marked %s as a duplicate of %s", who, e.Issue.Key, e.Data["original"])
	case "issue.updated":
		return who + " updated " + e.Issue.Key
	case "issue.attachment":
		return fmt.Sprintf("%s attached %s to %s", who, e.Data["filename"], e.Issue.Key)
	case "issue.voted":
		return who + " voted for " + e.Issue.Key
	case "issue.confirmed":
		if v, _ := e.Data["version"].(string); v != "" {
			return fmt.Sprintf("%s confirmed %s still happens in %s", who, e.Issue.Key, v)
		}
		return who + " is also affected by " + e.Issue.Key
	case "issue.linked":
		return fmt.Sprintf("%s linked %s %s %s", who, e.Issue.Key, e.Data["kind"], e.Data["other"])
	}
	return who + " changed " + e.Issue.Key
}

func (a *App) notify(ctx context.Context, e Event) {
	if e.Issue == nil {
		return
	}
	iss := e.Issue
	recipients := map[int64]string{}
	switch e.Type {
	case "issue.commented", "issue.transitioned", "issue.duplicate":
		ws, _ := a.DB.Watchers(ctx, iss.ID)
		for _, id := range ws {
			recipients[id] = e.Type
		}
		if e.Type == "issue.duplicate" {
			recipients[iss.ReporterID] = e.Type
		}
	case "issue.updated":
		if changes, ok := e.Data["changes"].([][3]string); ok {
			for _, c := range changes {
				if c[0] == "assignee" && c[2] != "" {
					if u, err := a.DB.UserByName(ctx, c[2]); err == nil {
						recipients[u.ID] = "assigned"
					}
				}
			}
		}
	}
	if a.Cfg().Notifications.Mentions && e.Text != "" {
		for _, name := range markdown.Mentions(e.Text) {
			if u, err := a.DB.UserByName(ctx, name); err == nil {
				recipients[u.ID] = "mention"
			}
		}
	}
	if e.Actor != nil {
		delete(recipients, e.Actor.ID)
	}
	delete(recipients, a.System.ID)
	if len(recipients) == 0 {
		return
	}
	ids := make([]int64, 0, len(recipients))
	for id := range recipients {
		ids = append(ids, id)
	}
	users, _ := a.DB.UsersByIDs(ctx, ids)
	title := a.Summary(e)
	body := ""
	if e.Comment != nil {
		body = markdown.Excerpt(e.Comment.Body, 280)
	} else if e.Type == "issue.created" {
		body = markdown.Excerpt(iss.Description, 280)
	}
	actorID := int64(0)
	if e.Actor != nil {
		actorID = e.Actor.ID
	}
	url := "/issues/" + iss.Key
	if e.Comment != nil {
		url += "#comment-" + strconv.FormatInt(e.Comment.ID, 10)
	}
	for id, kind := range recipients {
		u := users[id]
		if u == nil || u.Suspended() || u.System {
			continue
		}
		act := &Actor{User: u}
		if !a.CanSeeIssue(ctx, act, iss) {
			continue
		}
		if e.Comment != nil && e.Comment.Restricted && !a.Can(act, "comment.restricted", iss.ProjectKey()) {
			continue
		}
		t := title
		if kind == "mention" {
			who := "Someone"
			if e.Actor != nil {
				who = "@" + e.Actor.Username
			}
			t = who + " mentioned you on " + iss.Key
		} else if kind == "assigned" {
			t = iss.Key + " was assigned to you"
		}
		n := &store.Notification{UserID: id, IssueID: iss.ID, ActorID: actorID, Kind: kind, Title: t + ": " + iss.Title, Body: body, URL: url}
		if err := a.DB.CreateNotification(ctx, n); err != nil {
			a.Log.Error("notification", "err", err)
		}
		if u.Email != "" && u.EmailNotifications {
			a.sendMail(u.Email, "["+iss.Key+"] "+iss.Title, t+"\n\n"+body+"\n\n"+a.Cfg().Instance.URL+url+
				"\n\n--\nYou're receiving this because you watch this issue or were mentioned. Manage notifications: "+a.Cfg().Instance.URL+"/settings")
		}
	}
}

func (a *App) sendMail(to, subject, body string) {
	cfg := a.Cfg().Notifications.Email
	if !cfg.Enabled {
		return
	}
	go func() {
		if err := SendMail(cfg, to, subject, body); err != nil {
			a.Log.Error("send email", "err", err, "to", to)
		}
	}()
}

func SendMail(cfg config.Email, to, subject, body string) error {
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	msg := "From: " + cfg.From + "\r\nTo: " + to + "\r\nSubject: " + strings.ReplaceAll(subject, "\n", " ") +
		"\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nDate: " + time.Now().Format(time.RFC1123Z) +
		"\r\n\r\n" + strings.ReplaceAll(body, "\n", "\r\n")
	var auth smtp.Auth
	if cfg.Username != "" {
		auth = smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
	}
	from := cfg.From
	if i := strings.LastIndex(from, "<"); i >= 0 {
		from = strings.Trim(from[i:], "<>")
	}
	if cfg.TLS == "tls" {
		conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: cfg.Host})
		if err != nil {
			return err
		}
		c, err := smtp.NewClient(conn, cfg.Host)
		if err != nil {
			return err
		}
		defer c.Close()
		if auth != nil {
			if err := c.Auth(auth); err != nil {
				return err
			}
		}
		if err := c.Mail(from); err != nil {
			return err
		}
		if err := c.Rcpt(to); err != nil {
			return err
		}
		w, err := c.Data()
		if err != nil {
			return err
		}
		if _, err := w.Write([]byte(msg)); err != nil {
			return err
		}
		if err := w.Close(); err != nil {
			return err
		}
		return c.Quit()
	}
	return smtp.SendMail(addr, auth, from, []string{to}, []byte(msg))
}

func (a *App) sendWebhooks(e Event) {
	cfg := a.Cfg()
	if e.Issue == nil || len(cfg.Webhooks) == 0 {
		return
	}
	pk := e.Issue.ProjectKey()
	for _, w := range cfg.Webhooks {
		if !matchList(w.Events, e.Type) || (len(w.Projects) > 0 && !matchListFold(w.Projects, pk)) {
			continue
		}
		if (e.Issue.Private || e.Issue.Moderation != "approved" || (e.Comment != nil && e.Comment.Restricted)) && !w.IncludePrivate {
			continue
		}
		body, err := a.webhookPayload(w, e)
		if err != nil {
			continue
		}
		go a.deliver(w, e.Type, body)
	}
}

func matchList(list []string, v string) bool {
	if len(list) == 0 {
		return true
	}
	for _, x := range list {
		if x == "*" || x == v {
			return true
		}
	}
	return false
}

func matchListFold(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}

func hexColor(c string) int {
	n, _ := strconv.ParseInt(strings.TrimPrefix(c, "#"), 16, 64)
	return int(n)
}

func (a *App) webhookPayload(w config.Webhook, e Event) ([]byte, error) {
	cfg := a.Cfg()
	iss := e.Issue
	url := a.IssueURL(iss.Key)
	summary := a.Summary(e)
	detail := ""
	if e.Comment != nil {
		detail = markdown.Excerpt(e.Comment.Body, 500)
	} else if e.Type == "issue.created" {
		detail = markdown.Excerpt(iss.Description, 500)
	}
	switch w.Format {
	case "discord":
		color := hexColor(cfg.Instance.Theme.Primary)
		if s := cfg.ProjectWorkflow(iss.ProjectKey()).State(iss.State); s != nil && s.Color != "" {
			color = hexColor(s.Color)
		}
		return json.Marshal(map[string]any{
			"username": cfg.Instance.Name,
			"embeds": []map[string]any{{
				"title":       truncate("["+iss.Key+"] "+iss.Title, 250),
				"url":         url,
				"description": truncate(summary+"\n"+detail, 2000),
				"color":       color,
				"timestamp":   e.Time.UTC().Format(time.RFC3339),
			}},
		})
	case "slack":
		text := fmt.Sprintf("*<%s|%s>* %s\n%s", url, iss.Key, iss.Title, summary)
		if detail != "" {
			text += "\n> " + strings.ReplaceAll(detail, "\n", "\n> ")
		}
		return json.Marshal(map[string]any{"text": text})
	}
	payload := map[string]any{
		"event":     e.Type,
		"summary":   summary,
		"timestamp": e.Time.UTC().Format(time.RFC3339),
		"issue": map[string]any{
			"key": iss.Key, "title": iss.Title, "url": url, "state": iss.State, "resolution": iss.Resolution,
			"votes": iss.Votes, "labels": iss.Labels, "fields": iss.Fields, "private": iss.Private,
			"fingerprint": iss.Fingerprint, "project": iss.ProjectKey(),
		},
		"data": e.Data,
	}
	if e.Actor != nil {
		payload["actor"] = map[string]any{"username": e.Actor.Username, "name": e.Actor.Name()}
	}
	if e.Comment != nil {
		payload["comment"] = map[string]any{"id": e.Comment.ID, "body": e.Comment.Body, "restricted": e.Comment.Restricted}
	}
	return json.Marshal(payload)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func (a *App) deliver(w config.Webhook, event string, body []byte) {
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*attempt) * 5 * time.Second)
		}
		req, _ := http.NewRequest("POST", w.URL, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "Buggy-Webhook/1")
		req.Header.Set("X-Buggy-Event", event)
		if w.Secret != "" {
			m := hmac.New(sha256.New, []byte(w.Secret))
			m.Write(body)
			req.Header.Set("X-Buggy-Signature", "sha256="+hex.EncodeToString(m.Sum(nil)))
		}
		resp, err := a.HTTP.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 300 {
				webhooksTotal.inc("ok")
				return
			}
			if resp.StatusCode < 500 && resp.StatusCode != 429 {
				a.Log.Warn("webhook rejected", "name", w.Name, "status", resp.StatusCode)
				webhooksTotal.inc("failed")
				return
			}
			err = fmt.Errorf("status %d", resp.StatusCode)
		}
		a.Log.Warn("webhook delivery failed", "name", w.Name, "attempt", attempt+1, "err", err)
	}
	webhooksTotal.inc("failed")
}
