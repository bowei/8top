# README

8top is a top, k9s equivalent for a Substrate cluster. It is a CLI application
that shows the current state of all of the Actors, etc. Editable configuration
can be interacted with and updated using $EDITOR.

8top should copy the UI from k9s.

## Goals

- One terminal screen that answers "what is my Substrate cluster doing right
  now?": which actors are running, suspended, or crashed, which workers are
  free, and where each actor is placed.
- Navigate the Substrate resource hierarchy (atespace → actors → egress
  policy, worker → assignments) with the keyboard alone.
- Run common lifecycle operations (resume, suspend, revert, delete) on the
  selected row without switching to `kubectl ate`.
- View any resource as YAML, and edit mutable configuration in `$EDITOR` with
  the same dump → edit → apply flow as `kubectl edit`.

Non-goals: replacing `kubectl ate` for scripting, bootstrapping a cluster, or
the `admin` commands (CA / JWT pools).

## Resources

8top talks to the Substrate API server (`api/ateapi/v1/ateapi.proto`) over
gRPC. Actors, workers, templates, and tags live in the control plane's
database, not in etcd, so they are only visible through this API. It reads
`WorkerPool` CRDs from the Kubernetes API.

| View | Command | Scope | Columns | Editable |
| :--- | :--- | :--- | :--- | :--- |
| Atespaces | `:atespaces`, `:as` | Global | NAME, AGE | No (create / delete only) |
| Actors | `:actors`, `:a` | Atespace | ATESPACE, NAME, TEMPLATE, STATE, WORKER POD, WORKER IP, VERSION, AGE | `actor_template` (actor must be suspended), `worker_selector` |
| Actor templates | `:templates`, `:at` | Atespace | ATESPACE, NAME, GOLDEN TAG, ERROR, AGE | No. Templates are immutable |
| Tags | `:tags`, `:t` | Atespace | ATESPACE, NAME, SOURCE ACTOR, SCOPE, STATE, AGE | `scope` (`atespace` / `published`) |
| Egress policy | `:egress`, `:ep` (or `e` on an actor) | Actor | ATESPACE, ACTOR, RULES, VERSION, AGE | Yes, replaced as a whole |
| Workers | `:workers`, `:w` | Global | NAMESPACE, POOL, POD, STATE, STATUS, CPU, MEMORY, AGE | No (drain / mark (un)available only) |
| Actor assignments | `enter` on a worker | Worker | ATESPACE, ACTOR, ACTOR UID, CPU, MEMORY, AGE | No |
| Access policy | `:accesspolicy`, `:ap` | Global or atespace | SCOPE, BINDINGS, ROLES, VERSION, AGE | Yes |
| Worker pools | `:workerpools`, `:wp` | Kubernetes namespace | NAMESPACE, NAME, READY, DESIRED, AGE | Through the Kubernetes API |

Actor state (`ACTOR_STATE_*`) is shown without the prefix and color-coded the
way k9s colors pod phases:

| State | Color |
| :--- | :--- |
| `RUNNING` | green |
| `RESUMING`, `SUSPENDING`, `REVERTING` | yellow |
| `SUSPENDED` | gray |
| `CRASHED` | red |
| `DELETING` | magenta |

## UI

The layout copies k9s:

```
┌──────────────────────────────────────────────────────────────────────────┐
│ Context: kind-ate      <a> Resume    <s> Suspend    <e> Egress   ___     │
│ Endpoint: port-fwd     <r> Revert    <y> YAML       <l> Logs    / _ \    │
│ 8top Rev: v0.1.0       <ctrl-d> Delete  <E> Edit    <?> Help   ( 8top )  │
├──────────────────────────── Actors(default)[42] ─────────────────────────┤
│ ATESPACE  NAME        TEMPLATE         STATE      WORKER POD     VERSION │
│ default   counter-1   default/counter  RUNNING    ate/pool-a-0   7       │
│ default   counter-2   default/counter  SUSPENDED                 4       │
│ default   counter-3   default/counter  CRASHED                   2       │
├──────────────────────────────────────────────────────────────────────────┤
│ <actors> <default>                                                       │
└──────────────────────────────────────────────────────────────────────────┘
```

