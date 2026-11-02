package ui

import (
	"bufio"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"go.ate.dev/8top/internal/model"
)

const readmePath = "../../README.md"

// readmeTables returns every markdown table in the README whose header row
// starts with the given column names, as rows keyed by column name.
func readmeTables(t *testing.T, header ...string) [][]map[string]string {
	t.Helper()
	b, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("reading README: %v", err)
	}
	var tables [][]map[string]string
	var cols []string
	var rows []map[string]string
	inTable := false
	flush := func() {
		if inTable && slices.Equal(cols[:min(len(header), len(cols))], header) {
			tables = append(tables, rows)
		}
		inTable, cols, rows = false, nil, nil
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			flush()
			continue
		}
		cells := splitRow(line)
		switch {
		case !inTable:
			inTable, cols = true, cells
		case strings.HasPrefix(strings.TrimSpace(cells[0]), ":--") || strings.HasPrefix(cells[0], "--"):
			// alignment row
		default:
			row := map[string]string{}
			for i, c := range cells {
				if i < len(cols) {
					row[cols[i]] = c
				}
			}
			rows = append(rows, row)
		}
	}
	flush()
	return tables
}

func readmeTable(t *testing.T, header ...string) []map[string]string {
	t.Helper()
	tables := readmeTables(t, header...)
	if len(tables) == 0 {
		t.Fatalf("README has no table with columns %v", header)
	}
	return tables[0]
}

