package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kohanmathers/buggy/internal/config"
	"github.com/kohanmathers/buggy/internal/parsers"
	"github.com/kohanmathers/buggy/internal/store"
)

type Upload struct {
	Name        string
	ContentType string
	Size        int64
	R           io.Reader
}

type IssueInput struct {
	Project      string
	Title        string
	Description  string
	Fields       map[string][]string
	Labels       []string
	Affects      []string
	Private      bool
	CaptchaToken string
	Attachments  []Upload
}

func (a *App) GetIssue(ctx context.Context, act *Actor, key string) (*store.Issue, error) {
	iss, err := a.DB.IssueByKey(ctx, key)
	if err != nil {
		return nil, ErrNotFound
	}
	if err := a.DB.LoadRelations(ctx, []*store.Issue{iss}); err != nil {
		return nil, err
	}
	if !a.CanSeeIssue(ctx, act, iss) {
		return nil, ErrNotFound
	}
	return iss, nil
}

func (a *App) cleanFields(ctx context.Context, act *Actor, pk string, in map[string][]string) (map[string][]string, map[string]string) {
	out := map[string][]string{}
	errs := map[string]string{}
	allowed := map[string]*config.Field{}
	for _, f := range a.Cfg().ProjectFields(pk) {
		allowed[f.ID] = f
	}
	for id, raw := range in {
		f := allowed[id]
		if f == nil {
			continue
		}
		if f.EditPermission != "" && !a.Can(act, f.EditPermission, pk) {
			continue
		}
		var vals []string
		for _, v := range raw {
			if f.Type != "textarea" {
				v = strings.TrimSpace(v)
			}
			if strings.TrimSpace(v) != "" {
				vals = append(vals, v)
			}
		}
		if len(vals) == 0 {
			out[id] = nil
			continue
		}
		if !f.Multi() {
			vals = vals[:1]
		}
		bad := func(msg string) { errs[id] = f.Label + ": " + msg }
		switch f.Type {
		case "select", "multiselect":
			for _, v := range vals {
				if f.Option(v) == nil {
					bad(fmt.Sprintf("%q is not a valid option", v))
				}
			}
		case "number":
			n, err := strconv.ParseFloat(vals[0], 64)
			if err != nil {
				bad("must be a number")
			} else if (f.Min != nil && n < *f.Min) || (f.Max != nil && n > *f.Max) {
				bad("is out of range")
			}
		case "bool":
			switch strings.ToLower(vals[0]) {
			case "true", "on", "yes", "1":
				vals[0] = "true"
			default:
				vals[0] = "false"
			}
		case "date":
			if _, err := time.Parse("2006-01-02", vals[0]); err != nil {
				bad("must be a date (YYYY-MM-DD)")
			}
		case "url":
			u, err := url.Parse(vals[0])
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				bad("must be an http(s) URL")
			}
		case "user":
			if u, err := a.DB.UserByName(ctx, strings.TrimPrefix(vals[0], "@")); err != nil {
				bad("unknown user")
			} else {
				vals[0] = u.Username
			}
		case "version":
			row, err := a.DB.ProjectByKey(ctx, pk)
			if err == nil {
				if _, err := a.DB.VersionByName(ctx, row.ID, vals[0]); err != nil {
					bad("unknown version")
				}
			}
		case "text":
			if utf8.RuneCountInString(vals[0]) > 1000 {
				bad("is too long")
			}
		case "textarea":
			if len(vals[0]) > 200_000 {
				bad("is too long")
			}
		}
		if !f.Matches(vals[0]) && (f.Type == "text" || f.Type == "textarea") {
			bad("has an invalid format")
		}
		out[id] = vals
	}
	return out, errs
}

func fieldValues(cfg *config.Config, fields map[string][]string) ([]string, []store.FieldValue) {
	var ids []string
	var vals []store.FieldValue
	for id, vs := range fields {
		ids = append(ids, id)
		f := cfg.Field(id)
		for _, v := range vs {
			fv := store.FieldValue{Field: id, Value: v}
			if f != nil {
				switch f.Type {
				case "number":
					if n, err := strconv.ParseFloat(v, 64); err == nil {
						fv.Num = &n
					}
				case "date":
					if d, err := time.Parse("2006-01-02", v); err == nil {
						n := float64(d.Unix())
						fv.Num = &n
					}
				}
			}
			vals = append(vals, fv)
		}
	}
	return ids, vals
}

