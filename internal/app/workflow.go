package app

import (
	"bytes"
	"context"
	"strings"
	"text/template"
	"time"

	"github.com/kohanmathers/buggy/internal/config"
	"github.com/kohanmathers/buggy/internal/store"
)

type AvailableTransition struct {
	config.Transition
	ResolutionOptions []config.Resolution
	ToClosed          bool
}

func (a *App) Transitions(act *Actor, iss *store.Issue) []AvailableTransition {
	if !act.LoggedIn() {
		return nil
	}
	cfg := a.Cfg()
	pk := iss.ProjectKey()
	wf := cfg.ProjectWorkflow(pk)
	if wf == nil {
		return nil
	}
	var out []AvailableTransition
	idx := map[string]int{}
	for _, t := range wf.Transitions {
		if !t.AllowedFrom(iss.State) || t.To == iss.State && len(t.Resolutions) == 0 && !isClosed(wf, t.To) {
			continue
		}
		if !a.transitionAllowed(act, iss, &t) {
			continue
		}
		at := AvailableTransition{Transition: t, ToClosed: isClosed(wf, t.To)}
		if at.ToClosed {
			for _, r := range wf.Resolutions {
				if len(t.Resolutions) == 0 || contains(t.Resolutions, r.ID) {
					at.ResolutionOptions = append(at.ResolutionOptions, r)
				}
			}
		}
		k := t.Label + "→" + t.To
		if i, ok := idx[k]; ok {
			if len(at.ResolutionOptions) > len(out[i].ResolutionOptions) {
				out[i] = at
			}
			continue
		}
		idx[k] = len(out)
		out = append(out, at)
	}
	return out
}

func (a *App) transitionAllowed(act *Actor, iss *store.Issue, t *config.Transition) bool {
	pk := iss.ProjectKey()
	if t.Reporter && act.ID() == iss.ReporterID && act.ID() != 0 {
		return true
	}
	if t.Permission == "" {
		return false
	}
	return a.Can(act, t.Permission, pk)
}

