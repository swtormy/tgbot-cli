package mtp

import (
	_ "embed"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

//go:embed schema.tl
var schemaTL string

// TLField is one parameter of a TL constructor or method.
type TLField struct {
	Name     string
	Type     string // type without the flags.N? prefix, e.g. "InputPeer", "Vector<int>"
	Optional bool   // only sent when the flag bit is set
	Flags    bool   // this field is a flags word (flags:# or flags2:#)
	FlagName string // for optional fields: which flags word holds the bit
	Bit      int
}

// TLDecl is a TL constructor (an object) or a method.
type TLDecl struct {
	Name   string
	Fields []TLField
	Result string
}

type catalog struct {
	layer   string
	methods map[string]*TLDecl
	ctors   map[string]*TLDecl
	types   map[string][]string // result type -> constructor names
}

var (
	catOnce sync.Once
	cat     *catalog
)

func loadCatalog() *catalog {
	catOnce.Do(func() {
		c := &catalog{methods: map[string]*TLDecl{}, ctors: map[string]*TLDecl{}, types: map[string][]string{}}
		inFunctions := false
		for _, line := range strings.Split(schemaTL, "\n") {
			line = strings.TrimSpace(line)
			switch {
			case line == "":
				continue
			case strings.HasPrefix(line, "// LAYER"):
				f := strings.Fields(line)
				if len(f) >= 3 {
					c.layer = f[2]
				}
				continue
			case line == "---functions---":
				inFunctions = true
				continue
			case line == "---types---":
				inFunctions = false
				continue
			}
			d := parseDecl(line)
			if d == nil {
				continue
			}
			if inFunctions {
				c.methods[d.Name] = d
			} else {
				c.ctors[d.Name] = d
				c.types[d.Result] = append(c.types[d.Result], d.Name)
			}
		}
		cat = c
	})
	return cat
}

func parseDecl(line string) *TLDecl {
	line = strings.TrimSuffix(line, ";")
	head, result, ok := strings.Cut(line, " = ")
	if !ok {
		return nil
	}
	parts := strings.Fields(head)
	if len(parts) == 0 {
		return nil
	}
	name, _, _ := strings.Cut(parts[0], "#")
	d := &TLDecl{Name: name, Result: strings.TrimSpace(result)}
	for _, p := range parts[1:] {
		if strings.HasPrefix(p, "{") {
			continue
		}
		fname, typ, ok := strings.Cut(p, ":")
		if !ok {
			continue
		}
		f := TLField{Name: fname, Type: typ}
		if typ == "#" {
			f.Flags = true
		} else if i := strings.Index(typ, "?"); i > 0 && strings.Contains(typ[:i], ".") {
			f.Optional = true
			f.Type = typ[i+1:]
			flag, bit, _ := strings.Cut(typ[:i], ".")
			f.FlagName = flag
			f.Bit, _ = strconv.Atoi(bit)
		}
		d.Fields = append(d.Fields, f)
	}
	return d
}

// Layer is the MTProto API layer of the embedded schema.
func Layer() string { return loadCatalog().layer }

// MethodNames lists MTProto methods, optionally filtered by prefix.
func MethodNames(prefix string) []string {
	var out []string
	for n := range loadCatalog().methods {
		if strings.HasPrefix(n, prefix) && !strings.HasPrefix(n, "test.") {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func fieldLabel(f TLField) string {
	if f.Optional {
		return f.Type + "?"
	}
	return f.Type + "!"
}

func paramMap(d *TLDecl) map[string]string {
	m := make(map[string]string, len(d.Fields))
	for _, f := range d.Fields {
		if f.Flags {
			continue
		}
		m[f.Name] = fieldLabel(f)
	}
	return m
}

// Describe returns a compact description of a method or a TL type. Field types
// end in "!" when required and "?" when optional.
func Describe(name string) (map[string]any, error) {
	c := loadCatalog()
	if m, ok := c.methods[name]; ok {
		return map[string]any{
			"method": m.Name,
			"result": m.Result,
			"params": paramMap(m),
			"doc":    "https://core.telegram.org/method/" + m.Name,
			"note":   "Objects need an \"@type\" constructor name; use `raw describe <Type>` to list constructors. Missing numeric, string and list parameters default to 0, \"\" and []; random_id is filled in.",
		}, nil
	}
	if names, ok := c.types[name]; ok {
		cons := map[string]map[string]string{}
		for _, n := range names {
			cons[n] = paramMap(c.ctors[n])
		}
		return map[string]any{"type": name, "constructors": cons}, nil
	}
	if d, ok := c.ctors[name]; ok {
		return map[string]any{"constructor": d.Name, "type": d.Result, "params": paramMap(d)}, nil
	}
	return nil, &Error{Kind: KindUsage, Message: fmt.Sprintf("unknown method or type %q (try `tgbot user raw list`)", name)}
}

func isPrimitive(t string) bool {
	switch t {
	case "int", "long", "double", "string", "bytes", "Bool", "true", "int128", "int256":
		return true
	}
	return false
}

func vectorElem(t string) (string, bool) {
	l := strings.ToLower(t)
	if strings.HasPrefix(l, "vector<") && strings.HasSuffix(t, ">") {
		return t[len("vector<") : len(t)-1], true
	}
	return "", false
}
