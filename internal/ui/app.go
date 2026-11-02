// Package ui is 8top's Bubble Tea front end: the root App model with its view
// stack, the header, the resource table, YAML / describe / logs / help views,
// the `:` and `/` prompts, confirmation dialogs, and the flash line.
package ui

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/grpc/status"

	"go.ate.dev/8top/internal/edit"
	"go.ate.dev/8top/internal/model"
	"go.ate.dev/8top/internal/render"
	"go.ate.dev/8top/internal/style"
)

// Config configures the UI. Only API is required; everything else has a
// default.
type Config struct {
	API model.API
	// Renderer returns the renderer for a kind. Default render.For.
	Renderer func(model.Kind) render.Renderer

	// Header values.
	Context, Endpoint, Version string

	// Atespace is the initial atespace; "" means all atespaces.
	Atespace string
	// Favorites are the atespaces bound to 1–9. Atespaces the user switches
	// to are appended (up to nine).
	Favorites []string
	// Command is the initial view, e.g. "workers" or "actors myspace".
	// Default "actors".
	Command string
	// Refresh is the polling interval. Default 2s.
	Refresh time.Duration
	// ReadOnly hides and disables every mutating action and the editor.
	ReadOnly bool

	// Now is the clock used for AGE columns. Default time.Now.
	Now func() time.Time
	// Tick schedules a time-based message. Default tea.Tick. Tests replace
	// it so they never sleep.
	Tick func(time.Duration, func(time.Time) tea.Msg) tea.Cmd
	// Theme is the color theme. Default style.Default().
	Theme *style.Theme

	// Edit hooks. Defaults are the edit package functions and
	// tea.ExecProcess.
	Marshal  func(model.Object) ([]byte, error)
	Editable func(model.Kind) (bool, string)
	// EditFile returns the contents of the file to edit (YAML with a
	// kubectl-edit style header). Default edit.EditFile.
	EditFile      func(model.Object) ([]byte, error)
	Apply         func(ctx context.Context, api model.API, orig model.Object, edited []byte) (model.Object, bool, error)
	Annotate      func([]byte, error) []byte
	EditorCommand func(path string) *exec.Cmd
	Exec          func(*exec.Cmd, tea.ExecCallback) tea.Cmd
	// TempDir is where edit files are written. Default os.TempDir().
	TempDir string
}

