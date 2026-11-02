package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"go.ate.dev/8top/internal/style"
)

// helpView lists the keys of the view it was opened from.
type helpView struct {
	vid       int
	of        view
	favorites []string
	w, h      int
}

func (a *App) newHelp(of view) *helpView {
	return &helpView{vid: a.newID(), of: of, favorites: a.favorites}
}

func (v *helpView) id() int          { return v.vid }
func (v *helpView) crumbs() []string { return []string{"help"} }
func (v *helpView) title(t style.Theme) string {
	return t.Title.Render("Help")
}
func (v *helpView) keys() []binding                      { return nil }
func (v *helpView) setSize(w, h int)                     { v.w, v.h = w, h }
func (v *helpView) activate(*App) tea.Cmd                { return nil }
func (v *helpView) deactivate()                          {}
func (v *helpView) update(*App, tea.Msg) (tea.Cmd, bool) { return nil, false }

// globalKeys are the keys handled by the App in every view.
func globalKeys() []binding {
	return []binding{keyCommand, keyFilter, keyBack, keyEnter, keyHelp, keyAll, keyFav, keySort, keyRefresh, keyQuit}
}

// helpGroups returns the help columns for view of: its own keys, the
// global keys, and navigation.
func helpGroups(of view) [][]binding {
	var groups [][]binding
	if ks := of.keys(); len(ks) > 0 {
		groups = append(groups, ks)
	}
	g := globalKeys()
	if _, ok := of.(*tableView); !ok {
		// Filter, drill-down, atespaces and sorting are table keys.
		g = []binding{keyCommand, keyBack, keyHelp, keyRefresh, keyQuit}
	}
	groups = append(groups, g, navigationKeys())
	return groups
}

func (v *helpView) render(a *App) string {
	t := a.theme
	headings := []string{"RESOURCE", "GENERAL", "NAVIGATION"}
	groups := helpGroups(v.of)
	if len(groups) == 2 {
		headings = headings[1:]
	}
	h := help.New()
	h.Styles = help.Styles{FullKey: t.HintKey, FullDesc: t.HintDesc, FullSeparator: t.HintDesc, Ellipsis: t.HintDesc}
	h.SetWidth(v.w)
	var cols [][]key.Binding
	for i, g := range groups {
		heading := key.NewBinding(key.WithKeys("_"), key.WithHelp(headings[i], ""))
		cols = append(cols, append([]key.Binding{heading}, keyBindings(g)...))
	}
	var b strings.Builder
	b.WriteString(h.FullHelpView(cols))
	if len(v.favorites) > 0 {
		b.WriteString("\n\n")
		b.WriteString(t.TableHeader.Render("ATESPACES"))
		b.WriteString("\n")
		b.WriteString(t.HintNumKey.Render("<0>") + " " + t.HintNumDesc.Render("all"))
		for i, f := range v.favorites {
			b.WriteString("  " + t.HintNumKey.Render(fmt.Sprintf("<%d>", i+1)) + " " + t.HintNumDesc.Render(f))
		}
	}
	return b.String()
}
