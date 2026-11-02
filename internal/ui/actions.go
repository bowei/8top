package ui

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"go.ate.dev/8top/internal/model"
)

// actionDoneMsg is the result of an action. It goes to the flash line and
// triggers a refresh of the active view.
type actionDoneMsg struct {
	ok  string
	err error
}

// runAction starts fn as a tea.Cmd. progress is shown while it runs, ok when
// it succeeds.
func (a *App) runAction(progress, ok string, fn func(ctx context.Context) error) tea.Cmd {
	// The progress message stays up until the result replaces it.
	a.flash = flash{text: progress, level: flashInfo, seq: a.flash.seq + 1}
	return tea.Batch(a.startSpinner(), func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		return actionDoneMsg{ok: ok, err: fn(ctx)}
	})
}

func (a *App) actionDone(msg actionDoneMsg) tea.Cmd {
	if a.inflight > 0 {
		a.inflight--
	}
	var flash tea.Cmd
	if msg.err != nil {
		flash = a.flashErr(msg.err)
	} else {
		flash = a.flashInfo(msg.ok)
	}
	return tea.Batch(flash, a.refresh())
}

// doAction runs the resource action b on the selected object o of the
// table view tv.
func (a *App) doAction(tv *tableView, b binding, o model.Object) tea.Cmd {
	api := a.cfg.API
	name := objectName(o)
	switch b.Help().Key {
	case keyYAML.Help().Key:
		return a.push(a.newText(textYAML, o, ""))
	case keyDescribe.Help().Key:
		return a.push(a.newText(textDescribe, o, ""))
	case keyEdit.Help().Key:
		return a.startEdit(o)
	case keyDelete.Help().Key:
		return a.ask(confirm{
			title:   "Delete",
			message: fmt.Sprintf("Delete %s %s?", singular(o.Kind), name),
			run: func(bool) tea.Cmd {
				return a.runAction("Deleting "+name+"…", fmt.Sprintf("Deleted %s %s", singular(o.Kind), name),
					func(ctx context.Context) error { return api.Delete(ctx, o) })
			},
		})
	case keyResume.Help().Key:
		return a.runAction("Resuming "+name+"…", "Resume requested for actor "+name,
			func(ctx context.Context) error { return api.ResumeActor(ctx, o.Atespace, o.Name) })
	case keySuspend.Help().Key:
		return a.ask(confirm{
			title:   "Suspend",
			message: fmt.Sprintf("Suspend actor %s?", name),
			option:  "Wait for the snapshot to reach storage",
			run: func(wait bool) tea.Cmd {
				return a.runAction("Suspending "+name+"…", "Suspend requested for actor "+name,
					func(ctx context.Context) error { return api.SuspendActor(ctx, o.Atespace, o.Name, wait) })
			},
		})
	case keyRevert.Help().Key:
		return a.ask(confirm{
			title:   "Revert",
			message: fmt.Sprintf("Revert actor %s to its last external snapshot?", name),
			run: func(bool) tea.Cmd {
				return a.runAction("Reverting "+name+"…", "Revert requested for actor "+name,
					func(ctx context.Context) error { return api.RevertActor(ctx, o.Atespace, o.Name) })
			},
		})
	case keyEgress.Help().Key:
		a.setAtespace(o.Atespace)
		return a.push(a.newTable(model.KindEgressPolicy, model.Scope{Atespace: o.Atespace, Actor: o.Name}))
	case keyLogs.Help().Key:
		return a.push(a.newLogs(o))
	case keyPortForward.Help().Key:
		return a.flashWarn("Port-forward is not implemented yet")
	case keyTag.Help().Key:
		return a.openPrompt(promptTag, "", o)
	case keyDrain.Help().Key:
		return a.ask(confirm{
			title:   "Drain",
			message: fmt.Sprintf("Drain worker %s?", name),
			run: func(bool) tea.Cmd {
				return a.runAction("Draining "+name+"…", "Drain requested for worker "+name,
					func(ctx context.Context) error { return api.DrainWorker(ctx, o.Name) })
			},
		})
	case keyAvailable.Help().Key:
		return a.runAction("Marking "+name+" available…", "Marked worker "+name+" available",
			func(ctx context.Context) error { return api.SetWorkerAvailable(ctx, o.Name, true) })
	case keyUnavailable.Help().Key:
		return a.runAction("Marking "+name+" unavailable…", "Marked worker "+name+" unavailable",
			func(ctx context.Context) error { return api.SetWorkerAvailable(ctx, o.Name, false) })
	}
	return nil
}

// createTag runs CreateTag for the tag prompt.
func (a *App) createTag(o model.Object, tag string) tea.Cmd {
	if tag == "" {
		return a.flashWarn("Tag cancelled: no name given")
	}
	name := objectName(o)
	return a.runAction("Tagging "+name+"…", fmt.Sprintf("Tagged the snapshot of %s as %s", name, tag),
		func(ctx context.Context) error { return a.cfg.API.CreateTag(ctx, o.Atespace, tag, o.Name) })
}

// drill opens the view under o (`enter`).
func (a *App) drill(o model.Object) tea.Cmd {
	switch o.Kind {
	case model.KindAtespace:
		a.setAtespace(o.Name)
		return a.push(a.newTable(model.KindActor, model.Scope{Atespace: o.Name}))
	case model.KindActor:
		a.setAtespace(o.Atespace)
		return a.push(a.newTable(model.KindEgressPolicy, model.Scope{Atespace: o.Atespace, Actor: o.Name}))
	case model.KindWorker:
		return a.push(a.newTable(model.KindActorAssignment, model.Scope{Worker: o.Name}))
	}
	return a.push(a.newText(textYAML, o, ""))
}

func singular(k model.Kind) string {
	switch k {
	case model.KindAtespace:
		return "atespace"
	case model.KindActor:
		return "actor"
	case model.KindActorTemplate:
		return "template"
	case model.KindTag:
		return "tag"
	case model.KindEgressPolicy:
		return "egress policy of"
	case model.KindWorker:
		return "worker"
	case model.KindActorAssignment:
		return "assignment"
	case model.KindAccessPolicy:
		return "access policy"
	case model.KindWorkerPool:
		return "worker pool"
	}
	return string(k)
}
