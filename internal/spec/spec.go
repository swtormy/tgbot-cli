// Package spec exposes the embedded Telegram Bot API method catalog.
package spec

import (
	_ "embed"
	"encoding/json"
	"sort"
	"strings"
)

//go:embed api.json
var raw []byte

type Field struct {
	Name        string   `json:"name"`
	Types       []string `json:"types"`
	Required    bool     `json:"required"`
	Description string   `json:"description"`
}

type Method struct {
	Name        string   `json:"name"`
	Href        string   `json:"href"`
	Description []string `json:"description"`
	Returns     []string `json:"returns"`
	Fields      []Field  `json:"fields"`
}

type Spec struct {
	Version     string            `json:"version"`
	ReleaseDate string            `json:"release_date"`
	Methods     map[string]Method `json:"methods"`
}

func Load() (*Spec, error) {
	var s Spec
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (s *Spec) Names() []string {
	names := make([]string, 0, len(s.Methods))
	for n := range s.Methods {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Short returns the first sentence of the method description without the
// boilerplate "Use this method to" prefix.
func (m Method) Short() string {
	if len(m.Description) == 0 {
		return ""
	}
	d := m.Description[0]
	d = strings.TrimPrefix(d, "Use this method to ")
	d = strings.TrimPrefix(d, "Use this method ")
	if i := strings.Index(d, ". "); i > 0 {
		d = d[:i+1]
	}
	if d != "" {
		d = strings.ToUpper(d[:1]) + d[1:]
	}
	return d
}

func (f Field) TypeLabel() string {
	return strings.Join(f.Types, " | ")
}

// IsFile reports whether the field accepts an uploaded file.
func (f Field) IsFile() bool {
	for _, t := range f.Types {
		if t == "InputFile" {
			return true
		}
	}
	return false
}
