package ui

import (
	"fmt"
	"sort"
	"strings"

	"go.ate.dev/8top/internal/model"
)

// kindInfo describes how a kind is shown and scoped.
type kindInfo struct {
	kind model.Kind
	// title is the resource name in the body title, e.g. "Actors".
	title string
	// aliases are the `:` commands that open the view. The first one is the
	// canonical name.
	aliases []string
	// atespaced kinds follow the current atespace (`0`, `1`–`9`).
	atespaced bool
}

var kinds = []kindInfo{
	{model.KindAtespace, "Atespaces", []string{"atespaces", "as"}, false},
	{model.KindActor, "Actors", []string{"actors", "a"}, true},
	{model.KindActorTemplate, "Templates", []string{"templates", "at"}, true},
	{model.KindTag, "Tags", []string{"tags", "t"}, true},
	{model.KindEgressPolicy, "Egress", []string{"egress", "ep"}, true},
	{model.KindWorker, "Workers", []string{"workers", "w"}, false},
	{model.KindAccessPolicy, "AccessPolicy", []string{"accesspolicy", "ap"}, true},
	{model.KindWorkerPool, "WorkerPools", []string{"workerpools", "wp"}, false},
	// Not in the README resource table: reached by `enter` on a worker.
	{model.KindActorAssignment, "Assignments", []string{"assignments"}, false},
}

func infoFor(k model.Kind) kindInfo {
	for _, ki := range kinds {
		if ki.kind == k {
			return ki
		}
	}
	return kindInfo{kind: k, title: string(k), aliases: []string{string(k)}}
}

// commandNames returns every alias, for prompt suggestions.
func commandNames() []string {
	var out []string
	for _, ki := range kinds {
		out = append(out, ki.aliases...)
	}
	out = append(out, "quit")
	sort.Strings(out)
	return out
}

// command is a parsed `:` command line.
type command struct {
	quit  bool
	kind  model.Kind
	scope model.Scope
	// setAtespace is true when the command named an atespace (or "all"),
	// which becomes the current atespace.
	setAtespace bool
}

// allAtespaces are the arguments that select every atespace.
func isAll(s string) bool { return s == "all" || s == "-A" || s == "0" }

// parseCommand parses a command line such as "actors myspace". atespace is
// the current atespace, used when the command does not name one.
func parseCommand(line, atespace string) (command, error) {
	fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), ":"))
	if len(fields) == 0 {
		return command{}, fmt.Errorf("empty command")
	}
	name, args := fields[0], fields[1:]
	switch name {
	case "q", "q!", "quit", "qa":
		return command{quit: true}, nil
	}
	var ki kindInfo
	found := false
	for _, k := range kinds {
		for _, a := range k.aliases {
			if a == name {
				ki, found = k, true
			}
		}
	}
	if !found {
		return command{}, fmt.Errorf("unknown command %q", name)
	}
	if len(args) > 1 {
		return command{}, fmt.Errorf("too many arguments to %q", name)
	}
	arg := ""
	if len(args) == 1 {
		arg = args[0]
	}
	c := command{kind: ki.kind}
	switch ki.kind {
	case model.KindAtespace, model.KindWorker:
		if arg != "" {
			return command{}, fmt.Errorf("%q takes no arguments", name)
		}
	case model.KindWorkerPool:
		if !isAll(arg) {
			c.scope.Namespace = arg
		}
	case model.KindActorAssignment:
		if arg == "" {
			return command{}, fmt.Errorf("%q needs a worker name", name)
		}
		c.scope.Worker = arg
	case model.KindEgressPolicy:
		// egress [atespace[/actor]]
		c.scope.Atespace = atespace
		if arg != "" {
			c.setAtespace = true
			ns, actor, _ := strings.Cut(arg, "/")
			if isAll(ns) {
				ns = ""
			}
			c.scope.Atespace, c.scope.Actor = ns, actor
		}
	default:
		c.scope.Atespace = atespace
		if arg != "" {
			c.setAtespace = true
			c.scope.Atespace = arg
			if isAll(arg) {
				c.scope.Atespace = ""
			}
		}
	}
	return c, nil
}
