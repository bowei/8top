package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// fit truncates or pads s to exactly w cells.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if ansi.StringWidth(s) > w {
		s = ansi.Truncate(s, w, "…")
	}
	if pad := w - ansi.StringWidth(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// fitLines splits s into exactly h lines of exactly w cells.
func fitLines(s string, w, h int) []string {
	var lines []string
	if s != "" {
		lines = strings.Split(s, "\n")
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	out := make([]string, h)
	for i := range out {
		l := ""
		if i < len(lines) {
			l = lines[i]
		}
		out[i] = fit(l, w)
	}
	return out
}

// frame draws content in a box of w×h cells with title centered in the top
// border, the way k9s draws its resource views.
func frame(border lipgloss.Style, title, content string, w, h int) string {
	if w < 2 || h < 2 {
		return ""
	}
	inner := w - 2
	var top string
	if title != "" && inner >= 4 {
		title = " " + title + " "
		if ansi.StringWidth(title) > inner-2 {
			title = ansi.Truncate(title, inner-2, "…")
		}
		tw := ansi.StringWidth(title)
		left := (inner - tw) / 2
		right := inner - tw - left
		top = border.Render("┌"+strings.Repeat("─", left)) + title + border.Render(strings.Repeat("─", right)+"┐")
	} else {
		top = border.Render("┌" + strings.Repeat("─", inner) + "┐")
	}
	var b strings.Builder
	b.WriteString(top)
	side := border.Render("│")
	for _, l := range fitLines(content, inner, h-2) {
		b.WriteString("\n")
		b.WriteString(side + l + side)
	}
	b.WriteString("\n")
	b.WriteString(border.Render("└" + strings.Repeat("─", inner) + "┘"))
	return b.String()
}

// overlay draws box centered on top of base (both w cells wide).
func overlay(base, box string, w int) string {
	baseLines := strings.Split(base, "\n")
	boxLines := strings.Split(box, "\n")
	bw := 0
	for _, l := range boxLines {
		bw = max(bw, ansi.StringWidth(l))
	}
	if bw > w {
		bw = w
	}
	y := (len(baseLines) - len(boxLines)) / 2
	x := (w - bw) / 2
	for i, l := range boxLines {
		row := y + i
		if row < 0 || row >= len(baseLines) {
			continue
		}
		bl := fit(baseLines[row], w)
		left := ansi.Cut(bl, 0, x)
		right := ansi.Cut(bl, x+bw, w)
		baseLines[row] = left + "\x1b[m" + fit(l, bw) + "\x1b[m" + right
	}
	return strings.Join(baseLines, "\n")
}
