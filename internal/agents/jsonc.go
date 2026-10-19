package agents

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/tailscale/hujson"
)

// mergeEntry adds or updates agent_servers.<e.Name> in raw, preserving every
// byte outside the edited member. It reports whether anything changed.
//
// The file is JWCC (Zed's settings format: JSON with comments and commas), so
// the edit is a splice against the parsed offsets rather than a re-encode. A
// map round-trip would reorder keys and drop comments; a host that rewrites an
// editor's settings must not do that.
func mergeEntry(raw []byte, e Entry) ([]byte, bool, error) {
	v, err := hujson.Parse(raw)
	if err != nil {
		return nil, false, fmt.Errorf("settings is not JSON or JSONC: %w", err)
	}
	root, ok := v.Value.(*hujson.Object)
	if !ok {
		return nil, false, errors.New("settings root is not an object")
	}

	unit := detectUnit(raw)
	want := normalized(e.value())

	as := findMember(root, "agent_servers")
	if as == nil {
		out, insErr := insertMember(raw, v, root, "agent_servers", map[string]any{e.Name: want}, unit)
		return out, true, insErr
	}

	asObj, ok := as.Value.Value.(*hujson.Object)
	if !ok {
		return nil, false, errors.New("agent_servers is not an object")
	}

	if m := findMember(asObj, e.Name); m != nil {
		got, decodeErr := decodeValue(m.Value)
		if decodeErr != nil {
			return nil, false, decodeErr
		}
		if reflect.DeepEqual(got, want) {
			return raw, false, nil
		}
		indent := lineIndent(raw, m.Name.StartOffset)
		return splice(raw, m.Value.StartOffset, m.Value.EndOffset, prettyValue(want, indent, unit)), true, nil
	}

	out, err := insertMember(raw, as.Value, asObj, e.Name, want, unit)
	return out, true, err
}

// removeEntry deletes agent_servers.<name>, and agent_servers itself when it
// was the only entry. It reports whether anything changed.
func removeEntry(raw []byte, name string) ([]byte, bool, error) {
	v, err := hujson.Parse(raw)
	if err != nil {
		return nil, false, fmt.Errorf("settings is not JSON or JSONC: %w", err)
	}
	root, ok := v.Value.(*hujson.Object)
	if !ok {
		return nil, false, errors.New("settings root is not an object")
	}
	as := findMember(root, "agent_servers")
	if as == nil {
		return raw, false, nil
	}
	asObj, ok := as.Value.Value.(*hujson.Object)
	if !ok {
		return nil, false, errors.New("agent_servers is not an object")
	}
	if len(asObj.Members) == 1 && findMember(asObj, name) != nil {
		return cutMember(raw, root, "agent_servers"), true, nil
	}
	if findMember(asObj, name) == nil {
		return raw, false, nil
	}
	return cutMember(raw, asObj, name), true, nil
}

// insertMember writes `"name": value` as the last member of obj, matching the
// indentation of its siblings. owner is the Value whose span covers obj.
func insertMember(raw []byte, owner hujson.Value, obj *hujson.Object, name string, val any, unit string) ([]byte, error) {
	objIndent := lineIndent(raw, owner.StartOffset)
	closeBrace := owner.EndOffset - 1
	if closeBrace < 0 || closeBrace >= len(raw) || raw[closeBrace] != '}' {
		return nil, errors.New("internal: object span does not end at a closing brace")
	}

	if len(obj.Members) == 0 {
		memberIndent := objIndent + unit
		member := strconv.Quote(name) + ": " + prettyValue(val, memberIndent, unit)
		text := "\n" + memberIndent + member + "\n" + objIndent
		return splice(raw, owner.StartOffset+1, owner.StartOffset+1, text), nil
	}

	memberIndent := lineIndent(raw, obj.Members[0].Name.StartOffset)
	member := strconv.Quote(name) + ": " + prettyValue(val, memberIndent, unit)

	// Insert before the whitespace that indents the closing brace, so that
	// whitespace (and any comment in it) stays where it was.
	at := closeBrace
	for at > owner.StartOffset && isSpace(raw[at-1]) {
		at--
	}
	sep := ","
	if at > owner.StartOffset && raw[at-1] == ',' {
		sep = "" // an existing trailing comma already separates the members
	}
	return splice(raw, at, at, sep+"\n"+memberIndent+member), nil
}

