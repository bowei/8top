package ui

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"go.ate.dev/8top/internal/model"
	"go.ate.dev/8top/internal/style"
)

// maxLogLines bounds the logs buffer.
const maxLogLines = 5000

// maxBatch bounds the number of lines per logLinesMsg.
const maxBatch = 500

// logStartMsg reports that the log stream opened (or failed to).
type logStartMsg struct {
	view int
	rc   io.ReadCloser
	err  error
}

// logLinesMsg is one batch of log lines. err is io.EOF when the stream
// ended.
type logLinesMsg struct {
	view  int
	lines []string
	err   error
}

// logsView streams an actor's logs into a viewport.
type logsView struct {
	vid    int
	obj    model.Object
	vp     viewport.Model
	lines  []string
	follow bool

	started bool
	cancel  context.CancelFunc
	rc      io.ReadCloser
	br      *bufio.Reader
	ended   bool
	err     error
}

func (a *App) newLogs(o model.Object) *logsView {
	return &logsView{vid: a.newID(), obj: o, vp: newViewport(), follow: true}
}

func (v *logsView) id() int          { return v.vid }
func (v *logsView) crumbs() []string { return []string{"logs"} }

func (v *logsView) title(t style.Theme) string {
	s := t.Title.Render("Logs(") + t.TitleScope.Render(objectName(v.obj)) + t.Title.Render(")")
	if v.follow {
		s += t.TitleFilter.Render("[follow]")
	}
	if v.ended {
		s += t.Title.Render("[ended]")
	}
	return s
}

func (v *logsView) keys() []binding { return []binding{keyFollow} }

func (v *logsView) setSize(w, h int) {
	v.vp.SetWidth(w)
	v.vp.SetHeight(h)
	if v.follow {
		v.vp.GotoBottom()
	}
}

func (v *logsView) activate(a *App) tea.Cmd {
	if v.started {
		return nil
	}
	v.started = true
	ctx, cancel := context.WithCancel(context.Background())
	v.cancel = cancel
	api, o, id := a.cfg.API, v.obj, v.vid
	return func() tea.Msg {
		rc, err := api.StreamLogs(ctx, o.Atespace, o.Name, true)
		return logStartMsg{view: id, rc: rc, err: err}
	}
}

func (v *logsView) deactivate() {
	if v.cancel != nil {
		v.cancel()
	}
	if v.rc != nil {
		v.rc.Close()
	}
}

func (v *logsView) read() tea.Cmd {
	br, id := v.br, v.vid
	return func() tea.Msg {
		lines, err := readBatch(br, maxBatch)
		return logLinesMsg{view: id, lines: lines, err: err}
	}
}

// readBatch blocks for one line, then takes every further complete line
// that is already buffered, up to max lines.
func readBatch(br *bufio.Reader, max int) ([]string, error) {
	var lines []string
	for len(lines) < max {
		if len(lines) > 0 {
			buf, _ := br.Peek(br.Buffered())
			if bytes.IndexByte(buf, '\n') < 0 {
				break
			}
		}
		l, err := br.ReadString('\n')
		if l != "" {
			lines = append(lines, strings.TrimRight(l, "\r\n"))
		}
		if err != nil {
			return lines, err
		}
	}
	return lines, nil
}

func (v *logsView) update(a *App, msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case logStartMsg:
		if msg.err != nil {
			v.err, v.ended = msg.err, true
			return a.flashErr(msg.err), true
		}
		v.rc = msg.rc
		v.br = bufio.NewReader(msg.rc)
		return v.read(), true
	case logLinesMsg:
		v.lines = append(v.lines, msg.lines...)
		if n := len(v.lines) - maxLogLines; n > 0 {
			v.lines = v.lines[n:]
		}
		v.vp.SetContentLines(v.lines)
		if v.follow {
			v.vp.GotoBottom()
		}
		switch {
		case msg.err == nil:
			return v.read(), true
		case errors.Is(msg.err, io.EOF), errors.Is(msg.err, context.Canceled):
			v.ended = true
			return nil, true
		default:
			v.ended, v.err = true, msg.err
			return a.flashErr(msg.err), true
		}
	case tea.KeyPressMsg:
		switch {
		case matches(msg, keyFollow):
			v.follow = !v.follow
			if v.follow {
				v.vp.GotoBottom()
			}
			return a.flashInfo(map[bool]string{true: "Follow on", false: "Follow off"}[v.follow]), true
		case matches(msg, keyTop):
			v.follow = false
			v.vp.GotoTop()
			return nil, true
		case matches(msg, keyBottom):
			v.vp.GotoBottom()
			return nil, true
		case matches(msg, keyUp), matches(msg, keyPageUp):
			v.follow = false
			var cmd tea.Cmd
			v.vp, cmd = v.vp.Update(msg)
			return cmd, true
		case matches(msg, keyDown), matches(msg, keyPageDown):
			var cmd tea.Cmd
			v.vp, cmd = v.vp.Update(msg)
			return cmd, true
		}
	}
	return nil, false
}

func (v *logsView) render(a *App) string {
	if len(v.lines) == 0 {
		switch {
		case v.err != nil:
			return a.theme.FlashError.Render(errText(v.err))
		case v.ended:
			return a.theme.Dim.Render("No logs.")
		}
		return a.theme.Dim.Render("Waiting for logs…")
	}
	return v.vp.View()
}