func isClosed(wf *config.Workflow, state string) bool {
	if s := wf.State(state); s != nil {
		return s.Closed()
	}
	return false
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func (a *App) Transition(ctx context.Context, act *Actor, key, transitionID, resolution, comment string) (*store.Issue, error) {
	if err := RequireLogin(act); err != nil && !act.System {
		return nil, err
	}
	iss, err := a.GetIssue(ctx, act, key)
	if err != nil {
		return nil, err
	}
	cfg := a.Cfg()
	wf := cfg.ProjectWorkflow(iss.ProjectKey())
	t := wf.Transition(transitionID)
	if t == nil {
		return nil, Errf(422, "unknown transition %q", transitionID)
	}
	if !t.AllowedFrom(iss.State) {
		return nil, Errf(409, "can't %s from %s", t.Label, iss.State)
	}
	if !act.System && !a.transitionAllowed(act, iss, t) {
		return nil, ErrForbidden
	}
	if t.RequireComment && strings.TrimSpace(comment) == "" {
		return nil, Invalid(map[string]string{"comment": "A comment is required for " + t.Label})
	}
	if isClosed(wf, t.To) {
		if resolution == "" {
			if len(t.Resolutions) == 1 {
				resolution = t.Resolutions[0]
			} else {
				return nil, Invalid(map[string]string{"resolution": "Choose a resolution"})
			}
		}
		if wf.Resolution(resolution) == nil || (len(t.Resolutions) > 0 && !contains(t.Resolutions, resolution)) {
			return nil, Invalid(map[string]string{"resolution": "Invalid resolution"})
		}
		if resolution == wf.DuplicateResolution && iss.DuplicateOf == 0 {
			return nil, Invalid(map[string]string{"resolution": "Use “Mark as duplicate” to resolve as a duplicate so votes are moved"})
		}
	} else {
		resolution = ""
	}
	if err := a.applyState(ctx, act, iss, t.To, resolution); err != nil {
		return nil, err
	}
	if strings.TrimSpace(comment) != "" {
		if _, err := a.addComment(ctx, act, iss, comment, false, true); err != nil {
			return nil, err
		}
	}
	return iss, nil
}

func (a *App) applyState(ctx context.Context, act *Actor, iss *store.Issue, to, resolution string) error {
	cfg := a.Cfg()
	wf := cfg.ProjectWorkflow(iss.ProjectKey())
	if wf.State(to) == nil {
		return Errf(422, "unknown state %q", to)
	}
	from, fromRes := iss.State, iss.Resolution
	if from == to && fromRes == resolution {
		return nil
	}
	t := time.Now().Unix()
	iss.State, iss.Resolution = to, resolution
	if isClosed(wf, to) {
		if iss.ResolvedAt == 0 {
			iss.ResolvedAt = t
		}
	} else {
		iss.ResolvedAt = 0
		iss.Resolution = ""
		if iss.DuplicateOf != 0 {
			iss.DuplicateOf = 0
		}
	}
	iss.UpdatedAt, iss.ActivityAt = t, t
	err := a.DB.InTx(ctx, func(tx *store.Tx) error {
		if err := a.DB.SaveIssue(ctx, tx, iss); err != nil {
			return err
		}
		if err := a.DB.AddHistory(ctx, tx, iss.ID, act.ID(), "state", from, to); err != nil {
			return err
		}
		return a.DB.AddHistory(ctx, tx, iss.ID, act.ID(), "resolution", fromRes, iss.Resolution)
	})
	if err != nil {
		return err
	}
	a.emit(Event{Type: "issue.transitioned", Issue: iss, Actor: act.User,
		Data: map[string]any{"from": from, "to": to, "resolution": iss.Resolution}})
	return nil
}

func (a *App) MarkDuplicate(ctx context.Context, act *Actor, dupKey, origKey, comment string) (*store.Issue, error) {
	if err := RequireLogin(act); err != nil && !act.System {
		return nil, err
	}
	dup, err := a.GetIssue(ctx, act, dupKey)
	if err != nil {
		return nil, err
	}
	orig, err := a.GetIssue(ctx, act, origKey)
	if err != nil {
		return nil, Errf(404, "issue %s not found", origKey)
	}
	for i := 0; orig.DuplicateOf != 0 && i < 10; i++ {
		next, err := a.DB.IssueByID(ctx, orig.DuplicateOf)
		if err != nil {
			break
		}
		a.DB.LoadRelations(ctx, []*store.Issue{next})
		orig = next
	}
	if dup.ID == orig.ID {
		return nil, Errf(422, "an issue can't be a duplicate of itself")
	}
	pk := dup.ProjectKey()
	if !a.Can(act, "issue.duplicate", pk) && !a.Can(act, "issue.transition", pk) {
		return nil, ErrForbidden
	}
	wf := a.Cfg().ProjectWorkflow(pk)
	state := wf.DuplicateState
	if state == "" {
		state = wf.FirstStateIn("closed")
	}
	res := wf.DuplicateResolution
	if state == "" || res == "" {
		return nil, Errf(500, "workflow %q has no duplicate_state/duplicate_resolution configured", a.Cfg().Project(pk).Workflow)
	}
	env := map[string]string{}
	for _, f := range a.Cfg().ProjectFields(pk) {
		if f.Environment && dup.Field(f.ID) != "" {
			env[f.ID] = dup.Field(f.ID)
		}
	}
	var verID int64
	if len(dup.Affects) > 0 && dup.ProjectID == orig.ProjectID {
		verID = dup.Affects[len(dup.Affects)-1].ID
	}
	err = a.DB.InTx(ctx, func(tx *store.Tx) error {
		if _, err := a.DB.MoveSocial(ctx, tx, dup.ID, orig.ID); err != nil {
			return err
		}
		if dup.ReporterID != orig.ReporterID {
			if _, err := a.DB.SetVote(ctx, tx, orig.ID, dup.ReporterID, true); err != nil {
				return err
			}
			if _, err := a.DB.SetWatch(ctx, tx, orig.ID, dup.ReporterID, true); err != nil {
				return err
			}
			if err := a.DB.AddConfirmation(ctx, tx, &store.Confirmation{
				IssueID: orig.ID, UserID: dup.ReporterID, VersionID: verID, Env: encodeEnv(env), Note: "via " + dup.Key,
			}); err != nil {
				return err
			}
			if verID != 0 {
				if err := a.DB.AddIssueVersion(ctx, tx, orig.ID, verID, "affects"); err != nil {
					return err
				}
			}
		}
		if err := a.DB.AddLink(ctx, tx, dup.ID, orig.ID, "duplicates", act.ID()); err != nil {
			return err
		}
		if err := a.DB.AddHistory(ctx, tx, dup.ID, act.ID(), "duplicate", "", orig.Key); err != nil {
			return err
		}
		return a.DB.AddHistory(ctx, tx, orig.ID, act.ID(), "duplicated_by", "", dup.Key)
	})
	if err != nil {
		return nil, err
	}
	dup.DuplicateOf = orig.ID
	if err := a.applyState(ctx, act, dup, state, res); err != nil {
		return nil, err
	}
	if strings.TrimSpace(comment) != "" {
		a.addComment(ctx, act, dup, comment, false, true)
	}
	a.emit(Event{Type: "issue.duplicate", Issue: dup, Actor: act.User, Data: map[string]any{"original": orig.Key}})
	return dup, nil
}

type ExplainData struct {
	Issue       *store.Issue
	Duplicate   string
	FixVersions string
	Reporter    string
	Instance    string
}

func (a *App) Explain(ctx context.Context, iss *store.Issue) string {
	cfg := a.Cfg()
	wf := cfg.ProjectWorkflow(iss.ProjectKey())
	if wf == nil {
		return ""
	}
	tpl := ""
	if iss.Resolution != "" {
		if r := wf.Resolution(iss.Resolution); r != nil {
			tpl = r.Explain
		}
	}
	if tpl == "" {
		if s := wf.State(iss.State); s != nil {
			tpl = s.Explain
		}
	}
	if tpl == "" {
		return ""
	}
	d := ExplainData{Issue: iss, Instance: cfg.Instance.Name, FixVersions: versionNames(iss.FixVersions)}
	if iss.Reporter != nil {
		d.Reporter = "@" + iss.Reporter.Username
	}
	if iss.DuplicateOf != 0 {
		if o, err := a.DB.IssueByID(ctx, iss.DuplicateOf); err == nil {
			d.Duplicate = o.Key
		}
	}
	return renderTemplate(tpl, d)
}

func (a *App) ExplainExample(tpl string) string {
	return renderTemplate(tpl, ExplainData{
		Issue:     &store.Issue{Key: "the issue"},
		Duplicate: "the original issue",
		Reporter:  "the reporter",
		Instance:  a.Cfg().Instance.Name,
	})
}

func renderTemplate(tpl string, data any) string {
	t, err := template.New("x").Option("missingkey=zero").Parse(tpl)
	if err != nil {
		return tpl
	}
	var b bytes.Buffer
	if err := t.Execute(&b, data); err != nil {
		return tpl
	}
	return b.String()
}

func (a *App) ApplyCanned(ctx context.Context, act *Actor, key, cannedID, extra string) (*store.Issue, error) {
	cfg := a.Cfg()
	var cr *config.CannedResponse
	for i := range cfg.Canned {
		if cfg.Canned[i].ID == cannedID {
			cr = &cfg.Canned[i]
		}
	}
	if cr == nil {
		return nil, Errf(404, "unknown canned response")
	}
	iss, err := a.GetIssue(ctx, act, key)
	if err != nil {
		return nil, err
	}
	if !a.Can(act, "canned.use", iss.ProjectKey()) {
		return nil, ErrForbidden
	}
	body := renderTemplate(cr.Body, ExplainData{Issue: iss, Instance: cfg.Instance.Name, Reporter: "@" + iss.Reporter.Username})
	if strings.TrimSpace(extra) != "" {
		body += "\n\n" + extra
	}
	if len(cr.AddLabels) > 0 {
		if _, err := a.UpdateIssue(ctx, act, key, IssuePatch{AddLabels: cr.AddLabels}); err != nil {
			return nil, err
		}
	}
	if cr.Transition != "" {
		return a.Transition(ctx, act, key, cr.Transition, cr.Resolution, body)
	}
	if _, err := a.AddComment(ctx, act, key, body, cr.Restricted, ""); err != nil {
		return nil, err
	}
	return a.GetIssue(ctx, act, key)
}

type BulkAction struct {
	Transition   string
	Resolution   string
	Comment      string
	AddLabels    []string
	RemoveLabels []string
	Assignee     *string
	DuplicateOf  string
}

type BulkResult struct {
	Key   string `json:"key"`
	Error string `json:"error,omitempty"`
}

func (a *App) Bulk(ctx context.Context, act *Actor, keys []string, b BulkAction) ([]BulkResult, error) {
	if err := RequireLogin(act); err != nil {
		return nil, err
	}
	if !a.Can(act, "issue.bulk", "") && !a.Can(act, "triage", "") {
		return nil, ErrForbidden
	}
	if len(keys) > 500 {
		return nil, Errf(422, "at most 500 issues at once")
	}
	var out []BulkResult
	for _, k := range keys {
		r := BulkResult{Key: k}
		var err error
		if len(b.AddLabels) > 0 || len(b.RemoveLabels) > 0 || b.Assignee != nil {
			_, err = a.UpdateIssue(ctx, act, k, IssuePatch{AddLabels: b.AddLabels, RemoveLabels: b.RemoveLabels, Assignee: b.Assignee})
		}
		if err == nil && b.DuplicateOf != "" {
			_, err = a.MarkDuplicate(ctx, act, k, b.DuplicateOf, b.Comment)
		} else if err == nil && b.Transition != "" {
			_, err = a.Transition(ctx, act, k, b.Transition, b.Resolution, b.Comment)
		} else if err == nil && b.Comment != "" {
			_, err = a.AddComment(ctx, act, k, b.Comment, false, "")
		}
		if err != nil {
			r.Error = err.Error()
		}
		out = append(out, r)
	}
	return out, nil
}
