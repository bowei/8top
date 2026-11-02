// Package edit implements YAML views of objects and the $EDITOR round-trip.
//
// The YAML is the protojson form of the API message converted to YAML, the
// same shape `kubectl ate get ... -o yaml` prints and `kubectl ate create -f`
// reads, so a file can move between the two tools.
package edit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"sigs.k8s.io/yaml"

	"go.ate.dev/8top/internal/model"
)

// Header is the comment block at the top of every file opened in the editor.
const Header = `# Please edit the object below. Lines beginning with a '#' will be ignored,
# and an empty file will abort the edit. If an error occurs while saving this file will be
# reopened with the relevant failures.
#
`

// editableFields lists, per editable kind, the top-level proto field names
// the user may change. Every other field must stay as it was read.
var editableFields = map[model.Kind][]protoreflect.Name{
	model.KindActor:        {"actor_template", "worker_selector"},
	model.KindTag:          {"scope"},
	model.KindEgressPolicy: {"rules"},
	model.KindAccessPolicy: {"bindings"},
}

// Marshal renders o as YAML, for the YAML view and as the file to edit.
func Marshal(o model.Object) ([]byte, error) {
	if o.Raw == nil {
		return nil, fmt.Errorf("%s %q has no data", o.Kind, o.Name)
	}
	msg, ok := o.Raw.(proto.Message)
	if !ok {
		// Kubernetes objects such as *atev1.WorkerPool.
		b, err := yaml.Marshal(o.Raw)
		if err != nil {
			return nil, fmt.Errorf("marshal %s %q as YAML: %w", o.Kind, o.Name, err)
		}
		return b, nil
	}
	b, err := protojson.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal %s %q: %w", o.Kind, o.Name, err)
	}
	// Round-trip through encoding/json, as kubectl-ate does, so the output
	// does not depend on protojson's randomized spacing.
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("marshal %s %q: %w", o.Kind, o.Name, err)
	}
	if b, err = json.Marshal(v); err != nil {
		return nil, fmt.Errorf("marshal %s %q: %w", o.Kind, o.Name, err)
	}
	yb, err := yaml.JSONToYAML(b)
	if err != nil {
		return nil, fmt.Errorf("marshal %s %q as YAML: %w", o.Kind, o.Name, err)
	}
	return yb, nil
}

// EditFile returns the contents of the file to open in the editor: Header
// followed by the YAML from Marshal.
func EditFile(o model.Object) ([]byte, error) {
	y, err := Marshal(o)
	if err != nil {
		return nil, err
	}
	return append([]byte(Header), y...), nil
}

// Editable reports whether objects of kind can be edited. For kinds that are
// not editable, reason says why (shown in the flash line).
func Editable(kind model.Kind) (ok bool, reason string) {
	if _, ok := editableFields[kind]; ok {
		return true, ""
	}
	switch kind {
	case model.KindActorTemplate:
		return false, "templates are immutable: create a new template and repoint suspended actors with `kubectl ate update actor --template`"
	case model.KindWorkerPool:
		return false, "worker pools are Kubernetes resources: edit them with kubectl"
	case "":
		return false, "nothing selected"
	default:
		return false, fmt.Sprintf("%s are read-only", kind)
	}
}

// EditableFields returns the YAML (protojson) names of the top-level fields
// that may be changed for kind, or nil if kind is not editable.
func EditableFields(kind model.Kind) []string {
	var out []string
	for _, n := range editableFields[kind] {
		out = append(out, jsonName(n))
	}
	return out
}

// Apply parses edited (the file contents after the editor exits), checks
// that only editable fields changed, and writes the change with api.Update.
// changed is false, with a nil error, if the user made no change or emptied
// the file. On error the caller should reopen the editor with Annotate.
func Apply(ctx context.Context, api model.API, orig model.Object, edited []byte) (updated model.Object, changed bool, err error) {
	if ok, reason := Editable(orig.Kind); !ok {
		return orig, false, errors.New(reason)
	}
	origMsg, ok := orig.Raw.(proto.Message)
	if !ok || origMsg == nil {
		return orig, false, fmt.Errorf("%s %q has no API object to update", orig.Kind, orig.Name)
	}

	body := stripLeadingComments(edited)
	if isEmpty(body) {
		return orig, false, nil
	}
	if origYAML, err := Marshal(orig); err == nil && bytes.Equal(body, origYAML) {
		return orig, false, nil
	}

	j, err := yaml.YAMLToJSON(body)
	if err != nil {
		return orig, false, fmt.Errorf("invalid YAML: %w", err)
	}
	typeName := origMsg.ProtoReflect().Descriptor().Name()
	if s := strings.TrimSpace(string(j)); s == "null" || !strings.HasPrefix(s, "{") {
		return orig, false, fmt.Errorf("invalid %s: expected a YAML mapping", typeName)
	}
	newMsg := origMsg.ProtoReflect().New().Interface()
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(j, newMsg); err != nil {
		return orig, false, fmt.Errorf("invalid %s: %w", typeName, err)
	}

	if proto.Equal(origMsg, newMsg) {
		return orig, false, nil
	}
	if path := firstReadOnlyChange(origMsg.ProtoReflect(), newMsg.ProtoReflect(), editableFields[orig.Kind]); path != "" {
		return orig, false, fmt.Errorf("field %q is read-only; only %s can be edited",
			path, strings.Join(EditableFields(orig.Kind), ", "))
	}

	want := orig
	want.Raw = newMsg
	got, err := api.Update(ctx, want)
	if err != nil {
		return orig, false, fmt.Errorf("update %s %s: %w", orig.Kind, objectName(orig), err)
	}
	return got, true, nil
}

