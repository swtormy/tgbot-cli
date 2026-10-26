package mtp

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

// The raw escape hatch speaks TL directly, driven by the embedded schema, so
// any of the ~800 methods works without hand-written bindings. Constructor ids
// come from gotd's registry.

var (
	idsOnce sync.Once
	nameID  map[string]uint32
	idName  map[uint32]string
)

func loadIDs() {
	idsOnce.Do(func() {
		nameID = tg.NamesMap()
		idName = make(map[uint32]string, len(nameID))
		for n, id := range nameID {
			idName[id] = n
		}
	})
}

const gzipPackedID = 0x3072cfa1

type tlRequest struct {
	decl   *TLDecl
	params map[string]any
}

func (r *tlRequest) Encode(b *bin.Buffer) error {
	loadIDs()
	id, ok := nameID[r.decl.Name]
	if !ok {
		return fmt.Errorf("no constructor id for %s", r.decl.Name)
	}
	b.PutID(id)
	return encodeFields(b, r.decl, r.params, r.decl.Name)
}

func present(v any, ok bool) bool { return ok && v != nil }

func encodeFields(b *bin.Buffer, d *TLDecl, obj map[string]any, path string) error {
	known := map[string]bool{"@type": true}
	for _, f := range d.Fields {
		if !f.Flags {
			known[f.Name] = true
		}
	}
	var unknown []string
	for k := range obj {
		if !known[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		var valid []string
		for _, f := range d.Fields {
			if !f.Flags {
				valid = append(valid, f.Name)
			}
		}
		return usagef("%s: unknown parameter %s (valid: %s)", path, strings.Join(unknown, ", "), strings.Join(valid, ", "))
	}
	flags := map[string]uint32{}
	for _, f := range d.Fields {
		if !f.Optional {
			continue
		}
		v, ok := obj[f.Name]
		if !present(v, ok) {
			continue
		}
		if f.Type == "true" {
			if t, _ := v.(bool); !t {
				continue
			}
		}
		flags[f.FlagName] |= 1 << uint(f.Bit)
	}
	for _, f := range d.Fields {
		if f.Flags {
			b.PutUint32(flags[f.Name])
			continue
		}
		v, ok := obj[f.Name]
		has := present(v, ok)
		if f.Optional {
			if !has || f.Type == "true" {
				continue
			}
		} else if !has {
			if f.Name == "random_id" && f.Type == "long" {
				var r [8]byte
				_, _ = rand.Read(r[:])
				b.PutLong(int64(binary.LittleEndian.Uint64(r[:])))
				continue
			}
			def, err := defaultValue(f.Type)
			if err != nil {
				return usagef("%s: missing required parameter %q (%s)", path, f.Name, f.Type)
			}
			v = def
		}
		if err := encodeValue(b, f.Type, v, path+"."+f.Name); err != nil {
			return err
		}
	}
	return nil
}

func defaultValue(t string) (any, error) {
	switch t {
	case "int", "long", "double":
		return json.Number("0"), nil
	case "string":
		return "", nil
	case "bytes":
		return "", nil
	case "Bool":
		return false, nil
	case "int128":
		return strings.Repeat("00", 16), nil
	case "int256":
		return strings.Repeat("00", 32), nil
	}
	if _, ok := vectorElem(t); ok {
		return []any{}, nil
	}
	return nil, fmt.Errorf("no default")
}

func asInt64(v any, path string) (int64, error) {
	switch n := v.(type) {
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i, nil
		}
		return 0, usagef("%s: %s is not an integer", path, n)
	case string:
		var i int64
		if _, err := fmt.Sscanf(n, "%d", &i); err == nil {
			return i, nil
		}
	case float64:
		if n == math.Trunc(n) {
			return int64(n), nil
		}
	case int:
		return int64(n), nil
	case int64:
		return n, nil
	}
	return 0, usagef("%s: expected a number, got %v", path, v)
}

