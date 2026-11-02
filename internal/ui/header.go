package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

var logo = []string{
	"  ___   ",
	" / _ \\  ",
	"( 8top ) ",
}

const (
	minHeaderRows = 3
	maxHeaderRows = 6
)

// headerHints are the key hints of the active view plus help.
func (a *App) headerHints() []binding {
	hints := append([]binding(nil), a.top().keys()...)
	return append(hints, keyHelp)
}

type headerLayout struct {
	rows     int
	info     bool
	infoW    int
	logo     bool
	columns  [][]binding
	colWidth []int
}

func hintWidth(b binding) int {
	return ansi.StringWidth("<"+b.Help().Key+"> ") + ansi.StringWidth(b.Help().Desc)
}

// infoLines are the left column of the header.
func (a *App) infoLines() [][2]string {
	v := func(s string) string {
		if s == "" {
			return "n/a"
		}
		return s
	}
	return [][2]string{
		{"Context:", v(a.cfg.Context)},
		{"Endpoint:", v(a.cfg.Endpoint)},
		{"8top Rev:", v(a.cfg.Version)},
	}
}

func layoutHeader(width int, info [][2]string, hints []binding) headerLayout {
	l := headerLayout{rows: minHeaderRows}
	for _, kv := range info {
		l.infoW = max(l.infoW, ansi.StringWidth(kv[0]+" "+kv[1]))
	}
	l.info = width >= 40
	l.logo = width >= 80
	avail := width
	if l.info {
		avail -= l.infoW + 3
	}
	if l.logo {
		avail -= ansi.StringWidth(logo[2])
	}
	const gap = 3
	split := func(rows int) ([][]binding, []int, int) {
		var cols [][]binding
		var widths []int
		total := 0
		for i := 0; i < len(hints); i += rows {
			col := hints[i:min(i+rows, len(hints))]
			w := 0
			for _, b := range col {
				w = max(w, hintWidth(b))
			}
			cols = append(cols, col)
			widths = append(widths, w+gap)
			total += w + gap
		}
		return cols, widths, total
	}
	for rows := minHeaderRows; rows <= maxHeaderRows; rows++ {
		cols, widths, total := split(rows)
		l.rows, l.columns, l.colWidth = rows, cols, widths
		if total <= avail {
			break
		}
	}
	// Drop the columns that still don't fit.
	total := 0
	for i, w := range l.colWidth {
		if total+w > avail+gap {
			l.columns, l.colWidth = l.columns[:i], l.colWidth[:i]
			break
		}
		total += w
	}
	return l
}

func (a *App) renderHeader() string {
	t := a.theme
	info := a.infoLines()
	hints := a.headerHints()
	l := layoutHeader(a.width, info, hints)
	lines := make([]string, l.rows)
	for r := range lines {
		var b strings.Builder
		if l.info {
			s := ""
			if r < len(info) {
				s = t.InfoLabel.Render(info[r][0]) + " " + t.InfoValue.Render(info[r][1])
			}
			b.WriteString(" " + fit(s, l.infoW) + "  ")
		}
		used := 0
		for c, col := range l.columns {
			s := ""
			if r < len(col) {
				h := col[r].Help()
				ks := t.HintKey
				if col[r].Help().Key == keyHelp.Help().Key {
					ks = t.HintNumKey
				}
				s = ks.Render("<"+h.Key+">") + " " + t.HintDesc.Render(h.Desc)
			}
			b.WriteString(fit(s, l.colWidth[c]))
			used += l.colWidth[c]
		}
		line := b.String()
		if l.logo {
			lw := ansi.StringWidth(logo[2])
			pad := a.width - lw - ansi.StringWidth(line)
			lg := ""
			if r < len(logo) {
				lg = t.Logo.Render(fit(logo[r], lw))
			}
			if pad > 0 {
				line += strings.Repeat(" ", pad)
			}
			line += lg
		}
		lines[r] = line
	}
	return strings.Join(lines, "\n")
}
