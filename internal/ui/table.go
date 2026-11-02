package ui

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"go.ate.dev/8top/internal/model"
	"go.ate.dev/8top/internal/render"
	"go.ate.dev/8top/internal/style"
)

// listMsg is the result of a List call for a table view.
type listMsg struct {
	view int
	gen  int
	objs []model.Object
	err  error
}

// tickMsg asks a table view to poll.
type tickMsg struct {
	view int
	gen  int
}

// tableView lists the objects of one kind in one scope.
type tableView struct {
	vid   int
	kind  model.Kind
	scope model.Scope
	r     render.Renderer
	ro    bool

	loaded bool
	objs   map[string]model.Object
	// rows are all rendered rows in API order.
	rows []render.Row
	// changed holds the keys of rows that changed in the last refresh.
	changed map[string]bool

	filter   string
	filterRe *regexp.Regexp
	invert   bool

	sortCol  int // -1: API order
	sortDesc bool

	// visible are the filtered, sorted rows.
	visible []render.Row
	cursor  int
	offset  int
	selKey  string

	w, h int
}

func (a *App) newTable(kind model.Kind, scope model.Scope) *tableView {
	return &tableView{
		vid:     a.newID(),
		kind:    kind,
		scope:   scope,
		r:       a.cfg.Renderer(kind),
		ro:      a.cfg.ReadOnly,
		objs:    map[string]model.Object{},
		sortCol: -1,
	}
}

func (tv *tableView) id() int { return tv.vid }

func (tv *tableView) crumbs() []string {
	c := []string{string(tv.kind)}
	if s := tv.scopeLabel(); s != "" {
		c = append(c, s)
	}
	return c
}

// scopeLabel is the scope shown in the title and breadcrumbs.
func (tv *tableView) scopeLabel() string {
	s := tv.scope
	switch tv.kind {
	case model.KindAtespace, model.KindWorker:
		return ""
	case model.KindActorAssignment:
		return s.Worker
	case model.KindWorkerPool:
		if s.Namespace == "" {
			return "all"
		}
		return s.Namespace
	case model.KindAccessPolicy:
		if s.Atespace == "" {
			return "global"
		}
		return s.Atespace
	case model.KindEgressPolicy:
		if s.Actor != "" {
			return s.Atespace + "/" + s.Actor
		}
	}
	if s.Atespace == "" {
		return "all"
	}
	return s.Atespace
}

func (tv *tableView) title(t style.Theme) string {
	s := t.Title.Render(infoFor(tv.kind).title)
	if sc := tv.scopeLabel(); sc != "" {
		s += t.Title.Render("(") + t.TitleScope.Render(sc) + t.Title.Render(")")
	}
	s += t.Title.Render("[") + t.TitleCount.Render(strconv.Itoa(len(tv.visible))) + t.Title.Render("]")
	if tv.filter != "" {
		s += " " + t.TitleFilter.Render("</"+tv.filter+">")
	}
	return s
}

func (tv *tableView) keys() []binding {
	return visible(actionsFor(tv.kind), tv.ro)
}

func (tv *tableView) setSize(w, h int) {
	tv.w, tv.h = w, h
	tv.clampScroll()
}

// pageSize is the number of rows that fit under the column header.
func (tv *tableView) pageSize() int { return max(tv.h-1, 1) }

func (tv *tableView) activate(a *App) tea.Cmd { return tv.reload(a) }

func (tv *tableView) deactivate() {}

// reload fetches now. The result schedules the next poll if the view is
// still active and no newer refresh superseded it.
func (tv *tableView) reload(a *App) tea.Cmd {
	return tv.fetch(a, a.pollGen)
}

func (tv *tableView) fetch(a *App, gen int) tea.Cmd {
	api, kind, scope, id := a.cfg.API, tv.kind, tv.scope, tv.vid
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		objs, err := api.List(ctx, kind, scope)
		return listMsg{view: id, gen: gen, objs: objs, err: err}
	}
}