// Annotate returns content with err added as a comment block at the top,
// replacing any error block from a previous attempt.
func Annotate(content []byte, err error) []byte {
	var b bytes.Buffer
	b.WriteString(Header)
	if err != nil {
		lines := strings.Split(strings.TrimRight(err.Error(), "\n"), "\n")
		for i, l := range lines {
			if i == 0 {
				b.WriteString("# error: " + l + "\n")
			} else {
				b.WriteString("#   " + l + "\n")
			}
		}
		b.WriteString("#\n")
	}
	b.Write(stripLeadingComments(content))
	return b.Bytes()
}

// EditorCommand returns the command that edits path, using $VISUAL, then
// $EDITOR, then vi. The command may include arguments (EDITOR="code -w").
func EditorCommand(path string) *exec.Cmd {
	var args []string
	for _, env := range []string{"VISUAL", "EDITOR"} {
		if args = strings.Fields(os.Getenv(env)); len(args) > 0 {
			break
		}
	}
	if len(args) == 0 {
		args = []string{"vi"}
	}
	return exec.Command(args[0], append(args[1:], path)...)
}

// stripLeadingComments removes the lines starting with '#' at the top of
// content (the Header and any error block).
func stripLeadingComments(content []byte) []byte {
	for len(content) > 0 && content[0] == '#' {
		i := bytes.IndexByte(content, '\n')
		if i < 0 {
			return nil
		}
		content = content[i+1:]
	}
	return content
}

// isEmpty reports whether content holds nothing but whitespace and comment
// lines.
func isEmpty(content []byte) bool {
	for _, l := range bytes.Split(content, []byte("\n")) {
		l = bytes.TrimSpace(l)
		if len(l) > 0 && l[0] != '#' {
			return false
		}
	}
	return true
}

// firstReadOnlyChange returns the protojson path of the first field, other
// than the top-level editable ones, that differs between a and b, or "" if
// there is none.
func firstReadOnlyChange(a, b protoreflect.Message, editable []protoreflect.Name) string {
	fields := a.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if containsName(editable, fd.Name()) {
			continue
		}
		if p := diffField(a, b, fd); p != "" {
			return p
		}
	}
	return ""
}

// diffField returns the path of the first difference in field fd of a and b.
// It descends into singular message fields so errors name the leaf, such as
// "metadata.version".
func diffField(a, b protoreflect.Message, fd protoreflect.FieldDescriptor) string {
	name := fd.JSONName()
	if fd.Message() != nil && !fd.IsList() && !fd.IsMap() {
		// Get returns an empty message for an unset field, so a missing
		// block is reported by the first leaf that was set on the other side.
		am, bm := a.Get(fd).Message(), b.Get(fd).Message()
		if proto.Equal(am.Interface(), bm.Interface()) {
			return ""
		}
		sub := fd.Message().Fields()
		for i := 0; i < sub.Len(); i++ {
			if p := diffField(am, bm, sub.Get(i)); p != "" {
				return name + "." + p
			}
		}
		return name
	}
	if !a.Get(fd).Equal(b.Get(fd)) {
		return name
	}
	return ""
}

func containsName(names []protoreflect.Name, n protoreflect.Name) bool {
	for _, x := range names {
		if x == n {
			return true
		}
	}
	return false
}

// jsonName converts a proto field name to its protojson (lowerCamelCase) name.
func jsonName(n protoreflect.Name) string {
	parts := strings.Split(string(n), "_")
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

func objectName(o model.Object) string {
	if o.Atespace != "" {
		return o.Atespace + "/" + o.Name
	}
	return o.Name
}
