package ui

import (
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"go.ate.dev/8top/internal/model"
)

type promptKind int

const (
	promptCommand promptKind = iota
	promptFilter
	promptTag
)

// prompt is the one-line input above the body: `:` commands, `/` filters,
// and the tag name for `t`.
type prompt struct {
	kind   promptKind
	input  textinput.Model
	target model.Object // promptTag
	// orig is the filter before the prompt opened, restored on esc.
	orig string
}

func (a *App) openPrompt(kind promptKind, value string, target ...model.Object) tea.Cmd {
	in := textinput.New()
	st := textinput.DefaultDarkStyles()
	st.Cursor.Blink = false
	st.Focused.Prompt = a.theme.Prompt
	st.Focused.Text = lipgloss.NewStyle()
	st.Focused.Suggestion = a.theme.Dim
	in.SetStyles(st)
	switch kind {
	case promptCommand:
		in.Prompt = ":"
		in.ShowSuggestions = true
		in.SetSuggestions(commandNames())
	case promptFilter:
		in.Prompt = "/"
	case promptTag:
		in.Prompt = "Tag name: "
		in.Placeholder = "e.g. v1"
	}
	in.SetValue(value)
	in.CursorEnd()
	p := &prompt{kind: kind, input: in, orig: value}
	if len(target) > 0 {
		p.target = target[0]
	}
	a.prompt = p
	cmd := a.prompt.input.Focus()
	a.layout()
	return cmd
}

func (a *App) closePrompt() {
	a.prompt = nil
	a.layout()
}

func (a *App) updatePrompt(msg tea.KeyPressMsg) tea.Cmd {
	p := a.prompt
	switch msg.String() {
	case "esc":
		a.closePrompt()
		if p.kind == promptFilter {
			if tv, ok := a.top().(*tableView); ok {
				tv.setFilter("")
			}
		}
		return nil
	case "enter":
		a.closePrompt()
		val := p.input.Value()
		switch p.kind {
		case promptCommand:
			return a.runCommand(val)
		case promptFilter:
			if tv, ok := a.top().(*tableView); ok {
				tv.setFilter(val)
			}
			return nil
		case promptTag:
			return a.createTag(p.target, val)
		}
		return nil
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	if p.kind == promptFilter {
		// Filter as you type.
		if tv, ok := a.top().(*tableView); ok {
			tv.setFilter(p.input.Value())
		}
	}
	return cmd
}

// runCommand executes a `:` command line.
func (a *App) runCommand(line string) tea.Cmd {
	if line == "" {
		return nil
	}
	c, err := parseCommand(line, a.atespace)
	if err != nil {
		return a.flashErr(err)
	}
	if c.quit {
		return tea.Quit
	}
	if c.setAtespace {
		a.setAtespace(c.scope.Atespace)
	}
	return a.reset(a.newTable(c.kind, c.scope))
}

func (a *App) renderPrompt() string {
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(a.theme.PromptBorder.GetForeground()).
		Width(a.width)
	inner := a.width - 2
	return box.Render(fit(a.prompt.input.View(), inner))
}