func (tv *tableView) update(a *App, msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case listMsg:
		var cmd tea.Cmd
		if msg.err != nil {
			cmd = a.flashErr(msg.err)
		} else {
			tv.apply(msg.objs, a.cfg.Now())
		}
		if msg.gen == a.pollGen && a.top() == view(tv) {
			id, gen := tv.vid, msg.gen
			cmd = tea.Batch(cmd, a.cfg.Tick(a.cfg.Refresh, func(time.Time) tea.Msg { return tickMsg{view: id, gen: gen} }))
		}
		return cmd, true
	case tickMsg:
		if msg.gen != a.pollGen || a.top() != view(tv) {
			return nil, true
		}
		return tv.fetch(a, msg.gen), true
	case tea.KeyPressMsg:
		return tv.handleKey(a, msg)
	}
	return nil, false
}

func (tv *tableView) handleKey(a *App, msg tea.KeyPressMsg) (tea.Cmd, bool) {
	switch {
	case matches(msg, keyUp):
		tv.move(-1)
	case matches(msg, keyDown):
		tv.move(1)
	case matches(msg, keyPageUp):
		tv.move(-tv.pageSize())
	case matches(msg, keyPageDown):
		tv.move(tv.pageSize())
	case matches(msg, keyTop):
		tv.move(-len(tv.visible))
	case matches(msg, keyBottom):
		tv.move(len(tv.visible))
	case matches(msg, keyFilter):
		return a.openPrompt(promptFilter, tv.filter), true
	case matches(msg, keyBack):
		if tv.filter != "" {
			tv.setFilter("")
			return nil, true
		}
		return nil, false
	case matches(msg, keyEnter):
		if o, ok := tv.selected(); ok {
			return a.drill(o), true
		}
	case matches(msg, keyAll):
		return tv.switchAtespace(a, ""), true
	case matches(msg, keyFav):
		n := int(keyString(msg)[0] - '1')
		if n >= len(a.favorites) {
			return a.flashWarn(fmt.Sprintf("No favorite atespace %d", n+1)), true
		}
		return tv.switchAtespace(a, a.favorites[n]), true
	default:
		// Resource actions win over sorting (E is Edit, not shift-e).
		for _, b := range actionsFor(tv.kind) {
			if !matches(msg, b) {
				continue
			}
			if tv.ro && b.mutating {
				return nil, false // the App reports read-only mode
			}
			o, ok := tv.selected()
			if !ok {
				return nil, true
			}
			return a.doAction(tv, b, o), true
		}
		if matches(msg, keySort) {
			tv.sortBy(keyString(msg)[0])
			return nil, true
		}
		return nil, false
	}
	return nil, true
}

// switchAtespace re-opens this view in another atespace (`0`, `1`–`9`).
func (tv *tableView) switchAtespace(a *App, ns string) tea.Cmd {
	a.setAtespace(ns)
	if !infoFor(tv.kind).atespaced || tv.scope.Actor != "" {
		return nil
	}
	if tv.scope.Atespace == ns {
		return nil
	}
	nv := a.newTable(tv.kind, model.Scope{Atespace: ns})
	nv.sortCol, nv.sortDesc = tv.sortCol, tv.sortDesc
	nv.setFilter(tv.filter)
	return a.replaceTop(nv)
}

func (tv *tableView) selected() (model.Object, bool) {
	if tv.cursor < 0 || tv.cursor >= len(tv.visible) {
		return model.Object{}, false
	}
	o, ok := tv.objs[tv.visible[tv.cursor].Key]
	return o, ok
}

// apply replaces the rows with a fresh List result, marking changed rows.
func (tv *tableView) apply(objs []model.Object, now time.Time) {
	old := map[string]render.Row{}
	for _, r := range tv.rows {
		old[r.Key] = r
	}
	tv.objs = make(map[string]model.Object, len(objs))
	tv.rows = make([]render.Row, 0, len(objs))
	tv.changed = map[string]bool{}
	for _, o := range objs {
		r := tv.r.Render(o, now)
		if r.Key == "" {
			r.Key = o.Key
		}
		tv.objs[r.Key] = o
		tv.rows = append(tv.rows, r)
		if !tv.loaded {
			continue
		}
		if p, ok := old[r.Key]; !ok || !sameCells(p, r) {
			tv.changed[r.Key] = true
		}
	}
	tv.loaded = true
	tv.recompute()
}

