package ui

import (
	"bytes"
	"context"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"go.ate.dev/8top/internal/model"
)

// editSession is one `E` round-trip: dump → $EDITOR → apply, reopening the
// editor with the error on top until the update succeeds or the user makes
// no further change.
type editSession struct {
	orig model.Object
	path string
	// written is what 8top last wrote to the file.
	written []byte
	// lastErr is the error the file was annotated with, if any.
	lastErr error
}

// editStartMsg reports that the edit file is ready (or failed).
type editStartMsg struct {
	s   *editSession
	err error
}

// editorExitMsg reports that the editor exited.
type editorExitMsg struct {
	s   *editSession
	err error
}

// editResultMsg is the result of applying the edited file.
type editResultMsg struct {
	s         *editSession
	content   []byte
	changed   bool
	cancelled bool // unchanged after an error: give up
	err       error
}

// startEdit handles `E` on o.
func (a *App) startEdit(o model.Object) tea.Cmd {
	if a.cfg.ReadOnly {
		return a.flashWarn("8top is in read-only mode")
	}
	if a.edit != nil {
		return a.flashWarn("An edit is already in progress")
	}
	if ok, reason := a.cfg.Editable(o.Kind); !ok {
		// Show the YAML read-only, with the reason as a hint (templates
		// are immutable).
		return tea.Batch(a.flashWarn(reason), a.push(a.newText(textYAML, o, reason)))
	}
	s := &editSession{orig: o}
	a.edit = s
	api, editFile, dir := a.cfg.API, a.cfg.EditFile, a.cfg.TempDir
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		cur, err := api.Get(ctx, o)
		if err != nil {
			return editStartMsg{s: s, err: err}
		}
		s.orig = cur
		content, err := editFile(cur)
		if err != nil {
			return editStartMsg{s: s, err: err}
		}
		f, err := os.CreateTemp(dir, "8top-edit-*.yaml")
		if err != nil {
			return editStartMsg{s: s, err: err}
		}
		defer f.Close()
		if _, err := f.Write(content); err != nil {
			os.Remove(f.Name())
			return editStartMsg{s: s, err: err}
		}
		s.path, s.written = f.Name(), content
		return editStartMsg{s: s}
	}
}

func (a *App) openEditor(s *editSession) tea.Cmd {
	return a.cfg.Exec(a.cfg.EditorCommand(s.path), func(err error) tea.Msg {
		return editorExitMsg{s: s, err: err}
	})
}

func (a *App) updateEdit(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case editStartMsg:
		if msg.err != nil {
			return a.finishEdit(msg.s, a.flashErr(msg.err))
		}
		return a.openEditor(msg.s)
	case editorExitMsg:
		s := msg.s
		if msg.err != nil {
			return a.finishEdit(s, a.flashErr(fmt.Errorf("editor: %w", msg.err)))
		}
		api, apply := a.cfg.API, a.cfg.Apply
		return func() tea.Msg {
			content, err := os.ReadFile(s.path)
			if err != nil {
				return editResultMsg{s: s, err: err}
			}
			if s.lastErr != nil && bytes.Equal(content, s.written) {
				return editResultMsg{s: s, cancelled: true}
			}
			ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
			defer cancel()
			_, changed, err := apply(ctx, api, s.orig, content)
			return editResultMsg{s: s, content: content, changed: changed, err: err}
		}
	case editResultMsg:
		s := msg.s
		switch {
		case msg.cancelled:
			return a.finishEdit(s, a.flashErr(fmt.Errorf("Edit cancelled, no changes made after error: %s", errText(s.lastErr))))
		case msg.err != nil && msg.content == nil:
			return a.finishEdit(s, a.flashErr(msg.err))
		case msg.err != nil:
			// Reopen the editor with the error on top so no edits are
			// lost.
			s.lastErr = msg.err
			s.written = a.cfg.Annotate(msg.content, msg.err)
			flash := a.flashErr(msg.err)
			return tea.Batch(flash, func() tea.Msg {
				if err := os.WriteFile(s.path, s.written, 0o600); err != nil {
					return editStartMsg{s: s, err: err}
				}
				return editStartMsg{s: s}
			})
		case !msg.changed:
			return a.finishEdit(s, a.flashInfo("Edit cancelled, no changes made"))
		default:
			return a.finishEdit(s, tea.Batch(
				a.flashInfo(fmt.Sprintf("Updated %s %s", singular(s.orig.Kind), objectName(s.orig))),
				a.refresh()))
		}
	}
	return nil
}

// finishEdit ends the session and removes the temp file.
func (a *App) finishEdit(s *editSession, cmd tea.Cmd) tea.Cmd {
	if a.edit == s {
		a.edit = nil
	}
	path := s.path
	if path == "" {
		return cmd
	}
	return tea.Batch(cmd, func() tea.Msg {
		os.Remove(path)
		return nil
	})
}
