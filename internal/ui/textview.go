package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"go.ate.dev/8top/internal/model"
	"go.ate.dev/8top/internal/render"
	"go.ate.dev/8top/internal/style"
)

type textKind int

const (
	textYAML textKind = iota
	textDescribe
)

// objectMsg carries the text of a YAML / describe view.
type objectMsg struct {
	view int
	text string
	err  error
}

// textView shows an object as YAML, or described (YAML plus related
// resources), in a viewport.
type textView struct {
	vid    int
	kind   textKind
	obj    model.Object
	banner string // e.g. why the object can't be edited
	vp     viewport.Model
	text   string
	loaded bool
	err    error
}

func newViewport() viewport.Model {
	vp := viewport.New()
	vp.KeyMap = viewport.KeyMap{
		Up:       keyUp.Binding,
		Down:     keyDown.Binding,
		PageUp:   keyPageUp.Binding,
		PageDown: keyPageDown.Binding,
	}
	return vp
}

func (a *App) newText(kind textKind, o model.Object, banner string) *textView {
	return &textView{vid: a.newID(), kind: kind, obj: o, banner: banner, vp: newViewport()}
}

func (v *textView) id() int { return v.vid }

func (v *textView) name() string {
	if v.kind == textDescribe {
		return "describe"
	}
	return "yaml"
}

func (v *textView) crumbs() []string { return []string{v.name()} }

func (v *textView) title(t style.Theme) string {
	n := "YAML"
	if v.kind == textDescribe {
		n = "Describe"
	}
	return t.Title.Render(n+"(") + t.TitleScope.Render(string(v.obj.Kind)+"/"+objectName(v.obj)) + t.Title.Render(")")
}

func (v *textView) keys() []binding { return nil }

func (v *textView) setSize(w, h int) {
	v.vp.SetWidth(w)
	v.vp.SetHeight(h)
}

func (v *textView) activate(a *App) tea.Cmd {
	if v.loaded {
		return nil
	}
	return v.reload(a)
}

func (v *textView) deactivate() {}

func (v *textView) reload(a *App) tea.Cmd {
	api, o, kind, id := a.cfg.API, v.obj, v.kind, v.vid
	marshal, renderer, now := a.cfg.Marshal, a.cfg.Renderer, a.cfg.Now
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		cur, err := api.Get(ctx, o)
		if err != nil {
			return objectMsg{view: id, err: err}
		}
		b, err := marshal(cur)
		if err != nil {
			return objectMsg{view: id, err: err}
		}
		text := string(b)
		if kind == textDescribe {
			text += describeRelated(ctx, api, renderer, now, cur)
		}
		return objectMsg{view: id, text: text}
	}
}

func (v *textView) update(a *App, msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case objectMsg:
		v.loaded = true
		if msg.err != nil {
			v.err = msg.err
			return a.flashErr(msg.err), true
		}
		text := msg.text
		if v.banner != "" {
			text = "# " + strings.ReplaceAll(v.banner, "\n", "\n# ") + "\n" + text
		}
		v.text = text
		v.vp.SetContent(strings.TrimRight(text, "\n"))
		return nil, true
	case tea.KeyPressMsg:
		switch {
		case matches(msg, keyTop):
			v.vp.GotoTop()
			return nil, true
		case matches(msg, keyBottom):
			v.vp.GotoBottom()
			return nil, true
		case matches(msg, keyUp), matches(msg, keyDown), matches(msg, keyPageUp), matches(msg, keyPageDown):
			var cmd tea.Cmd
			v.vp, cmd = v.vp.Update(msg)
			return cmd, true
		}
	}
	return nil, false
}

func (v *textView) render(a *App) string {
	if !v.loaded {
		return a.theme.Dim.Render("Loading…")
	}
	if v.err != nil {
		return a.theme.FlashError.Render(errText(v.err))
	}
	return v.vp.View()
}

// relatedOf returns the List calls that find the resources related to o.
func relatedOf(o model.Object) []struct {
	kind  model.Kind
	scope model.Scope
} {
	type rel = struct {
		kind  model.Kind
		scope model.Scope
	}
	switch o.Kind {
	case model.KindAtespace:
		return []rel{{model.KindActor, model.Scope{Atespace: o.Name}}}
	case model.KindActor:
		return []rel{{model.KindEgressPolicy, model.Scope{Atespace: o.Atespace, Actor: o.Name}}}
	case model.KindWorker:
		return []rel{{model.KindActorAssignment, model.Scope{Worker: o.Name}}}
	}
	return nil
}

// describeRelated renders the related resources of o as small tables.
func describeRelated(ctx context.Context, api model.API, renderer func(model.Kind) render.Renderer, now func() time.Time, o model.Object) string {
	var b strings.Builder
	for _, rel := range relatedOf(o) {
		fmt.Fprintf(&b, "\n# Related %s(%s)\n", infoFor(rel.kind).title, scopeText(rel.kind, rel.scope))
		objs, err := api.List(ctx, rel.kind, rel.scope)
		if err != nil {
			fmt.Fprintf(&b, "#   error: %s\n", errText(err))
			continue
		}
		if len(objs) == 0 {
			b.WriteString("#   none\n")
			continue
		}
		r := renderer(rel.kind)
		cols := r.Columns()
		rows := [][]string{make([]string, len(cols))}
		for i, c := range cols {
			rows[0][i] = c.Name
		}
		for _, x := range objs {
			rows = append(rows, r.Render(x, now()).Cells)
		}
		widths := make([]int, len(cols))
		for _, row := range rows {
			for i := range widths {
				if i < len(row) {
					widths[i] = max(widths[i], len(row[i]))
				}
			}
		}
		for _, row := range rows {
			b.WriteString("#  ")
			for i := range widths {
				c := ""
				if i < len(row) {
					c = row[i]
				}
				fmt.Fprintf(&b, " %-*s", widths[i], c)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

func scopeText(k model.Kind, s model.Scope) string {
	tv := tableView{kind: k, scope: s}
	return tv.scopeLabel()
}