func (c *Config) setDefaults() {
	if c.Renderer == nil {
		c.Renderer = render.For
	}
	if c.Command == "" {
		c.Command = string(model.KindActor)
	}
	if c.Refresh <= 0 {
		c.Refresh = 2 * time.Second
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Tick == nil {
		c.Tick = tea.Tick
	}
	if c.Theme == nil {
		t := style.Default()
		c.Theme = &t
	}
	if c.Marshal == nil {
		c.Marshal = edit.Marshal
	}
	if c.Editable == nil {
		c.Editable = edit.Editable
	}
	if c.EditFile == nil {
		c.EditFile = edit.EditFile
	}
	if c.Apply == nil {
		c.Apply = edit.Apply
	}
	if c.Annotate == nil {
		c.Annotate = edit.Annotate
	}
	if c.EditorCommand == nil {
		c.EditorCommand = edit.EditorCommand
	}
	if c.Exec == nil {
		c.Exec = tea.ExecProcess
	}
}

// callTimeout bounds each API call made by an action or a refresh.
const callTimeout = 30 * time.Second

// flashTimeout is how long a flash message stays up.
const flashTimeout = 5 * time.Second

// view is one entry of the view stack.
type view interface {
	// id is unique per view instance; async results carry it.
	id() int
	// crumbs are the breadcrumbs this view adds.
	crumbs() []string
	// title is the (styled) title drawn in the body border.
	title(t style.Theme) string
	// keys are the bindings the view handles itself (shown in the header
	// and in help), already filtered for read-only mode.
	keys() []binding
	// setSize sets the inner body size.
	setSize(w, h int)
	// activate is called when the view becomes the top of the stack.
	activate(a *App) tea.Cmd
	// deactivate is called when the view is popped.
	deactivate()
	// update handles a key or a message addressed to the view. handled
	// reports whether a key was consumed.
	update(a *App, msg tea.Msg) (cmd tea.Cmd, handled bool)
	// render returns the body content of the inner size.
	render(a *App) string
}

// App is the root model.
type App struct {
	cfg   Config
	theme style.Theme

	width, height int

	stack   []view
	prompt  *prompt
	confirm *confirm
	flash   flash

	// atespace is the current atespace ("" = all).
	atespace  string
	favorites []string

	// pollGen invalidates pending ticks when the active view changes or a
	// refresh is forced.
	pollGen int
	nextID  int

	// inflight counts running actions, shown with a spinner.
	inflight     int
	spinnerFrame int
	spinnerOn    bool

	edit *editSession

	initErr error
}

// New returns the root model.
func New(cfg Config) *App {
	cfg.setDefaults()
	a := &App{
		cfg:       cfg,
		theme:     *cfg.Theme,
		atespace:  cfg.Atespace,
		favorites: slices.Clone(cfg.Favorites),
	}
	if a.atespace != "" {
		a.addFavorite(a.atespace)
	}
	c, err := parseCommand(cfg.Command, a.atespace)
	if err != nil || c.quit {
		a.initErr = err
		c, _ = parseCommand(string(model.KindActor), a.atespace)
	}
	if c.setAtespace {
		a.setAtespace(c.scope.Atespace)
	}
	a.stack = []view{a.newTable(c.kind, c.scope)}
	return a
}

func (a *App) newID() int {
	a.nextID++
	return a.nextID
}

// Init starts the first view.
func (a *App) Init() tea.Cmd {
	var cmds []tea.Cmd
	if a.initErr != nil {
		cmds = append(cmds, a.flashErr(a.initErr))
	}
	cmds = append(cmds, a.top().activate(a))
	return tea.Batch(cmds...)
}

func (a *App) top() view { return a.stack[len(a.stack)-1] }

// push makes v the active view.
func (a *App) push(v view) tea.Cmd {
	a.stack = append(a.stack, v)
	a.layout()
	a.pollGen++
	return v.activate(a)
}

// pop returns to the previous view.
func (a *App) pop() tea.Cmd {
	if len(a.stack) <= 1 {
		return nil
	}
	a.top().deactivate()
	a.stack = a.stack[:len(a.stack)-1]
	a.layout()
	a.pollGen++
	return a.top().activate(a)
}

// reset replaces the whole stack with v (`:` commands).
func (a *App) reset(v view) tea.Cmd {
	for _, old := range a.stack {
		old.deactivate()
	}
	a.stack = nil
	return a.push(v)
}

// replaceTop swaps the active view (e.g. after changing its atespace).
func (a *App) replaceTop(v view) tea.Cmd {
	a.top().deactivate()
	a.stack = a.stack[:len(a.stack)-1]
	return a.push(v)
}

func (a *App) find(id int) view {
	for _, v := range a.stack {
		if v.id() == id {
			return v
		}
	}
	return nil
}

func (a *App) addFavorite(ns string) {
	if ns == "" || slices.Contains(a.favorites, ns) || len(a.favorites) >= 9 {
		return
	}
	a.favorites = append(a.favorites, ns)
}

func (a *App) setAtespace(ns string) {
	a.atespace = ns
	a.addFavorite(ns)
}

// Update implements tea.Model.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		a.layout()
		return a, nil
	case tea.KeyPressMsg:
		return a, a.handleKey(msg)
	case flashClearMsg:
		if msg.seq == a.flash.seq {
			a.flash = flash{seq: a.flash.seq}
		}
		return a, nil
	case spinnerTickMsg:
		if a.inflight == 0 {
			a.spinnerOn = false
			return a, nil
		}
		a.spinnerFrame++
		return a, a.cfg.Tick(spinnerInterval, func(time.Time) tea.Msg { return spinnerTickMsg{} })
	case actionDoneMsg:
		return a, a.actionDone(msg)
	case tickMsg, listMsg, objectMsg, logStartMsg, logLinesMsg:
		id := viewID(msg)
		if v := a.find(id); v != nil {
			cmd, _ := v.update(a, msg)
			return a, cmd
		}
		if m, ok := msg.(logStartMsg); ok && m.rc != nil {
			// The logs view was closed before the stream opened.
			m.rc.Close()
		}
		return a, nil
	case editStartMsg, editorExitMsg, editResultMsg:
		return a, a.updateEdit(msg)
	}
	return a, nil
}

func viewID(msg tea.Msg) int {
	switch m := msg.(type) {
	case tickMsg:
		return m.view
	case listMsg:
		return m.view
	case objectMsg:
		return m.view
	case logStartMsg:
		return m.view
	case logLinesMsg:
		return m.view
	}
	return 0
}