// sameCells compares the cells that are not ages, so rows don't flash just
// because time passed.
func sameCells(a, b render.Row) bool {
	if a.Status != b.Status || len(a.Cells) != len(b.Cells) {
		return false
	}
	for i := range a.Cells {
		if a.Cells[i] != b.Cells[i] && !isAgeCell(a.Cells[i]) {
			return false
		}
	}
	return true
}

var ageRe = regexp.MustCompile(`^(\d+[smhdy])+$`)

func isAgeCell(s string) bool { return ageRe.MatchString(s) }

func (tv *tableView) setFilter(f string) {
	tv.filter = f
	tv.invert = strings.HasPrefix(f, "!")
	pat := strings.TrimPrefix(f, "!")
	tv.filterRe = nil
	if pat != "" {
		re, err := regexp.Compile("(?i)" + pat)
		if err != nil {
			re = regexp.MustCompile("(?i)" + regexp.QuoteMeta(pat))
		}
		tv.filterRe = re
	}
	tv.recompute()
}

// sortBy handles shift-<letter>: it sorts by a column starting with the
// letter. Pressing it again on the same column reverses the order; if
// several columns start with the letter, the next press moves to the next
// one. Candidates are tried from the right, so shift-a picks AGE before
// ATESPACE.
func (tv *tableView) sortBy(letter byte) {
	var cands []int
	cols := tv.r.Columns()
	for i := len(cols) - 1; i >= 0; i-- {
		if n := cols[i].Name; n != "" && n[0] == letter {
			cands = append(cands, i)
		}
	}
	if len(cands) == 0 {
		return
	}
	i := slices.Index(cands, tv.sortCol)
	switch {
	case i < 0:
		tv.sortCol, tv.sortDesc = cands[0], false
	case !tv.sortDesc:
		tv.sortDesc = true
	default:
		tv.sortCol, tv.sortDesc = cands[(i+1)%len(cands)], false
	}
	tv.recompute()
}

// recompute filters and sorts the rows, keeping the selection on the same
// key and the selected row at the same screen position.
func (tv *tableView) recompute() {
	screenPos := tv.cursor - tv.offset
	var vis []render.Row
	for _, r := range tv.rows {
		if tv.filterRe != nil && tv.filterRe.MatchString(strings.Join(r.Cells, " ")) == tv.invert {
			continue
		}
		vis = append(vis, r)
	}
	if tv.sortCol >= 0 {
		c := tv.sortCol
		slices.SortStableFunc(vis, func(x, y render.Row) int {
			n := compareSortKeys(sortKey(x, c), sortKey(y, c))
			if tv.sortDesc {
				n = -n
			}
			return n
		})
	}
	tv.visible = vis
	idx := -1
	if tv.selKey != "" {
		idx = slices.IndexFunc(vis, func(r render.Row) bool { return r.Key == tv.selKey })
	}
	if idx < 0 {
		idx = min(tv.cursor, len(vis)-1)
	}
	tv.cursor = max(idx, 0)
	tv.offset = tv.cursor - screenPos
	tv.clampScroll()
	tv.updateSel()
}

func sortKey(r render.Row, c int) string {
	if c < len(r.SortKeys) && r.SortKeys[c] != "" {
		return r.SortKeys[c]
	}
	if c < len(r.Cells) {
		return r.Cells[c]
	}
	return ""
}

// compareSortKeys orders integers numerically and everything else as text.
func compareSortKeys(a, b string) int {
	x, errA := strconv.ParseFloat(a, 64)
	y, errB := strconv.ParseFloat(b, 64)
	if errA == nil && errB == nil {
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
		return 0
	}
	return strings.Compare(a, b)
}

