package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// confirm is a yes/no dialog for destructive or disruptive actions.
type confirm struct {
	title   string
	message string
	// option, if set, is a checkbox toggled with `w` (e.g. wait for the
	// snapshot when suspending). Its value is passed to run.
	option   string
	optionOn bool
	// okFocused is which button enter presses. Cancel is the default.
	okFocused bool
	run       func(option bool) tea.Cmd
}

func (a *App) ask(c confirm) tea.Cmd {
	a.confirm = &c
	return nil
}

func (a *App) updateConfirm(msg tea.KeyPressMsg) tea.Cmd {
	c := a.confirm
	switch msg.String() {
	case "y", "Y":
		a.confirm = nil
		return c.run(c.optionOn)
	case "n", "N", "esc", "q":
		a.confirm = nil
		return nil
	case "w":
		if c.option != "" {
			c.optionOn = !c.optionOn
		}
	case "tab", "shift+tab", "left", "right", "h", "l":
		c.okFocused = !c.okFocused
	case "enter":
		a.confirm = nil
		if c.okFocused {
			return c.run(c.optionOn)
		}
	}
	return nil
}

func (a *App) renderConfirm() string {
	t := a.theme
	c := a.confirm
	longest := max(ansi.StringWidth(c.message), ansi.StringWidth("[ ] "+c.option+" (w)"))
	width := min(max(longest+4, 40), max(a.width-4, 10))
	inner := width - 4
	var lines []string
	lines = append(lines, t.DialogTitle.Render("<"+c.title+">"), "")
	for _, l := range strings.Split(ansi.Wordwrap(c.message, inner, " "), "\n") {
		lines = append(lines, t.Dialog.Render(l))
	}
	if c.option != "" {
		box := "[ ]"
		if c.optionOn {
			box = "[x]"
		}
		lines = append(lines, "", t.Dialog.Render(box+" "+c.option+" (w)"))
	}
	ok, cancel := t.Button, t.ButtonActive
	if c.okFocused {
		ok, cancel = t.ButtonActive, t.Button
	}
	buttons := ok.Render(" OK (y) ") + "  " + cancel.Render(" Cancel (n) ")
	lines = append(lines, "", lipgloss.PlaceHorizontal(inner, lipgloss.Center, buttons))
	for i, l := range lines {
		lines[i] = fit(l, inner)
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.DialogTitle.GetForeground()).
		Padding(0, 1).
		Render(strings.Join(lines, "\n"))
}
