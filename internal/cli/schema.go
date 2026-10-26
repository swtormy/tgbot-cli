package cli

import (
	"strings"

	"github.com/swtormy/tgbot-cli/internal/spec"
)

func typeSchema(t string) map[string]any {
	switch t {
	case "String":
		return map[string]any{"type": "string"}
	case "Integer":
		return map[string]any{"type": "integer"}
	case "Float":
		return map[string]any{"type": "number"}
	case "Boolean", "True":
		return map[string]any{"type": "boolean"}
	case "InputFile":
		return map[string]any{"type": "string", "description": "file_id or URL; on the command line use @path to upload"}
	}
	if elem, ok := strings.CutPrefix(t, "Array of "); ok {
		return map[string]any{"type": "array", "items": typeSchema(elem)}
	}
	return map[string]any{"type": "object", "x-telegram-type": t}
}

func fieldSchema(f spec.Field) map[string]any {
	var s map[string]any
	if len(f.Types) == 1 {
		s = typeSchema(f.Types[0])
	} else {
		alts := make([]any, len(f.Types))
		for i, t := range f.Types {
			alts[i] = typeSchema(t)
		}
		s = map[string]any{"anyOf": alts}
	}
	s["description"] = f.Description
	return s
}

// methodSchema builds the JSON Schema (draft 2020-12) of a method's request body.
func methodSchema(m spec.Method) map[string]any {
	props := map[string]any{}
	required := []string{}
	for _, f := range m.Fields {
		props[f.Name] = fieldSchema(f)
		if f.Required {
			required = append(required, f.Name)
		}
	}
	return map[string]any{
		"$schema":    "https://json-schema.org/draft/2020-12/schema",
		"title":      m.Name,
		"type":       "object",
		"properties": props,
		"required":   required,
	}
}