- **Header**: cluster context, API endpoint, version, and key hints for the
  current view.
- **Body**: a sortable, filterable table of the current resource. The title
  shows the resource, scope, and row count.
- **Breadcrumbs**: the view stack. `esc` pops back to the previous view.
- **Flash line**: results and errors from the last action, e.g.
  `FailedPrecondition: actor must be SUSPENDED to change its template`.

### Keybindings

Global:

| Key | Action |
| :--- | :--- |
| `:` | Command mode. Jump to a view (`:actors`, `:workers`, `:actors myspace`) |
| `/` | Filter rows of the current view (regex; `!` prefix inverts) |
| `esc` | Clear the filter, or pop back to the previous view |
| `enter` | Drill down (atespace → its actors, worker → its assignments, actor → its egress policy) |
| `?` | Help for the current view |
| `0` | All atespaces (`-A`) |
| `1`–`9` | Favorite atespaces |
| `shift-<col>` | Sort by column (e.g. `shift-s` for STATE, `shift-a` for AGE). Press again to reverse, then to move to the next column with that letter |
| `ctrl-r` | Refresh now |
| `ctrl-c`, `:q` | Quit |

Resource actions (shown in the header only where they apply):

| Key | Action | API call |
| :--- | :--- | :--- |
| `y` | View YAML (protojson rendered as YAML) | `Get*` |
| `d` | Describe: YAML plus related resources | `Get*` |
| `E` | Edit in `$EDITOR` | `Update*` |
| `ctrl-d` | Delete (asks for confirmation) | `Delete*` |
| `a` | Resume actor | `ResumeActor` |
| `s` | Suspend actor (`w` in the dialog waits for the snapshot to reach storage) | `SuspendActor` |
| `r` | Revert actor to its last external snapshot | `RevertActor` |
| `e` | Show the actor's egress policy | `GetActorEgressPolicy` |
| `l` | Stream actor logs (`f` toggles follow) | same stream as `kubectl ate logs actor` |
| `p` | Port-forward to the actor | same as `kubectl ate proxy actor` |
| `t` | Tag a suspended actor's snapshot | `CreateTag` |
| `n` | Drain worker | `DrainWorker` |
| `c` / `u` | Mark worker available / unavailable | `MarkWorkerAvailable` / `MarkWorkerUnavailable` |

Anything destructive or disruptive (delete, revert, suspend, drain) asks for
confirmation first.

## Editing with `$EDITOR`

`E` on an editable resource works like `kubectl edit`:

1. Fetch the current resource and write it to a temp file as YAML. The file
   includes `metadata.uid` and `metadata.version`.
2. Open `$EDITOR` (or `$VISUAL`, falling back to `vi`) with
   `tea.ExecProcess`, which releases the terminal until the editor exits.
3. If nothing changed, or the file is empty, cancel.
4. Otherwise send the matching `Update*` call. The server uses
   `uid`/`version` for optimistic concurrency, so if someone else changed the
   resource first the call fails with `Aborted`.
5. On any error, reopen the editor with the error as a comment at the top of
   the file so no edits are lost, the same as k9s / `kubectl edit`.

Which fields can be edited:

- **EgressPolicy**: the whole `rules` list. **AccessPolicy**: the whole
  `bindings` list. The update replaces the entire policy.
- **Actor**: only `actor_template` and `worker_selector`. Changing the
  template needs the actor to be `SUSPENDED`. Other fields are read-only, and
  8top rejects edits to them before calling the server.
- **Tag**: only `scope`.
- **ActorTemplate**: immutable. `E` opens the YAML read-only, with a hint to
  create a new template and repoint suspended actors at it instead.

## Connecting to the cluster

8top connects the same way `kubectl ate` does, and takes the same flags:

| Flag | Description | Default |
| :--- | :--- | :--- |
| `--kubeconfig` | Path to the kubeconfig file | `~/.kube/config` |
| `--context` | Kubeconfig context to use | current context |
| `--endpoint` | gRPC endpoint for the API server. Skips the port-forward | |
| `--token-file` | Bearer token for ate-api auth, or `-` for stdin | Kubernetes ServiceAccount token |
| `-a`, `--atespace` | Atespace to open in | all atespaces |
| `--refresh` | Polling interval | `2s` |
| `--readonly` | Disable every mutating action and the editor | `false` |
| `--command` | View to open at startup (e.g. `workers`) | `actors` |