// cutMember removes a member and one adjacent comma.
func cutMember(raw []byte, obj *hujson.Object, name string) []byte {
	idx := -1
	for i := range obj.Members {
		if memberName(obj.Members[i]) == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return raw
	}
	m := obj.Members[idx]
	start, end := m.Name.StartOffset, m.Value.EndOffset

	// Prefer the preceding comma: it removes the separator and the member in
	// one span, leaving the whitespace that ends the previous member intact.
	if i := prevNonSpace(raw, start-1); i >= 0 && raw[i] == ',' {
		return splice(raw, i, end, "")
	}
	// First member: remove through the following comma and the whitespace up
	// to the next member, so no blank line is left behind.
	if idx+1 < len(obj.Members) {
		return splice(raw, start, obj.Members[idx+1].Name.StartOffset, "")
	}
	// Only member.
	return splice(raw, start, end, "")
}

func findMember(obj *hujson.Object, name string) *hujson.ObjectMember {
	for i := range obj.Members {
		if memberName(obj.Members[i]) == name {
			return &obj.Members[i]
		}
	}
	return nil
}

func memberName(m hujson.ObjectMember) string {
	lit, ok := m.Name.Value.(hujson.Literal)
	if !ok {
		return ""
	}
	s, err := strconv.Unquote(string(lit))
	if err != nil {
		return string(lit)
	}
	return s
}

func decodeValue(v hujson.Value) (any, error) {
	var out any
	if err := json.Unmarshal(bytes.TrimSpace(v.Pack()), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// decodeDoc returns raw as a standard JSON tree, comments and commas dropped.
//
// The input is cloned first: Standardize rewrites the value in place, and
// Literal values alias the input bytes, so standardizing raw would blank the
// comments in the caller's slice — including the slice about to be written.
func decodeDoc(raw []byte) (map[string]any, error) {
	v, err := hujson.Parse(bytes.Clone(raw))
	if err != nil {
		return nil, err
	}
	v.Standardize()
	var doc map[string]any
	if err := json.Unmarshal(v.Pack(), &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// prettyValue renders v starting at indent, with nested lines a unit deeper.
//
// It is not MarshalIndent: an array of scalars stays on one line
// (`"args": ["-driver", "tmpdir"]`), which is how the surrounding file is
// written. Object keys are sorted, so the result is deterministic.
func prettyValue(v any, indent, unit string) string {
	switch x := v.(type) {
	case map[string]any:
		if len(x) == 0 {
			return "{}"
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		b.WriteString("{\n")
		for i, k := range keys {
			b.WriteString(indent + unit + strconv.Quote(k) + ": " + prettyValue(x[k], indent+unit, unit))
			if i < len(keys)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString(indent + "}")
		return b.String()

	case []any:
		if len(x) == 0 {
			return "[]"
		}
		if isScalarList(x) {
			parts := make([]string, len(x))
			for i, item := range x {
				parts[i] = scalarJSON(item)
			}
			return "[" + strings.Join(parts, ", ") + "]"
		}
		var b strings.Builder
		b.WriteString("[\n")
		for i, item := range x {
			b.WriteString(indent + unit + prettyValue(item, indent+unit, unit))
			if i < len(x)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString(indent + "]")
		return b.String()

	default:
		return scalarJSON(v)
	}
}

func isScalarList(xs []any) bool {
	for _, x := range xs {
		switch x.(type) {
		case map[string]any, []any:
			return false
		}
	}
	return true
}

func scalarJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

func detectUnit(raw []byte) string {
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == line || trimmed == "" {
			continue
		}
		return line[:len(line)-len(trimmed)]
	}
	return "  "
}

// lineIndent returns the whitespace from the start of off's line to its first
// non-space byte.
func lineIndent(raw []byte, off int) string {
	if off > len(raw) {
		off = len(raw)
	}
	start := bytes.LastIndexByte(raw[:off], '\n') + 1
	i := start
	for i < off && (raw[i] == ' ' || raw[i] == '\t') {
		i++
	}
	return string(raw[start:i])
}

func prevNonSpace(raw []byte, i int) int {
	for i >= 0 && isSpace(raw[i]) {
		i--
	}
	return i
}

func isSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

func splice(raw []byte, start, end int, text string) []byte {
	out := make([]byte, 0, len(raw)-(end-start)+len(text))
	out = append(out, raw[:start]...)
	out = append(out, text...)
	out = append(out, raw[end:]...)
	return out
}
