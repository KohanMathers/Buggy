package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/kohanmathers/buggy/internal/parsers"
	"github.com/kohanmathers/buggy/internal/store"
)

func (a *App) Comments(ctx context.Context, act *Actor, iss *store.Issue) ([]*store.Comment, error) {
	all, err := a.DB.Comments(ctx, iss.ID)
	if err != nil {
		return nil, err
	}
	var out []*store.Comment
	for _, c := range all {
		if a.commentVisible(act, iss.ProjectKey(), c) {
			out = append(out, c)
		}
	}
	return out, nil
}

func (a *App) AddComment(ctx context.Context, act *Actor, key, body string, restricted bool, captcha string) (*store.Comment, error) {
	if err := RequireLogin(act); err != nil && !act.System {
		return nil, err
	}
	iss, err := a.GetIssue(ctx, act, key)
	if err != nil {
		return nil, err
	}
	pk := iss.ProjectKey()
	if !a.Can(act, "comment.create", pk) {
		return nil, ErrForbidden
	}
	if restricted && !a.Can(act, "comment.restricted", pk) {
		return nil, ErrForbidden
	}
	if err := a.spamCheck(ctx, act, "comment", captcha); err != nil {
		return nil, err
	}
	return a.addComment(ctx, act, iss, body, restricted, false)
}

func (a *App) addComment(ctx context.Context, act *Actor, iss *store.Issue, body string, restricted, quiet bool) (*store.Comment, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, Invalid(map[string]string{"body": "Comment can't be empty"})
	}
	if len(body) > 100_000 {
		return nil, Invalid(map[string]string{"body": "Comment is too long"})
	}
	pk := iss.ProjectKey()
	c := &store.Comment{IssueID: iss.ID, AuthorID: act.ID(), Body: body, Restricted: restricted}
	if a.needsModeration(act, pk, body) {
		c.Moderation = "pending"
	}
	t := time.Now().Unix()
	err := a.DB.InTx(ctx, func(tx *store.Tx) error {
		if err := a.DB.CreateComment(ctx, tx, c); err != nil {
			return err
		}
		if !act.System {
			if _, err := a.DB.SetWatch(ctx, tx, iss.ID, act.ID(), true); err != nil {
				return err
			}
		}
		iss.CommentCount = a.DB.CountComments(ctx, tx, iss.ID)
		if c.Moderation == "approved" {
			iss.UpdatedAt, iss.ActivityAt = t, t
		}
		var w int64
		tx.QueryRow(ctx, `SELECT watchers FROM issues WHERE id = ?`, iss.ID).Scan(&w)
		iss.Watchers = w
		if err := a.DB.SaveIssue(ctx, tx, iss); err != nil {
			return err
		}
		return a.DB.UpdateFTS(ctx, tx, iss.ID)
	})
	if err != nil {
		return nil, err
	}
	c.Author = act.User
	if c.Moderation == "approved" {
		a.emit(Event{Type: "issue.commented", Issue: iss, Actor: act.User, Comment: c, Text: body})
	}
	if !quiet && act.ID() == iss.ReporterID && !restricted {
		wf := a.Cfg().ProjectWorkflow(pk)
		if s := wf.State(iss.State); s != nil && s.OnReporterReply != "" {
			a.applyState(ctx, a.SystemActor(), iss, s.OnReporterReply, "")
		}
	}
	return c, nil
}

func (a *App) commentForEdit(ctx context.Context, act *Actor, id int64, perm string) (*store.Comment, *store.Issue, error) {
	if err := RequireLogin(act); err != nil {
		return nil, nil, err
	}
	c, err := a.DB.CommentByID(ctx, id)
	if err != nil {
		return nil, nil, ErrNotFound
	}
	iss, err := a.DB.IssueByID(ctx, c.IssueID)
	if err != nil {
		return nil, nil, ErrNotFound
	}
	if !a.CanSeeIssue(ctx, act, iss) {
		return nil, nil, ErrNotFound
	}
	pk := iss.ProjectKey()
	own := c.AuthorID == act.ID()
	if !(a.Can(act, perm, pk) || (own && a.Can(act, "comment.edit_own", pk))) {
		return nil, nil, ErrForbidden
	}
	return c, iss, nil
}

func (a *App) EditComment(ctx context.Context, act *Actor, id int64, body string) (*store.Comment, error) {
	c, iss, err := a.commentForEdit(ctx, act, id, "comment.edit")
	if err != nil {
		return nil, err
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, Invalid(map[string]string{"body": "Comment can't be empty"})
	}
	c.Body = body
	c.UpdatedAt = time.Now().Unix()
	if err := a.DB.SaveComment(ctx, a.DB, c); err != nil {
		return nil, err
	}
	a.DB.UpdateFTS(ctx, a.DB, iss.ID)
	return c, nil
}

