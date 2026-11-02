// Package style holds 8top's Lip Gloss theme: the colors and styles used by
// the header, the resource table, breadcrumbs, the flash line, and dialogs.
//
// Lip Gloss v2 styles always render full-fidelity ANSI; Bubble Tea downsamples
// to the terminal's color profile when it writes the frame. Tests that need
// deterministic output render with a Theme and pass the result through
// Downsample with a fixed profile (NoTTY for plain text, ANSI for 16 colors).
package style

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"

	"go.ate.dev/8top/internal/render"
)

// Palette colors. They are basic or 256-color ANSI indexes so that
// downsampling to the 16-color profile is predictable.
var (
	colorOrange  = lipgloss.Color("214")
	colorBlue    = lipgloss.Color("33")
	colorAqua    = lipgloss.Color("51")
	colorWhite   = lipgloss.Color("15")
	colorGray    = lipgloss.Color("245")
	colorBlack   = lipgloss.Color("0")
	colorFuchsia = lipgloss.Color("201")
	colorRed     = lipgloss.Color("196")
	colorGreen   = lipgloss.Color("40")
	colorYellow  = lipgloss.Color("226")
	colorMagenta = lipgloss.Color("5")
	colorChanged = lipgloss.Color("123")
)

// ForStatus returns the foreground color for a row status, following the
// k9s pod phase colors. StatusNone returns nil (the default foreground).
func ForStatus(s render.Status) color.Color {
	switch s {
	case render.StatusOK:
		return colorGreen
	case render.StatusPending:
		return colorYellow
	case render.StatusIdle:
		return colorGray
	case render.StatusError:
		return colorRed
	case render.StatusDeleting:
		return colorMagenta
	}
	return nil
}

// Theme is the set of styles used by the UI.
type Theme struct {
	// Header.
	Logo        lipgloss.Style
	InfoLabel   lipgloss.Style
	InfoValue   lipgloss.Style
	HintKey     lipgloss.Style
	HintDesc    lipgloss.Style
	HintNumKey  lipgloss.Style
	HintNumDesc lipgloss.Style

	// Body frame.
	Border      lipgloss.Style // foreground of the border runes
	Title       lipgloss.Style // resource name in the title
	TitleScope  lipgloss.Style // (scope) in the title
	TitleCount  lipgloss.Style // [N] in the title
	TitleFilter lipgloss.Style // </filter> in the title

	// Table.
	TableHeader lipgloss.Style
	Cell        lipgloss.Style
	Selected    lipgloss.Style
	Changed     lipgloss.Style

	// Text views.
	Text lipgloss.Style
	Dim  lipgloss.Style

	// Breadcrumbs.
	Crumb       lipgloss.Style
	CrumbActive lipgloss.Style

	// Flash line.
	FlashInfo  lipgloss.Style
	FlashWarn  lipgloss.Style
	FlashError lipgloss.Style

	// Prompt and dialogs.
	Prompt       lipgloss.Style
	PromptBorder lipgloss.Style
	Dialog       lipgloss.Style
	DialogTitle  lipgloss.Style
	Button       lipgloss.Style
	ButtonActive lipgloss.Style

	// colored reports whether status colors are applied.
	colored bool
}

// Default returns the k9s-like color theme.
func Default() Theme {
	s := lipgloss.NewStyle
	return Theme{
		Logo:        s().Foreground(colorOrange).Bold(true),
		InfoLabel:   s().Foreground(colorOrange),
		InfoValue:   s().Foreground(colorWhite).Bold(true),
		HintKey:     s().Foreground(colorBlue).Bold(true),
		HintDesc:    s().Foreground(colorGray),
		HintNumKey:  s().Foreground(colorFuchsia).Bold(true),
		HintNumDesc: s().Foreground(colorGray),

		Border:      s().Foreground(colorBlue),
		Title:       s().Foreground(colorAqua).Bold(true),
		TitleScope:  s().Foreground(colorFuchsia).Bold(true),
		TitleCount:  s().Foreground(colorWhite).Bold(true),
		TitleFilter: s().Foreground(colorOrange).Bold(true),

		TableHeader: s().Foreground(colorWhite).Bold(true),
		Cell:        s(),
		Selected:    s().Foreground(colorBlack).Background(colorAqua),
		Changed:     s().Foreground(colorChanged).Bold(true),

		Text: s(),
		Dim:  s().Foreground(colorGray),

		Crumb:       s().Foreground(colorBlack).Background(colorAqua),
		CrumbActive: s().Foreground(colorBlack).Background(colorOrange),

		FlashInfo:  s().Foreground(colorAqua),
		FlashWarn:  s().Foreground(colorOrange),
		FlashError: s().Foreground(colorRed).Bold(true),

		Prompt:       s().Foreground(colorAqua),
		PromptBorder: s().Foreground(colorAqua),
		Dialog:       s().Foreground(colorWhite),
		DialogTitle:  s().Foreground(colorOrange).Bold(true),
		Button:       s().Foreground(colorWhite),
		ButtonActive: s().Foreground(colorBlack).Background(colorOrange).Bold(true),

		colored: true,
	}
}

// Plain returns a theme without colors. Selection and the active button use
// reverse video so they stay visible.
func Plain() Theme {
	s := lipgloss.NewStyle
	return Theme{
		Logo: s(), InfoLabel: s(), InfoValue: s(), HintKey: s(), HintDesc: s(),
		HintNumKey: s(), HintNumDesc: s(),
		Border: s(), Title: s(), TitleScope: s(), TitleCount: s(), TitleFilter: s(),
		TableHeader: s().Bold(true), Cell: s(), Selected: s().Reverse(true), Changed: s().Bold(true),
		Text: s(), Dim: s(),
		Crumb: s(), CrumbActive: s().Reverse(true),
		FlashInfo: s(), FlashWarn: s(), FlashError: s().Bold(true),
		Prompt: s(), PromptBorder: s(), Dialog: s(), DialogTitle: s().Bold(true),
		Button: s(), ButtonActive: s().Reverse(true),
	}
}

// Status returns the style for a row with status s.
func (t Theme) Status(s render.Status) lipgloss.Style {
	st := lipgloss.NewStyle()
	if !t.colored {
		return st
	}
	if c := ForStatus(s); c != nil {
		st = st.Foreground(c)
	}
	return st
}

// Downsample converts styled output to the given color profile.
// colorprofile.NoTTY strips all escape sequences, colorprofile.ANSI keeps
// 16-color styling, and colorprofile.TrueColor returns s unchanged.
func Downsample(s string, p colorprofile.Profile) string {
	var b strings.Builder
	w := &colorprofile.Writer{Forward: &b, Profile: p}
	_, _ = w.WriteString(s)
	return b.String()
}