func (tv *tableView) move(n int) {
	tv.cursor = max(min(tv.cursor+n, len(tv.visible)-1), 0)
	tv.clampScroll()
	tv.updateSel()
}

func (tv *tableView) updateSel() {
	if tv.cursor < len(tv.visible) {
		tv.selKey = tv.visible[tv.cursor].Key
	}
}

// clampScroll keeps the cursor on screen and the page full.
func (tv *tableView) clampScroll() {
	ps := tv.pageSize()
	if tv.cursor < tv.offset {
		tv.offset = tv.cursor
	}
	if tv.cursor >= tv.offset+ps {
		tv.offset = tv.cursor - ps + 1
	}
	tv.offset = max(min(tv.offset, len(tv.visible)-ps), 0)
}

// columnWidths sizes the columns to their content and fits them into w.
func (tv *tableView) columnWidths(cols []render.Column, w int) []int {
	widths := make([]int, len(cols))
	for i, c := range cols {
		widths[i] = ansi.StringWidth(c.Name)
		for _, r := range tv.visible {
			if i < len(r.Cells) {
				widths[i] = max(widths[i], ansi.StringWidth(r.Cells[i]))
			}
		}
		if c.Width > 0 {
			widths[i] = min(widths[i], max(c.Width, ansi.StringWidth(c.Name)))
		}
	}
	// Each column has one cell of padding on the right.
	total := func() int {
		t := 0
		for _, x := range widths {
			t += x + 1
		}
		return t
	}
	// Shrink the widest column until everything fits.
	for total() > w {
		wi := 0
		for i := range widths {
			if widths[i] > widths[wi] {
				wi = i
			}
		}
		if widths[wi] <= 1 {
			break
		}
		widths[wi]--
	}
	// Give the slack to the last column.
	if len(widths) > 0 && total() < w {
		widths[len(widths)-1] += w - total()
	}
	return widths
}

func (tv *tableView) render(a *App) string {
	t := a.theme
	cols := tv.r.Columns()
	// One cell of margin on the left, as in k9s.
	w := max(tv.w-1, 1)
	widths := tv.columnWidths(cols, w)
	tcols := make([]table.Column, len(cols))
	for i, c := range cols {
		tcols[i] = table.Column{Title: c.Name, Width: widths[i]}
	}
	ps := tv.pageSize()
	end := min(tv.offset+ps, len(tv.visible))
	var rows []table.Row
	for i := tv.offset; i < end; i++ {
		r := tv.visible[i]
		st := t.Status(r.Status)
		if tv.changed[r.Key] {
			st = t.Changed
		}
		cells := make(table.Row, len(cols))
		for j := range cols {
			v := ""
			if j < len(r.Cells) {
				v = r.Cells[j]
			}
			if i != tv.cursor {
				// The selected row is left plain so the selection
				// style applies to the whole row.
				v = st.Render(v)
			}
			cells[j] = v
		}
		rows = append(rows, cells)
	}
	m := table.New(
		table.WithColumns(tcols),
		table.WithRows(rows),
		table.WithHeight(ps+1),
		table.WithWidth(w),
		table.WithStyles(table.Styles{
			Header:   t.TableHeader.PaddingRight(1),
			Cell:     lipgloss.NewStyle().PaddingRight(1),
			Selected: t.Selected,
		}),
	)
	if len(rows) > 0 {
		m.SetCursor(tv.cursor - tv.offset)
	}
	out := m.View()
	if !tv.loaded {
		head, _, _ := strings.Cut(out, "\n")
		out = head + "\n" + t.Dim.Render("Loading…")
	}
	return " " + strings.ReplaceAll(out, "\n", "\n ")
}

// visibleRowKeys returns the keys of the visible rows (for tests).
func (tv *tableView) visibleRowKeys() []string {
	out := make([]string, len(tv.visible))
	for i, r := range tv.visible {
		out[i] = r.Key
	}
	return out
}