func (a *App) DeleteComment(ctx context.Context, act *Actor, id int64) error {
	c, iss, err := a.commentForEdit(ctx, act, id, "comment.delete")
	if err != nil {
		return err
	}
	c.Deleted = true
	c.UpdatedAt = time.Now().Unix()
	if err := a.DB.SaveComment(ctx, a.DB, c); err != nil {
		return err
	}
	iss.CommentCount = a.DB.CountComments(ctx, a.DB, iss.ID)
	a.DB.Exec(ctx, `UPDATE issues SET comment_count = ? WHERE id = ?`, iss.CommentCount, iss.ID)
	a.DB.AddHistory(ctx, a.DB, iss.ID, act.ID(), "comment_deleted", fmt.Sprint(c.ID), "")
	return a.DB.UpdateFTS(ctx, a.DB, iss.ID)
}

var unsafeName = regexp.MustCompile(`[^\w.\-]+`)

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (a *App) storeAttachment(ctx context.Context, iss *store.Issue, act *Actor, f bufferedUpload) (*store.Attachment, error) {
	name := unsafeName.ReplaceAllString(filepath.Base(f.Name), "_")
	if name == "" || name == "." {
		name = "file"
	}
	ct := f.ContentType
	if ct == "" || ct == "application/octet-stream" {
		if t := mime.TypeByExtension(filepath.Ext(name)); t != "" {
			ct = t
		} else if isText(f.data) {
			ct = "text/plain; charset=utf-8"
		} else {
			ct = "application/octet-stream"
		}
	}
	key := fmt.Sprintf("issues/%d/%s-%s", iss.ID, randHex(8), name)
	if err := a.Files.Put(ctx, key, bytes.NewReader(f.data), int64(len(f.data)), ct); err != nil {
		return nil, err
	}
	at := &store.Attachment{IssueID: iss.ID, UploaderID: act.ID(), Filename: name, ContentType: ct, Size: int64(len(f.data)), StorageKey: key}
	if err := a.DB.CreateAttachment(ctx, a.DB, at); err != nil {
		return nil, err
	}
	a.DB.AddHistory(ctx, a.DB, iss.ID, act.ID(), "attachment", "", name)
	return at, nil
}

func (a *App) AddAttachment(ctx context.Context, act *Actor, key string, up Upload) (*store.Attachment, error) {
	if err := RequireLogin(act); err != nil {
		return nil, err
	}
	iss, err := a.GetIssue(ctx, act, key)
	if err != nil {
		return nil, err
	}
	pk := iss.ProjectKey()
	if !a.Can(act, "attachment.upload", pk) {
		return nil, ErrForbidden
	}
	if err := a.checkMuted(act); err != nil {
		return nil, err
	}
	if err := a.limit(act, "upload"); err != nil {
		return nil, err
	}
	files, err := a.bufferUploads([]Upload{up})
	if err != nil {
		return nil, err
	}
	at, err := a.storeAttachment(ctx, iss, act, files[0])
	if err != nil {
		return nil, err
	}
	res := a.ParseInputs(pk, "", nil, files)
	a.applyParse(ctx, iss, res)
	a.emit(Event{Type: "issue.attachment", Issue: iss, Actor: act.User, Data: map[string]any{"filename": at.Filename}})
	return at, nil
}

func (a *App) applyParse(ctx context.Context, iss *store.Issue, res parsers.Result) {
	fields := map[string][]string{}
	for id, v := range res.Fields {
		if len(iss.Fields[id]) == 0 {
			fields[id] = v
		}
	}
	var add []string
	for _, l := range res.Labels {
		if !iss.HasLabel(l) {
			add = append(add, l)
		}
	}
	setFP := res.Fingerprint != "" && iss.Fingerprint == ""
	if len(fields) == 0 && len(add) == 0 && !setFP {
		return
	}
	cfg := a.Cfg()
	a.DB.InTx(ctx, func(tx *store.Tx) error {
		if len(fields) > 0 {
			ids, vals := fieldValues(cfg, fields)
			if err := a.DB.SetFieldValues(ctx, tx, iss.ID, ids, vals); err != nil {
				return err
			}
		}
		if len(add) > 0 {
			if err := a.DB.SetLabels(ctx, tx, iss.ID, append(iss.Labels, add...)); err != nil {
				return err
			}
		}
		if setFP {
			iss.Fingerprint = res.Fingerprint
			if err := a.DB.SaveIssue(ctx, tx, iss); err != nil {
				return err
			}
			a.DB.AddHistory(ctx, tx, iss.ID, a.System.ID, "fingerprint", "", res.Fingerprint)
		}
		return a.DB.UpdateFTS(ctx, tx, iss.ID)
	})
	a.DB.LoadRelations(ctx, []*store.Issue{iss})
}

