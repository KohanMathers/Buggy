package app

import (
	"context"
	"fmt"
	"time"

	"github.com/kohanmathers/buggy/internal/config"
	"github.com/kohanmathers/buggy/internal/store"
)

func (a *App) schedulerLoop() {
	defer a.wg.Done()
	first := time.NewTimer(10 * time.Second)
	defer first.Stop()
	iv := a.Cfg().Server.AutomationInterval.D()
	if iv <= 0 {
		iv = 5 * time.Minute
	}
	tick := time.NewTicker(iv)
	defer tick.Stop()
	house := time.NewTicker(time.Hour)
	defer house.Stop()
	for {
		select {
		case <-a.stop:
			return
		case <-first.C:
			a.RunScheduled(context.Background())
		case <-tick.C:
			a.RunScheduled(context.Background())
		case <-house.C:
			a.DB.PurgeExpiredSessions(context.Background())
			a.limiter.Sweep()
		}
	}
}

func automationQuery(au config.Automation) string {
	q := au.Query
	if d := au.Idle.D(); d > 0 {
		q += fmt.Sprintf(" updated:>%ds", int64(d.Seconds()))
	}
	return q
}

func (a *App) RunScheduled(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			a.Log.Error("automation panic", "panic", r)
		}
	}()
	sys := a.SystemActor()
	for _, au := range a.Cfg().Automations {
		if au.Disabled || !contains(au.On, "schedule") {
			continue
		}
		iq, err := a.CompileQuery(ctx, sys, automationQuery(au))
		if err != nil {
			a.Log.Error("automation query", "id", au.ID, "err", err)
			continue
		}
		iq.Limit = 200
		iq.Order = "i.updated_at ASC"
		list, _, err := a.DB.SearchIssues(ctx, iq)
		if err != nil {
			a.Log.Error("automation search", "id", au.ID, "err", err)
			continue
		}
		for _, iss := range list {
			if a.DB.AutomationRan(ctx, au.ID, iss.ID, iss.UpdatedAt) {
				continue
			}
			a.runActions(ctx, au, iss)
		}
	}
}

func (a *App) runEventAutomations(ctx context.Context, e Event) {
	if e.Issue == nil {
		return
	}
	sys := a.SystemActor()
	for _, au := range a.Cfg().Automations {
		if au.Disabled || !contains(au.On, e.Type) {
			continue
		}
		iq, err := a.CompileQuery(ctx, sys, au.Query)
		if err != nil {
			a.Log.Error("automation query", "id", au.ID, "err", err)
			continue
		}
		iq.Where = append(iq.Where, "i.id = ?")
		iq.Args = append(iq.Args, e.Issue.ID)
		iq.Limit = 1
		list, _, err := a.DB.SearchIssues(ctx, iq)
		if err != nil || len(list) == 0 {
			continue
		}
		a.runActions(ctx, au, list[0])
	}
}

func (a *App) runActions(ctx context.Context, au config.Automation, iss *store.Issue) {
	sys := a.SystemActor()
	key := iss.Key
	act := au.Actions
	fail := func(err error) {
		a.Log.Error("automation action failed", "id", au.ID, "issue", key, "err", err)
		automationsTotal.inc("failed")
	}
	p := IssuePatch{AddLabels: act.AddLabels, RemoveLabels: act.RemoveLabels, Private: act.Private}
	if len(act.SetFields) > 0 {
		p.Fields = map[string][]string{}
		for k, v := range act.SetFields {
			p.Fields[k] = []string{v}
		}
	}
	if act.Assign != "" {
		as := act.Assign
		p.Assignee = &as
	}
	if len(p.AddLabels) > 0 || len(p.RemoveLabels) > 0 || p.Private != nil || p.Fields != nil || p.Assignee != nil {
		if _, err := a.UpdateIssue(ctx, sys, key, p); err != nil {
			fail(err)
			return
		}
	}
	fresh, err := a.GetIssue(ctx, sys, key)
	if err != nil {
		fail(err)
		return
	}
	if act.Comment != "" {
		body := renderTemplate(act.Comment, ExplainData{Issue: fresh, Instance: a.Cfg().Instance.Name, Reporter: "@" + fresh.Reporter.Username})
		if _, err := a.addComment(ctx, sys, fresh, body, false, true); err != nil {
			fail(err)
			return
		}
	}
	if act.Transition != "" && fresh.State != act.Transition {
		res := act.Resolution
		wf := a.Cfg().ProjectWorkflow(fresh.ProjectKey())
		if isClosed(wf, act.Transition) && res == "" && len(wf.Resolutions) > 0 {
			res = wf.Resolutions[0].ID
		}
		if err := a.applyState(ctx, sys, fresh, act.Transition, res); err != nil {
			fail(err)
			return
		}
	}
	if after, err := a.DB.IssueByID(ctx, iss.ID); err == nil {
		a.DB.RecordAutomation(ctx, au.ID, iss.ID, after.UpdatedAt)
	}
	automationsTotal.inc(au.ID)
	a.Log.Info("automation ran", "id", au.ID, "issue", key)
}
