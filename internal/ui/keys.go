package ui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"go.ate.dev/8top/internal/model"
)

// binding is a key binding plus whether it changes the cluster. Mutating
// bindings are hidden and disabled in --readonly mode.
type binding struct {
	key.Binding
	mutating bool
}

func newBinding(help, desc string, keys ...string) binding {
	return binding{Binding: key.NewBinding(key.WithKeys(keys...), key.WithHelp(help, desc))}
}

func mutating(b binding) binding {
	b.mutating = true
	return b
}

// Global keys, available in every view. The help keys (first argument) use
// the README spelling ("ctrl-r") so the docs-sync test can compare them.
var (
	keyCommand = newBinding(":", "Command", ":")
	keyFilter  = newBinding("/", "Filter", "/")
	keyBack    = newBinding("esc", "Back", "esc")
	keyEnter   = newBinding("enter", "Drill down", "enter")
	keyHelp    = newBinding("?", "Help", "?")
	keyAll     = newBinding("0", "All atespaces", "0")
	keyFav     = newBinding("1–9", "Favorite atespace", "1", "2", "3", "4", "5", "6", "7", "8", "9")
	keySort    = newBinding("shift-<col>", "Sort by column", sortKeys()...)
	keyRefresh = newBinding("ctrl-r", "Refresh", "ctrl+r")
	keyQuit    = newBinding("ctrl-c", "Quit", "ctrl+c")
)

// Resource actions. Which of them apply depends on the kind (actionsFor).
var (
	keyYAML        = newBinding("y", "YAML", "y")
	keyDescribe    = newBinding("d", "Describe", "d")
	keyEdit        = mutating(newBinding("E", "Edit", "E"))
	keyDelete      = mutating(newBinding("ctrl-d", "Delete", "ctrl+d"))
	keyResume      = mutating(newBinding("a", "Resume", "a"))
	keySuspend     = mutating(newBinding("s", "Suspend", "s"))
	keyRevert      = mutating(newBinding("r", "Revert", "r"))
	keyEgress      = newBinding("e", "Egress", "e")
	keyLogs        = newBinding("l", "Logs", "l")
	keyPortForward = newBinding("p", "Port-Forward", "p")
	keyTag         = mutating(newBinding("t", "Tag", "t"))
	keyDrain       = mutating(newBinding("n", "Drain", "n"))
	keyAvailable   = mutating(newBinding("c", "Available", "c"))
	keyUnavailable = mutating(newBinding("u", "Unavailable", "u"))
)

// Logs view.
var keyFollow = newBinding("f", "Toggle follow", "f")

// Navigation keys of the table and viewports. These are the usual widget
// keys and are not part of the README keybinding tables.
var (
	keyUp       = newBinding("↑/k", "Up", "up", "k")
	keyDown     = newBinding("↓/j", "Down", "down", "j")
	keyPageUp   = newBinding("pgup", "Page up", "pgup", "ctrl+b")
	keyPageDown = newBinding("pgdn", "Page down", "pgdown", "ctrl+f", "space")
	keyTop      = newBinding("home", "Top", "home", "g")
	keyBottom   = newBinding("end", "Bottom", "end")
)

func navigationKeys() []binding {
	return []binding{keyUp, keyDown, keyPageUp, keyPageDown, keyTop, keyBottom}
}

func sortKeys() []string {
	var ks []string
	for c := 'A'; c <= 'Z'; c++ {
		ks = append(ks, string(c))
	}
	return ks
}

// actionsFor returns the resource actions that apply to kind, in the order
// they are shown in the header.
func actionsFor(kind model.Kind) []binding {
	switch kind {
	case model.KindAtespace:
		return []binding{keyYAML, keyDescribe, keyDelete}
	case model.KindActor:
		return []binding{keyResume, keySuspend, keyRevert, keyEgress, keyLogs, keyPortForward, keyTag,
			keyYAML, keyDescribe, keyEdit, keyDelete}
	case model.KindActorTemplate:
		// E shows the YAML read-only with a hint; templates are immutable.
		return []binding{keyYAML, keyDescribe, keyEdit, keyDelete}
	case model.KindTag, model.KindEgressPolicy:
		return []binding{keyYAML, keyDescribe, keyEdit, keyDelete}
	case model.KindAccessPolicy:
		return []binding{keyYAML, keyDescribe, keyEdit}
	case model.KindWorker:
		return []binding{keyDrain, keyAvailable, keyUnavailable, keyYAML, keyDescribe}
	case model.KindActorAssignment, model.KindWorkerPool:
		return []binding{keyYAML, keyDescribe}
	}
	return []binding{keyYAML, keyDescribe}
}

// visible drops mutating bindings in read-only mode.
func visible(bs []binding, readOnly bool) []binding {
	var out []binding
	for _, b := range bs {
		if readOnly && b.mutating {
			continue
		}
		out = append(out, b)
	}
	return out
}

func has(bs []binding, b binding) bool {
	for _, x := range bs {
		if x.Help().Key == b.Help().Key {
			return true
		}
	}
	return false
}

// keyString normalizes a key press: shift+letter becomes the upper-case
// letter even when the terminal reports it without text.
func keyString(msg tea.KeyPressMsg) string {
	s := msg.String()
	if strings.HasPrefix(s, "shift+") && len(s) == len("shift+")+1 {
		c := s[len(s)-1]
		if c >= 'a' && c <= 'z' {
			return strings.ToUpper(string(c))
		}
	}
	return s
}

func matches(msg tea.KeyPressMsg, b binding) bool {
	k := keyString(msg)
	for _, x := range b.Keys() {
		if x == k {
			return true
		}
	}
	return false
}

func keyBindings(bs []binding) []key.Binding {
	out := make([]key.Binding, len(bs))
	for i, b := range bs {
		out[i] = b.Binding
	}
	return out
}