func (a *App) OpenAttachment(ctx context.Context, act *Actor, id int64) (*store.Attachment, io.ReadCloser, error) {
	at, err := a.DB.AttachmentByID(ctx, id)
	if err != nil || at.Deleted {
		return nil, nil, ErrNotFound
	}
	iss, err := a.DB.IssueByID(ctx, at.IssueID)
	if err != nil || !a.CanSeeIssue(ctx, act, iss) {
		return nil, nil, ErrNotFound
	}
	rc, err := a.Files.Open(ctx, at.StorageKey)
	if err != nil {
		return nil, nil, ErrNotFound
	}
	return at, rc, nil
}

func (a *App) DeleteAttachment(ctx context.Context, act *Actor, id int64) error {
	if err := RequireLogin(act); err != nil {
		return err
	}
	at, err := a.DB.AttachmentByID(ctx, id)
	if err != nil {
		return ErrNotFound
	}
	iss, err := a.DB.IssueByID(ctx, at.IssueID)
	if err != nil || !a.CanSeeIssue(ctx, act, iss) {
		return ErrNotFound
	}
	if at.UploaderID != act.ID() && !a.Can(act, "attachment.delete", iss.ProjectKey()) {
		return ErrForbidden
	}
	if err := a.DB.DeleteAttachment(ctx, id); err != nil {
		return err
	}
	a.Files.Delete(ctx, at.StorageKey)
	return a.DB.AddHistory(ctx, a.DB, iss.ID, act.ID(), "attachment_deleted", at.Filename, "")
}

func (a *App) Vote(ctx context.Context, act *Actor, key string, on bool) (*store.Issue, error) {
	if err := RequireLogin(act); err != nil {
		return nil, err
	}
	iss, err := a.GetIssue(ctx, act, key)
	if err != nil {
		return nil, err
	}
	if !a.Can(act, "issue.vote", iss.ProjectKey()) {
		return nil, ErrForbidden
	}
	if err := a.limit(act, "vote"); err != nil {
		return nil, err
	}
	changed, err := a.DB.SetVote(ctx, a.DB, iss.ID, act.ID(), on)
	if err != nil {
		return nil, err
	}
	if changed {
		if on {
			iss.Votes++
			a.DB.SetWatch(ctx, a.DB, iss.ID, act.ID(), true)
			a.emit(Event{Type: "issue.voted", Issue: iss, Actor: act.User})
		} else {
			iss.Votes--
		}
	}
	return iss, nil
}

func (a *App) Watch(ctx context.Context, act *Actor, key string, on bool) (*store.Issue, error) {
	if err := RequireLogin(act); err != nil {
		return nil, err
	}
	iss, err := a.GetIssue(ctx, act, key)
	if err != nil {
		return nil, err
	}
	if !a.Can(act, "issue.watch", iss.ProjectKey()) {
		return nil, ErrForbidden
	}
	if ch, err := a.DB.SetWatch(ctx, a.DB, iss.ID, act.ID(), on); err != nil {
		return nil, err
	} else if ch {
		if on {
			iss.Watchers++
		} else {
			iss.Watchers--
		}
	}
	return iss, nil
}

