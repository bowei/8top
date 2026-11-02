package ui

import (
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
)

type flashLevel int

const (
	flashInfo flashLevel = iota
	flashWarn
	flashError
)

// flash is the message line under the breadcrumbs.
type flash struct {
	text  string
	level flashLevel
	// seq identifies the message so that its clear timer does not clear a
	// newer one.
	seq int
}

type flashClearMsg struct{ seq int }

type spinnerTickMsg struct{}

var spinnerFrames = spinner.Dot.Frames

const spinnerInterval = 100 * time.Millisecond

func (a *App) setFlash(level flashLevel, text string) tea.Cmd {
	seq := a.flash.seq + 1
	a.flash = flash{text: text, level: level, seq: seq}
	return a.cfg.Tick(flashTimeout, func(time.Time) tea.Msg { return flashClearMsg{seq: seq} })
}

func (a *App) flashInfo(text string) tea.Cmd { return a.setFlash(flashInfo, text) }
func (a *App) flashWarn(text string) tea.Cmd { return a.setFlash(flashWarn, text) }
func (a *App) flashErr(err error) tea.Cmd    { return a.setFlash(flashError, errText(err)) }

func (a *App) renderFlash() string {
	if a.inflight > 0 && a.flash.text == "" {
		f := spinnerFrames[a.spinnerFrame%len(spinnerFrames)]
		return a.theme.FlashInfo.Render(f + " Working…")
	}
	if a.flash.text == "" {
		return ""
	}
	st := a.theme.FlashInfo
	switch a.flash.level {
	case flashWarn:
		st = a.theme.FlashWarn
	case flashError:
		st = a.theme.FlashError
	}
	text := a.flash.text
	if a.inflight > 0 {
		text = spinnerFrames[a.spinnerFrame%len(spinnerFrames)] + " " + text
	}
	return st.Render(text)
}

// startSpinner counts an in-flight action and starts the spinner ticking.
func (a *App) startSpinner() tea.Cmd {
	a.inflight++
	if a.spinnerOn {
		return nil
	}
	a.spinnerOn = true
	return a.cfg.Tick(spinnerInterval, func(time.Time) tea.Msg { return spinnerTickMsg{} })
}
