package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/swtormy/tgbot-cli/internal/spec"
)

func hasType(f spec.Field, t string) bool {
	for _, x := range f.Types {
		if x == t {
			return true
		}
	}
	return false
}

func isScalarType(t string) bool {
	switch t {
	case "String", "Integer", "Float", "Boolean", "True", "InputFile":
		return true
	}
	return false
}

// coerce converts a command-line string to the JSON value the Bot API expects
// for field f. A returned file path means the value is a multipart upload.
func coerce(f spec.Field, raw string) (value any, file string, err error) {
	if f.IsFile() && strings.HasPrefix(raw, "@") {
		path := raw[1:]
		if st, e := os.Stat(path); e != nil || st.IsDir() {
			return nil, "", fmt.Errorf("--%s: cannot read file %q", f.Name, path)
		}
		return nil, path, nil
	}

	scalar := true
	for _, t := range f.Types {
		if !isScalarType(t) {
			scalar = false
		}
	}
	if !scalar {
		return coerceJSON(f, raw)
	}

	if hasType(f, "Integer") {
		if n, e := strconv.ParseInt(raw, 10, 64); e == nil {
			return n, "", nil
		}
	}
	if hasType(f, "Float") {
		if n, e := strconv.ParseFloat(raw, 64); e == nil {
			return n, "", nil
		}
	}
	if hasType(f, "Boolean") || hasType(f, "True") {
		if b, e := strconv.ParseBool(raw); e == nil {
			return b, "", nil
		}
	}
	if hasType(f, "String") {
		return raw, "", nil
	}
	if f.IsFile() {
		return nil, "", fmt.Errorf("--%s: use @path to upload a local file", f.Name)
	}
	return nil, "", fmt.Errorf("--%s: %q is not a valid %s", f.Name, raw, f.TypeLabel())
}

func coerceJSON(f spec.Field, raw string) (any, string, error) {
	text := raw
	if strings.HasPrefix(raw, "@") {
		b, err := os.ReadFile(raw[1:])
		if err != nil {
			return nil, "", fmt.Errorf("--%s: %v", f.Name, err)
		}
		text = string(b)
	}
	if !json.Valid([]byte(text)) {
		return nil, "", fmt.Errorf("--%s expects JSON (%s), got %q", f.Name, f.TypeLabel(), truncate(text, 60))
	}
	return json.RawMessage(text), "", nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