func (a *App) resolveVersions(ctx context.Context, projectID int64, names []string) ([]int64, error) {
	var ids []int64
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		v, err := a.DB.VersionByName(ctx, projectID, n)
		if err != nil {
			return nil, Errf(422, "unknown version %q", n)
		}
		ids = append(ids, v.ID)
	}
	return ids, nil
}

func (a *App) filterLabels(act *Actor, pk string, labels []string) []string {
	cfg := a.Cfg()
	var out []string
	seen := map[string]bool{}
	for _, l := range labels {
		l = strings.TrimSpace(l)
		if l == "" || seen[l] {
			continue
		}
		def := cfg.Label(l)
		if def == nil && !a.Can(act, "issue.label", pk) {
			continue
		}
		if def != nil && def.Restricted && !a.Can(act, "issue.label", pk) {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	return out
}

func (a *App) labelsMakePrivate(labels []string) bool {
	cfg := a.Cfg()
	for _, l := range labels {
		if d := cfg.Label(l); d != nil && d.Private {
			return true
		}
	}
	return false
}

type bufferedUpload struct {
	Upload
	data []byte
}

func (a *App) bufferUploads(ups []Upload) ([]bufferedUpload, error) {
	cfg := a.Cfg()
	var out []bufferedUpload
	for _, u := range ups {
		if err := a.checkUpload(u); err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(u.R, int64(cfg.Storage.MaxSize)+1))
		if err != nil {
			return nil, err
		}
		if int64(len(data)) > int64(cfg.Storage.MaxSize) {
			return nil, Errf(413, "%s is larger than %d MB", u.Name, cfg.Storage.MaxSize>>20)
		}
		u.Size = int64(len(data))
		out = append(out, bufferedUpload{Upload: u, data: data})
	}
	return out, nil
}

func (a *App) checkUpload(u Upload) error {
	cfg := a.Cfg()
	if u.Size > int64(cfg.Storage.MaxSize) {
		return Errf(413, "%s is larger than %d MB", u.Name, cfg.Storage.MaxSize>>20)
	}
	if len(cfg.Storage.AllowedTypes) > 0 {
		ok := false
		ln := strings.ToLower(u.Name)
		for _, ext := range cfg.Storage.AllowedTypes {
			if strings.HasSuffix(ln, strings.ToLower(ext)) {
				ok = true
			}
		}
		if !ok {
			return Errf(415, "%s: file type not allowed (allowed: %s)", u.Name, strings.Join(cfg.Storage.AllowedTypes, ", "))
		}
	}
	return nil
}

func isText(data []byte) bool {
	n := len(data)
	if n > 8192 {
		n = 8192
	}
	return utf8.Valid(data[:n]) && !bytes.ContainsRune(data[:n], 0)
}

func (a *App) ParseInputs(pk, description string, fields map[string][]string, files []bufferedUpload) parsers.Result {
	cfg := a.Cfg()
	inputs := []parsers.Input{{Name: "", Content: description}}
	for id, vals := range fields {
		if f := cfg.Field(id); f != nil && f.Type == "textarea" {
			for _, v := range vals {
				inputs = append(inputs, parsers.Input{Name: id + ".txt", Content: v})
			}
		}
	}
	for _, f := range files {
		if int64(len(f.data)) <= int64(cfg.Storage.ParseMaxSize) && isText(f.data) {
			inputs = append(inputs, parsers.Input{Name: f.Name, Content: string(f.data)})
		}
	}
	return parsers.Run(cfg, pk, inputs)
}

func (a *App) ParseText(pk, text, filename string) parsers.Result {
	return parsers.Run(a.Cfg(), pk, []parsers.Input{{Name: filename, Content: text}})
}

func (a *App) CreateIssue(ctx context.Context, act *Actor, in IssueInput) (*store.Issue, error) {
	cfg := a.Cfg()
	pc := cfg.Project(in.Project)
	if pc == nil || !a.Can(act, "issue.view", pc.Key) {
		return nil, ErrNotFound
	}
	pk := pc.Key
	if err := RequireLogin(act); err != nil && !act.System {
		return nil, err
	}
	if !a.Can(act, "issue.create", pk) {
		return nil, ErrForbidden
	}
	if err := a.spamCheck(ctx, act, "issue", in.CaptchaToken); err != nil {
		return nil, err
	}
	errs := map[string]string{}
	in.Title = strings.TrimSpace(in.Title)
	if n := utf8.RuneCountInString(in.Title); n < 3 {
		errs["title"] = "Summary must be at least 3 characters"
	} else if n > 250 {
		errs["title"] = "Summary must be at most 250 characters"
	}
	if len(in.Description) > 200_000 {
		errs["description"] = "Description is too long"
	}
	fields, ferrs := a.cleanFields(ctx, act, pk, in.Fields)
	for k, v := range ferrs {
		errs[k] = v
	}
	get := func(id string) []string {
		switch id {
		case "title":
			return []string{in.Title}
		case "description":
			return []string{in.Description}
		case "affects_versions":
			return in.Affects
		case "labels":
			return in.Labels
		}
		return fields[id]
	}
	form := cfg.ProjectForm(pk)
	for _, step := range form.Steps {
		for _, ff := range step.Fields {
			if !ff.ShowIf.Match(get) {
				if ff.ShowIf != nil {
					delete(fields, ff.Field)
				}
				continue
			}
			req := false
			if f := cfg.Field(ff.Field); f != nil {
				req = f.Required
				if f.EditPermission != "" && !a.Can(act, f.EditPermission, pk) {
					continue
				}
			} else if ff.Field == "description" || ff.Field == "title" {
				req = ff.Field == "title"
			}
			if ff.Required != nil {
				req = *ff.Required
			}
			if !req || ff.Field == "attachments" {
				continue
			}
			empty := true
			for _, v := range get(ff.Field) {
				if strings.TrimSpace(v) != "" {
					empty = false
				}
			}
			if ff.Field == "description" && strings.TrimSpace(in.Description) != "" {
				empty = false
			}
			if empty && errs[ff.Field] == "" {
				label := ff.Label
				if label == "" {
					if f := cfg.Field(ff.Field); f != nil {
						label = f.Label
					} else {
						label = strings.ReplaceAll(ff.Field, "_", " ")
					}
				}
				errs[ff.Field] = label + " is required"
			}
		}
	}
	files, err := a.bufferUploads(in.Attachments)
	if err != nil {
		return nil, err
	}
	row, err := a.DB.EnsureProject(ctx, pk)
	if err != nil {
		return nil, err
	}
	affects, err := a.resolveVersions(ctx, row.ID, in.Affects)
	if err != nil {
		errs["affects_versions"] = err.Error()
	}
	if len(errs) > 0 {
		return nil, Invalid(errs)
	}

	pres := a.ParseInputs(pk, in.Description, fields, files)
	for id, vals := range pres.Fields {
		if len(fields[id]) == 0 {
			fields[id] = vals
		}
	}
	labels := a.filterLabels(act, pk, in.Labels)
	labels = append(labels, pres.Labels...)

	wf := cfg.ProjectWorkflow(pk)
	iss := &store.Issue{
		ProjectID:   row.ID,
		Title:       in.Title,
		Description: in.Description,
		State:       wf.Initial,
		ReporterID:  act.ID(),
		Private:     in.Private || pc.PrivateByDefault || a.labelsMakePrivate(labels),
		Fingerprint: pres.Fingerprint,
	}
	if pc.DefaultAssignee != "" {
		if u, err := a.DB.UserByName(ctx, pc.DefaultAssignee); err == nil {
			iss.AssigneeID = u.ID
		}
	}
	if a.needsModeration(act, pk, in.Title, in.Description) {
		iss.Moderation = "pending"
	}
	err = a.DB.InTx(ctx, func(tx *store.Tx) error {
		if err := a.DB.CreateIssue(ctx, tx, iss, pk); err != nil {
			return err
		}
		if err := a.DB.SetLabels(ctx, tx, iss.ID, labels); err != nil {
			return err
		}
		if err := a.DB.SetIssueVersions(ctx, tx, iss.ID, "affects", affects); err != nil {
			return err
		}
		ids, vals := fieldValues(cfg, fields)
		if err := a.DB.SetFieldValues(ctx, tx, iss.ID, ids, vals); err != nil {
			return err
		}
		if _, err := a.DB.SetWatch(ctx, tx, iss.ID, act.ID(), true); err != nil {
			return err
		}
		iss.Watchers = 1
		return a.DB.UpdateFTS(ctx, tx, iss.ID)
	})
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if _, err := a.storeAttachment(ctx, iss, act, f); err != nil {
			a.Log.Error("store attachment", "err", err, "issue", iss.Key)
		}
	}
	a.DB.LoadRelations(ctx, []*store.Issue{iss})
	a.emit(Event{Type: "issue.created", Issue: iss, Actor: act.User, Text: in.Description})
	return iss, nil
}