Without `--endpoint`, 8top finds the `ate-api-server` pods using the
kubeconfig and opens a background port-forward to one of them, reconnecting
if that pod goes away.

## Architecture

```
cmd/8top/            main, flag parsing
internal/client/     gRPC client, auth, auto port-forward (shared with / borrowed from kubectl-ate)
internal/model/      per-resource listers and in-memory cache
internal/ui/         Bubble Tea models: app (view stack), header, table, YAML, logs, prompt, breadcrumbs
internal/render/     one renderer per resource: columns, colors, sort keys
internal/style/      Lip Gloss styles and color theme
internal/edit/       $EDITOR round-trip, diffing, field validation
internal/fake/       in-memory fake of the Substrate API over bufconn, for tests
```

- **Language / libraries**: Go, with the [Charm](https://charm.sh) stack:
  - [Bubble Tea](https://github.com/charmbracelet/bubbletea) for the
    Elm-style `Model` / `Update` / `View` loop. The root model holds the view
    stack; each view (table, YAML, logs, help) is its own sub-model.
  - [Bubbles](https://github.com/charmbracelet/bubbles) for components:
    `table` for resource lists, `viewport` for YAML / describe / logs,
    `textinput` for the `:` command and `/` filter prompts, `help` and
    `key` for keybindings and the header hints, `spinner` for in-flight
    actions.
  - [Lip Gloss](https://github.com/charmbracelet/lipgloss) for layout
    (header, bordered body, breadcrumbs) and the state colors.

  Confirmation dialogs are a small yes/no model in `internal/ui/` built
  from these, not a separate forms library.

  Generated `ateapi` clients and connection code come from
  `go.ate.dev/substrate`.
- **Data refresh**: the Substrate API has no Watch RPCs, so each view polls
  its `List*` call every `--refresh` interval and follows page tokens. A
  `tea.Tick` fires a `tea.Cmd` that fetches the list and returns it as a
  message; `Update` applies it. Only the active view is polled. Rows are
  diffed by key so the selection and scroll position stay put, and changed
  rows briefly highlight, as in k9s.
- **Rendering**: one `Renderer` per resource type turns API objects into
  table rows and styles. The table, filter, sort, YAML, and describe views
  are shared across all resources.
- **Actions** (resume, suspend, delete, ...) run as `tea.Cmd`s so gRPC calls
  never block `Update`. Each returns a result message that goes to the flash
  line and triggers an immediate refresh. Log streaming sends one message
  per batch of lines into the logs viewport.

## Testing

The UI is tested at four levels, plus a docs check. Tests follow standard Go
style: the `testing` package, table-driven cases, and plain `if got != want
{ t.Errorf(...) }` checks with `slices.Equal` / `reflect.DeepEqual` where
needed. No assertion or mocking libraries (testify, gomock, ginkgo). Fakes
are hand-written. A Bubble Tea model is just `Update(msg) (Model, Cmd)` and
`View() string`, so tests can drive it directly without running a real
terminal.

### Renderers

Table-driven unit tests for each `Renderer` in `internal/render/`: given an
API object, check the cells, the sort key, and the style picked for each
row. These tests cover the details that break quietly, such as stripping the
`ACTOR_STATE_` prefix, empty `WORKER POD` for suspended actors, `DISABLED` /
`ERROR` for templates, and AGE formatting with an injected clock.

### Model behavior

Tests build a model with fake data, send it messages, and check the state
that comes back:

```go
m := newTestApp(t, withActors(running("counter-1"), suspended("counter-2")))
m = send(m, tea.WindowSizeMsg{Width: 120, Height: 40})
m = send(m, keys("/counter-2"), key(tea.KeyEnter))
if got, want := m.visibleRowNames(), []string{"counter-2"}; !slices.Equal(got, want) {
	t.Errorf("visible rows = %v, want %v", got, want)
}
```

They cover:

- Navigation: `:` commands, `enter` drill-down, `esc` popping the view
  stack, and breadcrumbs.
- Filter and sort, plus selection and scroll staying put when a refresh
  adds, removes, or reorders rows.
- Key hints: each view shows only the actions that apply, and `--readonly`
  hides every mutating action.
- Actions: pressing `s` on a running actor asks for confirmation; confirming
  returns a `tea.Cmd` that calls `SuspendActor`; the result message reaches
  the flash line and triggers a refresh. Error messages (`FailedPrecondition`,
  `Aborted`) show up in the flash line.

A test runs a returned `tea.Cmd` by calling it and feeding its message back
into `Update`. Time-based commands (`tea.Tick`) use an injected clock so
tests never sleep.

### Golden snapshots

`View()` output for each screen is compared to golden files in
`internal/ui/testdata/`. This catches layout regressions: column widths,
truncation, borders, header hints, and colors.

- Snapshots render at fixed sizes: 80×24, 120×40, and a narrow 60×20 to
  check truncation.
- Lip Gloss is forced to a fixed color profile so output is the same on
  every machine and in CI. One set of snapshots uses no color, for
  readable diffs; a smaller set keeps ANSI color to check state colors.
- Fixtures use fixed names, timestamps, and a fake clock, so AGE and other
  values don't change between runs.
- `go test ./internal/ui/... -update` rewrites the golden files. Changed
  snapshots get reviewed in the PR diff like any other change.

### Fake API server

Integration tests run the real gRPC client against an in-memory
implementation of the `ateapi` service, served over
`google.golang.org/grpc/test/bufconn` (already pulled in through gRPC). The
fake keeps actors, workers, templates, tags, and policies in maps, enforces
the rules 8top depends on (template changes need `SUSPENDED`, `uid`/`version`
checks on update, paging through `List*`), and can be told to fail or be
slow. These tests cover polling, paging, reconnecting after an error, and
end-to-end actions such as resume → `RESUMING` → `RUNNING` across refreshes.

The `$EDITOR` flow is tested by pointing `EDITOR` at a small test helper
binary that rewrites the temp file in a scripted way: no change, a valid
edit, an edit to a read-only field, invalid YAML, or a version conflict. The
test checks the resulting `Update*` call and that the editor reopens with the
error comment.

### Keeping docs and keys in sync

A test loads the key map for each view and checks it against the keybinding
tables in this README, so a binding can't be added, changed, or removed
without updating the docs.

### Running

```bash
go test ./...                       # everything
go test -race ./...                 # what CI runs
go test ./internal/ui/... -update   # regenerate golden snapshots
```

A manual smoke test against a kind cluster (see below) is still worthwhile
before a release, because it is the only check of real terminal handling:
resize, alternate screen, mouse, and handing the terminal to `$EDITOR`.

## Building and running

```bash
go build -o bin/8top ./cmd/8top

# Against the current kubeconfig context, with auto port-forward
./bin/8top

# Open one atespace, refresh every 5s
./bin/8top -a default --refresh 5s

# Directly against an endpoint, view only
./bin/8top --endpoint localhost:8080 --readonly
```

For a local cluster, follow the Substrate quickstart
(`hack/create-kind-cluster.sh`, then `ate-setup --kind deploy ate-system`) and
create a few actors from the counter demo (`demos/counter`) to have something
to look at.

## Status

First implementation. Everything above is implemented except port-forward
(`p` only shows "not implemented yet"). It has been tested against the fake
API server, not yet against a live cluster.

Substrate is pre-1.0 and its API may change. For now `go.mod` builds against a
local checkout (`replace go.ate.dev/substrate => ../s`), because the published
`go.ate.dev/substrate` release does not include `api/ateapi/v1` yet. Once it
does, 8top should pin a released version instead.

Possible later work:

- `top`-style CPU / memory columns for workers and running actors (like
  `kubectl ate top workers`).
- Pulse view: cluster summary of actors by state, worker utilization, and
  resume / suspend rate.
- Skins and custom aliases/hotkeys in `~/.config/8top/`, as k9s does.
>>>>>>> d34ae71 (generate)