func encodeValue(b *bin.Buffer, typ string, v any, path string) error {
	switch typ {
	case "int":
		i, err := asInt64(v, path)
		if err != nil {
			return err
		}
		if i < math.MinInt32 || i > math.MaxUint32 {
			return usagef("%s: %d does not fit in int32", path, i)
		}
		b.PutUint32(uint32(i))
	case "long":
		i, err := asInt64(v, path)
		if err != nil {
			return err
		}
		b.PutLong(i)
	case "double":
		n, ok := v.(json.Number)
		if !ok {
			return usagef("%s: expected a number", path)
		}
		f, err := n.Float64()
		if err != nil {
			return usagef("%s: %v", path, err)
		}
		b.PutDouble(f)
	case "string":
		s, ok := v.(string)
		if !ok {
			return usagef("%s: expected a string", path)
		}
		b.PutString(s)
	case "bytes":
		s, ok := v.(string)
		if !ok {
			return usagef("%s: expected a base64 string", path)
		}
		raw, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			raw, err = base64.RawStdEncoding.DecodeString(s)
		}
		if err != nil {
			return usagef("%s: bytes must be base64: %v", path, err)
		}
		b.PutBytes(raw)
	case "Bool":
		t, ok := v.(bool)
		if !ok {
			return usagef("%s: expected true or false", path)
		}
		b.PutBool(t)
	case "int128", "int256":
		s, ok := v.(string)
		want := 16
		if typ == "int256" {
			want = 32
		}
		raw, err := hex.DecodeString(s)
		if !ok || err != nil || len(raw) != want {
			return usagef("%s: expected %d hex bytes", path, want)
		}
		b.Put(raw)
	default:
		if el, ok := vectorElem(typ); ok {
			arr, ok := v.([]any)
			if !ok {
				return usagef("%s: expected an array", path)
			}
			b.PutVectorHeader(len(arr))
			for i, x := range arr {
				if err := encodeValue(b, el, x, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
			return nil
		}
		return encodeObject(b, typ, v, path)
	}
	return nil
}

func encodeObject(b *bin.Buffer, typ string, v any, path string) error {
	loadIDs()
	c := loadCatalog()
	obj, ok := v.(map[string]any)
	if !ok {
		return usagef("%s: expected an object of type %s (with \"@type\"); see `tgbot user raw describe %s`", path, typ, typ)
	}
	name, _ := obj["@type"].(string)
	if name == "" {
		if cs := c.types[typ]; len(cs) == 1 {
			name = cs[0]
		} else {
			return usagef("%s: missing \"@type\" (one of %s)", path, strings.Join(c.types[typ], ", "))
		}
	}
	d, ok := c.ctors[name]
	if !ok {
		return usagef("%s: unknown constructor %q", path, name)
	}
	if d.Result != typ {
		return usagef("%s: %q is a %s, but %s is expected", path, name, d.Result, typ)
	}
	b.PutID(nameID[name])
	return encodeFields(b, d, obj, path)
}

// tlResult decodes whatever the method returns into plain JSON-able values.
type tlResult struct {
	typ string
	out any
}

func (r *tlResult) Decode(b *bin.Buffer) error {
	loadIDs()
	v, err := decodeValue(b, r.typ)
	r.out = v
	return err
}

func decodeValue(b *bin.Buffer, typ string) (any, error) {
	switch typ {
	case "int":
		v, err := b.Int32()
		return int64(v), err
	case "long":
		return b.Long()
	case "double":
		return b.Double()
	case "string":
		return b.String()
	case "bytes":
		raw, err := b.Bytes()
		return base64.StdEncoding.EncodeToString(raw), err
	case "Bool":
		return b.Bool()
	case "int128":
		v, err := b.Int128()
		return hex.EncodeToString(v[:]), err
	case "int256":
		v, err := b.Int256()
		return hex.EncodeToString(v[:]), err
	}
	if el, ok := vectorElem(typ); ok {
		n, err := b.VectorHeader()
		if err != nil {
			return nil, err
		}
		out := make([]any, 0, n)
		for i := 0; i < n; i++ {
			x, err := decodeValue(b, el)
			if err != nil {
				return nil, err
			}
			out = append(out, x)
		}
		return out, nil
	}
	id, err := b.ID()
	if err != nil {
		return nil, err
	}
	if id == gzipPackedID {
		raw, err := b.Bytes()
		if err != nil {
			return nil, err
		}
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		plain, err := io.ReadAll(zr)
		if err != nil {
			return nil, err
		}
		return decodeValue(&bin.Buffer{Buf: plain}, typ)
	}
	if id == bin.TypeVector {
		return nil, fmt.Errorf("unexpected vector where %s was expected", typ)
	}
	name, ok := idName[id]
	if !ok {
		return nil, fmt.Errorf("unknown constructor 0x%x in %s (the schema layer is %s)", id, typ, Layer())
	}
	switch name {
	case "boolTrue", "true":
		return true, nil
	case "boolFalse":
		return false, nil
	case "null":
		return nil, nil
	}
	d, ok := loadCatalog().ctors[name]
	if !ok {
		return nil, fmt.Errorf("constructor %s is not in the embedded schema", name)
	}
	out := map[string]any{"@type": name}
	flags := map[string]uint32{}
	for _, f := range d.Fields {
		if f.Flags {
			v, err := b.Uint32()
			if err != nil {
				return nil, err
			}
			flags[f.Name] = v
			continue
		}
		if f.Optional {
			set := flags[f.FlagName]&(1<<uint(f.Bit)) != 0
			if f.Type == "true" {
				if set {
					out[f.Name] = true
				}
				continue
			}
			if !set {
				continue
			}
		}
		v, err := decodeValue(b, f.Type)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", name, f.Name, err)
		}
		out[f.Name] = v
	}
	return out, nil
}