type IssuePatch struct {
	Title        *string
	Description  *string
	Fields       map[string][]string
	Labels       *[]string
	AddLabels    []string
	RemoveLabels []string
	Affects      *[]string
	FixVersions  *[]string
	Assignee     *string
	Private      *bool
}

func (a *App) UpdateIssue(ctx context.Context, act *Actor, key string, p IssuePatch) (*store.Issue, error) {
	if err := RequireLogin(act); err != nil && !act.System {
		return nil, err
	}
	iss, err := a.GetIssue(ctx, act, key)
	if err != nil {
		return nil, err
	}
	pk := iss.ProjectKey()
	cfg := a.Cfg()
	own := act.ID() == iss.ReporterID
	canEdit := a.Can(act, "issue.edit", pk) || (own && a.Can(act, "issue.edit_own", pk))
	var changes [][3]string
	errs := map[string]string{}

	if p.Title != nil || p.Description != nil {
		if !canEdit {
			return nil, ErrForbidden
		}
		if p.Title != nil {
			t := strings.TrimSpace(*p.Title)
			if utf8.RuneCountInString(t) < 3 || utf8.RuneCountInString(t) > 250 {
				errs["title"] = "Summary must be 3-250 characters"
			} else if t != iss.Title {
				changes = append(changes, [3]string{"title", iss.Title, t})
				iss.Title = t
			}
		}
		if p.Description != nil && *p.Description != iss.Description {
			changes = append(changes, [3]string{"description", "", ""})
			iss.Description = *p.Description
		}
	}
	var newFields map[string][]string
	if len(p.Fields) > 0 {
		if !canEdit {
			anyAllowed := false
			for id := range p.Fields {
				if f := cfg.Field(id); f != nil && f.EditPermission != "" && a.Can(act, f.EditPermission, pk) {
					anyAllowed = true
				}
			}
			if !anyAllowed {
				return nil, ErrForbidden
			}
			for id := range p.Fields {
				if f := cfg.Field(id); f == nil || f.EditPermission == "" {
					delete(p.Fields, id)
				}
			}
		}
		var ferrs map[string]string
		newFields, ferrs = a.cleanFields(ctx, act, pk, p.Fields)
		for k, v := range ferrs {
			errs[k] = v
		}
		for id, vals := range newFields {
			old := strings.Join(iss.Fields[id], ", ")
			nw := strings.Join(vals, ", ")
			if old != nw {
				changes = append(changes, [3]string{"field:" + id, old, nw})
			}
		}
	}
	var labels []string
	labelsChanged := false
	if p.Labels != nil || len(p.AddLabels) > 0 || len(p.RemoveLabels) > 0 {
		if !a.Can(act, "issue.label", pk) && !canEdit {
			return nil, ErrForbidden
		}
		cur := append([]string{}, iss.Labels...)
		if p.Labels != nil {
			keep := []string{}
			for _, l := range cur {
				if d := cfg.Label(l); d != nil && d.Restricted && !a.Can(act, "issue.label", pk) {
					keep = append(keep, l)
				}
			}
			cur = append(keep, a.filterLabels(act, pk, *p.Labels)...)
		}
		cur = append(cur, a.filterLabels(act, pk, p.AddLabels)...)
		rm := map[string]bool{}
		for _, l := range p.RemoveLabels {
			if d := cfg.Label(l); d != nil && d.Restricted && !a.Can(act, "issue.label", pk) {
				continue
			}
			rm[l] = true
		}
		seen := map[string]bool{}
		for _, l := range cur {
			if !rm[l] && !seen[l] {
				seen[l] = true
				labels = append(labels, l)
			}
		}
		old, nw := strings.Join(iss.Labels, ", "), strings.Join(sortedCopy(labels), ", ")
		if old != nw {
			labelsChanged = true
			changes = append(changes, [3]string{"labels", old, nw})
			if a.labelsMakePrivate(labels) && !iss.Private {
				iss.Private = true
				changes = append(changes, [3]string{"private", "false", "true"})
			}
		}
	}
	var affects, fixes []int64
	if p.Affects != nil {
		if !canEdit && !a.Can(act, "triage", pk) {
			return nil, ErrForbidden
		}
		if affects, err = a.resolveVersions(ctx, iss.ProjectID, *p.Affects); err != nil {
			errs["affects_versions"] = err.Error()
		}
		changes = append(changes, [3]string{"affects_versions", versionNames(iss.Affects), strings.Join(*p.Affects, ", ")})
	}
	if p.FixVersions != nil {
		if !a.Can(act, "issue.edit", pk) && !a.Can(act, "version.manage", pk) {
			return nil, ErrForbidden
		}
		if fixes, err = a.resolveVersions(ctx, iss.ProjectID, *p.FixVersions); err != nil {
			errs["fix_versions"] = err.Error()
		}
		changes = append(changes, [3]string{"fix_versions", versionNames(iss.FixVersions), strings.Join(*p.FixVersions, ", ")})
	}
	if p.Assignee != nil {
		if !a.Can(act, "issue.assign", pk) {
			return nil, ErrForbidden
		}
		var id int64
		name := strings.TrimPrefix(strings.TrimSpace(*p.Assignee), "@")
		if name == "me" {
			id = act.ID()
		} else if name != "" && name != "none" {
			u, err := a.DB.UserByName(ctx, name)
			if err != nil {
				errs["assignee"] = "unknown user " + name
			} else {
				id = u.ID
			}
		}
		if id != iss.AssigneeID {
			oldName, newName := "", ""
			if iss.Assignee != nil {
				oldName = iss.Assignee.Username
			}
			if id != 0 {
				if u, _ := a.DB.UserByID(ctx, id); u != nil {
					newName = u.Username
				}
			}
			changes = append(changes, [3]string{"assignee", oldName, newName})
			iss.AssigneeID = id
		}
	}
	if p.Private != nil && *p.Private != iss.Private {
		if !a.Can(act, "issue.edit", pk) && !a.Can(act, "issue.view_private", pk) && !(own && *p.Private) {
			return nil, ErrForbidden
		}
		changes = append(changes, [3]string{"private", strconv.FormatBool(iss.Private), strconv.FormatBool(*p.Private)})
		iss.Private = *p.Private
	}
	if len(errs) > 0 {
		return nil, Invalid(errs)
	}
	if len(changes) == 0 {
		return iss, nil
	}
	iss.UpdatedAt = time.Now().Unix()
	iss.ActivityAt = iss.UpdatedAt
	err = a.DB.InTx(ctx, func(tx *store.Tx) error {
		if err := a.DB.SaveIssue(ctx, tx, iss); err != nil {
			return err
		}
		if newFields != nil {
			ids, vals := fieldValues(cfg, newFields)
			if err := a.DB.SetFieldValues(ctx, tx, iss.ID, ids, vals); err != nil {
				return err
			}
		}
		if labelsChanged {
			if err := a.DB.SetLabels(ctx, tx, iss.ID, labels); err != nil {
				return err
			}
		}
		if p.Affects != nil {
			if err := a.DB.SetIssueVersions(ctx, tx, iss.ID, "affects", affects); err != nil {
				return err
			}
		}
		if p.FixVersions != nil {
			if err := a.DB.SetIssueVersions(ctx, tx, iss.ID, "fixed", fixes); err != nil {
				return err
			}
		}
		for _, c := range changes {
			if err := a.DB.AddHistory(ctx, tx, iss.ID, act.ID(), c[0], c[1], c[2]); err != nil {
				return err
			}
		}
		return a.DB.UpdateFTS(ctx, tx, iss.ID)
	})
	if err != nil {
		return nil, err
	}
	a.DB.LoadRelations(ctx, []*store.Issue{iss})
	ev := Event{Type: "issue.updated", Issue: iss, Actor: act.User, Data: map[string]any{"changes": changes}}
	if p.Description != nil {
		ev.Text = *p.Description
	}
	a.emit(ev)
	return iss, nil
}

func sortedCopy(s []string) []string {
	c := append([]string{}, s...)
	for i := 1; i < len(c); i++ {
		for j := i; j > 0 && c[j] < c[j-1]; j-- {
			c[j], c[j-1] = c[j-1], c[j]
		}
	}
	return c
}

func versionNames(vs []*store.Version) string {
	n := make([]string, len(vs))
	for i, v := range vs {
		n[i] = v.Name
	}
	return strings.Join(n, ", ")
}

func (a *App) DeleteIssue(ctx context.Context, act *Actor, key string) error {
	iss, err := a.GetIssue(ctx, act, key)
	if err != nil {
		return err
	}
	if !a.Can(act, "issue.delete", iss.ProjectKey()) {
		return ErrForbidden
	}
	atts, _ := a.DB.Attachments(ctx, iss.ID)
	if err := a.DB.DeleteIssue(ctx, iss.ID); err != nil {
		return err
	}
	for _, at := range atts {
		a.Files.Delete(ctx, at.StorageKey)
	}
	a.Log.Info("issue deleted", "key", key, "by", act.User.Username)
	return nil
}