func (a *App) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	if matches(msg, keyQuit) {
		return tea.Quit
	}
	if a.confirm != nil {
		return a.updateConfirm(msg)
	}
	if a.prompt != nil {
		return a.updatePrompt(msg)
	}
	// The view gets the first chance, so that e.g. esc clears a filter
	// before popping the view.
	if cmd, handled := a.top().update(a, msg); handled {
		return cmd
	}
	switch {
	case matches(msg, keyCommand):
		return a.openPrompt(promptCommand, "")
	case matches(msg, keyHelp):
		if _, ok := a.top().(*helpView); ok {
			return nil
		}
		return a.push(a.newHelp(a.top()))
	case matches(msg, keyBack):
		return a.pop()
	case matches(msg, keyRefresh):
		return a.refresh()
	}
	if a.cfg.ReadOnly && isMutatingKey(msg) {
		return a.flashWarn("8top is in read-only mode")
	}
	return nil
}

func isMutatingKey(msg tea.KeyPressMsg) bool {
	for _, b := range []binding{keyEdit, keyDelete, keyResume, keySuspend, keyRevert, keyTag, keyDrain, keyAvailable, keyUnavailable} {
		if matches(msg, b) {
			return true
		}
	}
	return false
}

// refresh re-fetches the active view now and restarts its polling.
func (a *App) refresh() tea.Cmd {
	a.pollGen++
	if r, ok := a.top().(interface{ reload(*App) tea.Cmd }); ok {
		return r.reload(a)
	}
	return nil
}

// layout sizes the views for the current window.
func (a *App) layout() {
	w, h := a.bodyInner()
	for _, v := range a.stack {
		v.setSize(w, h)
	}
}

// Fixed rows around the body.
const (
	crumbRows = 1
	flashRows = 1
	promptRow = 3
)

func (a *App) headerRows() int {
	return layoutHeader(a.width, a.infoLines(), a.headerHints()).rows
}

func (a *App) bodyHeight() int {
	h := a.height - a.headerRows() - crumbRows - flashRows
	if a.prompt != nil {
		h -= promptRow
	}
	return max(h, 3)
}

func (a *App) bodyInner() (int, int) {
	return max(a.width-2, 1), max(a.bodyHeight()-2, 1)
}

// View implements tea.Model.
func (a *App) View() tea.View {
	v := tea.NewView(a.render())
	v.AltScreen = true
	v.WindowTitle = "8top"
	return v
}

func (a *App) render() string {
	if a.width <= 0 || a.height <= 0 {
		return ""
	}
	t := a.theme
	var parts []string
	parts = append(parts, a.renderHeader())
	if a.prompt != nil {
		parts = append(parts, a.renderPrompt())
	}
	body := frame(t.Border, a.top().title(t), a.top().render(a), a.width, a.bodyHeight())
	if a.confirm != nil {
		body = overlay(body, a.renderConfirm(), a.width)
	}
	parts = append(parts, body, a.renderCrumbs(), a.renderFlash())
	out := strings.Join(parts, "\n")
	return strings.Join(fitLines(out, a.width, a.height), "\n")
}

func (a *App) renderCrumbs() string {
	var crumbs []string
	for _, v := range a.stack {
		crumbs = append(crumbs, v.crumbs()...)
	}
	var b strings.Builder
	for i, c := range crumbs {
		st := a.theme.Crumb
		if i == len(crumbs)-1 {
			st = a.theme.CrumbActive
		}
		if i > 0 {
			b.WriteString(" ")
		}
		b.WriteString(st.Render("<" + c + ">"))
	}
	return b.String()
}

// errText formats an error for the flash line: gRPC errors as
// "Code: message", like the README's
// "FailedPrecondition: actor must be SUSPENDED to change its template".
func errText(err error) string {
	if s, ok := status.FromError(err); ok && s.Code() != 0 {
		return s.Code().String() + ": " + s.Message()
	}
	var se interface{ GRPCStatus() *status.Status }
	if errors.As(err, &se) {
		s := se.GRPCStatus()
		return s.Code().String() + ": " + s.Message()
	}
	return err.Error()
}

// objectName is "atespace/name", or "name" for global objects.
func objectName(o model.Object) string {
	if o.Atespace == "" {
		return o.Name
	}
	return o.Atespace + "/" + o.Name
}