func (a *App) Confirm(ctx context.Context, act *Actor, key, version string, env map[string]string, note string) (*store.Issue, error) {
	if err := RequireLogin(act); err != nil {
		return nil, err
	}
	iss, err := a.GetIssue(ctx, act, key)
	if err != nil {
		return nil, err
	}
	pk := iss.ProjectKey()
	if !a.Can(act, "issue.confirm", pk) {
		return nil, ErrForbidden
	}
	if err := a.checkMuted(act); err != nil {
		return nil, err
	}
	if err := a.limit(act, "vote"); err != nil {
		return nil, err
	}
	if iss.ResolvedAt != 0 && iss.Resolution != "fixed" {
		return nil, Errf(409, "this issue is resolved; open a new report if you still have the problem")
	}
	clean := map[string]string{}
	in := map[string][]string{}
	for k, v := range env {
		if f := a.Cfg().Field(k); f != nil && f.Environment {
			in[k] = []string{v}
		}
	}
	vals, errs := a.cleanFields(ctx, act, pk, in)
	if len(errs) > 0 {
		return nil, Invalid(errs)
	}
	for k, v := range vals {
		if len(v) > 0 {
			clean[k] = v[0]
		}
	}
	var verID int64
	if version != "" {
		v, err := a.DB.VersionByName(ctx, iss.ProjectID, version)
		if err != nil {
			return nil, Invalid(map[string]string{"version": "unknown version"})
		}
		verID = v.ID
	}
	note = strings.TrimSpace(note)
	if len(note) > 2000 {
		note = note[:2000]
	}
	reopened := false
	err = a.DB.InTx(ctx, func(tx *store.Tx) error {
		if err := a.DB.AddConfirmation(ctx, tx, &store.Confirmation{IssueID: iss.ID, UserID: act.ID(), VersionID: verID, Env: encodeEnv(clean), Note: note}); err != nil {
			return err
		}
		if _, err := a.DB.SetVote(ctx, tx, iss.ID, act.ID(), true); err != nil {
			return err
		}
		if _, err := a.DB.SetWatch(ctx, tx, iss.ID, act.ID(), true); err != nil {
			return err
		}
		if verID != 0 {
			if err := a.DB.AddIssueVersion(ctx, tx, iss.ID, verID, "affects"); err != nil {
				return err
			}
			a.DB.AddHistory(ctx, tx, iss.ID, act.ID(), "confirmed", "", version)
		}
		t := time.Now().Unix()
		_, err := tx.Exec(ctx, `UPDATE issues SET activity_at = ?, updated_at = ? WHERE id = ?`, t, t, iss.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	if iss.Resolution == "fixed" && verID != 0 && len(iss.FixVersions) > 0 {
		v, _ := a.DB.VersionByID(ctx, verID)
		newest := iss.FixVersions[len(iss.FixVersions)-1]
		if v != nil && v.SortOrder > newest.SortOrder {
			wf := a.Cfg().ProjectWorkflow(pk)
			a.addComment(ctx, a.SystemActor(), iss, fmt.Sprintf("@%s reports this still happens in **%s** (fixed in %s). Reopening for triage.", act.User.Username, version, newest.Name), false, true)
			a.applyState(ctx, a.SystemActor(), iss, wf.Initial, "")
			reopened = true
		}
	}
	iss, _ = a.GetIssue(ctx, act, key)
	a.emit(Event{Type: "issue.confirmed", Issue: iss, Actor: act.User, Data: map[string]any{"version": version, "reopened": reopened}})
	return iss, nil
}

func encodeEnv(m map[string]string) string {
	b, _ := json.Marshal(m)
	return string(b)
}

var LinkKinds = map[string][2]string{
	"relates":    {"relates to", "relates to"},
	"blocks":     {"blocks", "is blocked by"},
	"causes":     {"causes", "is caused by"},
	"duplicates": {"duplicates", "is duplicated by"},
	"clones":     {"clones", "is cloned by"},
}

func (a *App) Link(ctx context.Context, act *Actor, key, otherKey, kind string) error {
	if err := RequireLogin(act); err != nil {
		return err
	}
	if _, ok := LinkKinds[kind]; !ok || kind == "duplicates" {
		return Errf(422, "unknown link type %q", kind)
	}
	iss, err := a.GetIssue(ctx, act, key)
	if err != nil {
		return err
	}
	other, err := a.GetIssue(ctx, act, otherKey)
	if err != nil {
		return Errf(404, "issue %s not found", otherKey)
	}
	if iss.ID == other.ID {
		return Errf(422, "can't link an issue to itself")
	}
	if !a.Can(act, "issue.link", iss.ProjectKey()) {
		return ErrForbidden
	}
	if err := a.DB.AddLink(ctx, a.DB, iss.ID, other.ID, kind, act.ID()); err != nil {
		return err
	}
	a.DB.AddHistory(ctx, a.DB, iss.ID, act.ID(), "link", "", kind+" "+other.Key)
	a.emit(Event{Type: "issue.linked", Issue: iss, Actor: act.User, Data: map[string]any{"kind": kind, "other": other.Key}})
	return nil
}

func (a *App) Unlink(ctx context.Context, act *Actor, key string, linkID int64) error {
	if err := RequireLogin(act); err != nil {
		return err
	}
	iss, err := a.GetIssue(ctx, act, key)
	if err != nil {
		return err
	}
	if !a.Can(act, "issue.link", iss.ProjectKey()) {
		return ErrForbidden
	}
	l, err := a.DB.RemoveLink(ctx, linkID)
	if err != nil {
		return ErrNotFound
	}
	a.DB.AddHistory(ctx, a.DB, iss.ID, act.ID(), "link", l.Kind, "")
	return nil
}

func (a *App) Links(ctx context.Context, act *Actor, iss *store.Issue) []*store.Link {
	all, _ := a.DB.Links(ctx, iss.ID)
	var out []*store.Link
	for _, l := range all {
		if a.CanSeeIssue(ctx, act, l.Other) {
			out = append(out, l)
		}
	}
	return out
}