func splitRow(line string) []string {
	line = strings.TrimSuffix(strings.TrimPrefix(line, "|"), "|")
	parts := strings.Split(line, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

var backtickRe = regexp.MustCompile("`([^`]+)`")

func backticked(s string) []string {
	var out []string
	for _, m := range backtickRe.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

var rangeRe = regexp.MustCompile("^`([^`]+)`\\s*–\\s*`([^`]+)`$")

// keyTokens returns the keys in a Key cell: "`c` / `u`" is two keys,
// "`1`–`9`" is the single key "1–9".
func keyTokens(cell string) []string {
	if m := rangeRe.FindStringSubmatch(cell); m != nil {
		return []string{m[1] + "–" + m[2]}
	}
	return backticked(cell)
}

// readmeCommands returns the `:` commands in a Command cell.
func readmeCommands(cell string) []string {
	var out []string
	for _, c := range backticked(cell) {
		if strings.HasPrefix(c, ":") {
			out = append(out, c)
		}
	}
	return out
}

type readmeKey struct {
	action string
	// mentioned are other keys named in the action text (e.g. `f`).
	mentioned []string
}

// readmeKeys parses the Global and Resource actions keybinding tables.
func readmeKeys(t *testing.T) (global, resource map[string]readmeKey) {
	t.Helper()
	tables := readmeTables(t, "Key", "Action")
	if len(tables) != 2 {
		t.Fatalf("README has %d keybinding tables, want 2 (Global, Resource actions)", len(tables))
	}
	parse := func(rows []map[string]string) map[string]readmeKey {
		out := map[string]readmeKey{}
		for _, r := range rows {
			for _, k := range keyTokens(r["Key"]) {
				out[k] = readmeKey{action: r["Action"], mentioned: backticked(r["Action"])}
			}
		}
		return out
	}
	return parse(tables[0]), parse(tables[1])
}

func sortedKeys[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// allViewKeys returns the keymap of every view 8top can show.
func allViewKeys() map[string][]binding {
	a := New(Config{API: newFakeAPI(), Renderer: stubRendererFor, Tick: noTick})
	out := map[string][]binding{}
	for _, k := range kinds {
		out["table/"+string(k.kind)] = a.newTable(k.kind, model.Scope{}).keys()
	}
	out["yaml"] = a.newText(textYAML, model.Object{}, "").keys()
	out["logs"] = a.newLogs(model.Object{}).keys()
	out["help"] = a.newHelp(a.top()).keys()
	out["global"] = globalKeys()
	return out
}

func TestReadmeGlobalKeys(t *testing.T) {
	global, _ := readmeKeys(t)
	impl := map[string]binding{}
	for _, b := range globalKeys() {
		impl[b.Help().Key] = b
	}
	for k, rk := range global {
		if strings.HasPrefix(k, ":") && len(k) > 1 {
			// A command, e.g. `:q`.
			if c, err := parseCommand(k, ""); err != nil || !c.quit {
				t.Errorf("README global %s (%s): not a quit command (err %v)", k, rk.action, err)
			}
			continue
		}
		b, ok := impl[k]
		if !ok {
			t.Errorf("README global key %s (%s) is not bound", k, rk.action)
			continue
		}
		if !strings.Contains(strings.ToLower(rk.action), strings.ToLower(b.Help().Desc)) {
			t.Errorf("key %s: help %q does not match README action %q", k, b.Help().Desc, rk.action)
		}
	}
	for k := range impl {
		if _, ok := global[k]; !ok {
			t.Errorf("global key %s is not in the README Global table", k)
		}
	}
}

func TestReadmeResourceKeys(t *testing.T) {
	_, resource := readmeKeys(t)
	impl := map[string]binding{}
	for _, k := range kinds {
		for _, b := range actionsFor(k.kind) {
			impl[b.Help().Key] = b
		}
	}
	if got, want := sortedKeys(impl), sortedKeys(resource); !slices.Equal(got, want) {
		t.Errorf("resource action keys = %v, README has %v", got, want)
	}
	for k, b := range impl {
		rk, ok := resource[k]
		if !ok {
			continue
		}
		if !strings.Contains(strings.ToLower(rk.action), strings.ToLower(b.Help().Desc)) {
			t.Errorf("key %s: help %q does not match README action %q", k, b.Help().Desc, rk.action)
		}
	}
}

// TestReadmeViewKeys checks that every key a view binds is documented,
// either as a key of its own or named in the action of another key (like
// `f` in "Stream actor logs (`f` toggles follow)").
func TestReadmeViewKeys(t *testing.T) {
	global, resource := readmeKeys(t)
	documented := map[string]bool{}
	for _, m := range []map[string]readmeKey{global, resource} {
		for k, rk := range m {
			documented[k] = true
			for _, x := range rk.mentioned {
				documented[x] = true
			}
		}
	}
	views := allViewKeys()
	for _, name := range sortedKeys(views) {
		for _, b := range views[name] {
			if !documented[b.Help().Key] {
				t.Errorf("view %s binds %s (%s), which the README does not document", name, b.Help().Key, b.Help().Desc)
			}
		}
	}
	// The other direction: every documented key is bound by some view.
	bound := map[string]bool{}
	for _, bs := range views {
		for _, b := range bs {
			bound[b.Help().Key] = true
		}
	}
	for _, m := range []map[string]readmeKey{global, resource} {
		for _, k := range sortedKeys(m) {
			if !bound[k] && !strings.HasPrefix(k, ":q") {
				t.Errorf("README key %s (%s) is not bound by any view", k, m[k].action)
			}
		}
	}
}

// TestReadmeActionKinds checks the per-kind rules stated in the README:
// actor actions only on actors, worker actions only on workers, and Edit only
// where the resource table says the kind is editable (templates show the
// read-only YAML instead).
func TestReadmeActionKinds(t *testing.T) {
	editable := map[model.Kind]bool{}
	for _, row := range readmeTable(t, "View", "Command") {
		cmds := readmeCommands(row["Command"])
		if len(cmds) == 0 {
			continue // reached by enter only (assignments)
		}
		c, err := parseCommand(cmds[0], "")
		if err != nil {
			t.Fatalf("README command %s: %v", row["Command"], err)
		}
		e := row["Editable"]
		editable[c.kind] = !strings.HasPrefix(e, "No") && !strings.HasPrefix(e, "Through")
	}
	_, resource := readmeKeys(t)
	for _, k := range kinds {
		for _, b := range actionsFor(k.kind) {
			key := b.Help().Key
			action := strings.ToLower(resource[key].action)
			switch {
			case strings.Contains(action, "actor") && k.kind != model.KindActor:
				t.Errorf("%s: actor action %s (%s) bound", k.kind, key, action)
			case strings.Contains(action, "worker") && k.kind != model.KindWorker:
				t.Errorf("%s: worker action %s (%s) bound", k.kind, key, action)
			case key == keyEdit.Help().Key && !editable[k.kind] && k.kind != model.KindActorTemplate:
				t.Errorf("%s: E bound but the README says it is not editable", k.kind)
			}
		}
		if editable[k.kind] && !has(actionsFor(k.kind), keyEdit) {
			t.Errorf("%s: README says editable but E is not bound", k.kind)
		}
	}
}

func newBufReader(s string) *bufio.Reader { return bufio.NewReader(strings.NewReader(s)) }
