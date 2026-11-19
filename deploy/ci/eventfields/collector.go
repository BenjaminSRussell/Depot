package main

import (
	"go/types"
	"reflect"
	"sort"
	"strings"
)

// collector lists the JSON paths of a Go type as encoding/json reads (decode) or writes it.
type collector struct {
	decode bool
	open   bool
	seen   map[*types.TypeName]bool
	fields []Field
}

func (c *collector) result() []Field {
	sortFields(c.fields)
	out := c.fields[:0]
	for i, f := range c.fields {
		if i == 0 || f.Path != c.fields[i-1].Path {
			out = append(out, f)
		}
	}
	if out == nil {
		out = []Field{}
	}
	return out
}

func sortFields(f []Field) { sort.Slice(f, func(i, j int) bool { return f[i].Path < f[j].Path }) }

func (c *collector) leaf(path string, omit bool) {
	c.fields = append(c.fields, Field{Path: path, Omitempty: omit})
}

// codec reports whether t marshals or unmarshals itself, which ends the path.
func (c *collector) codec(t types.Type) bool {
	names := []string{"MarshalJSON", "MarshalText"}
	if c.decode {
		names = []string{"UnmarshalJSON", "UnmarshalText"}
	}
	ms := types.NewMethodSet(types.NewPointer(t))
	for _, n := range names {
		for i := range ms.Len() {
			if ms.At(i).Obj().Name() == n {
				return true
			}
		}
	}
	return false
}

func (c *collector) typ(t types.Type, path string, omit bool) {
	t = types.Unalias(t)
	if ptr, ok := t.(*types.Pointer); ok {
		t = types.Unalias(ptr.Elem())
	}
	if c.decode && path != "" && dynamicType(t) {
		c.fields = append(c.fields, Field{Path: path, Omitempty: omit, Dynamic: true})
		return
	}
	if path != "" && c.codec(t) {
		c.leaf(path, omit)
		return
	}
	switch u := t.Underlying().(type) {
	case *types.Struct:
		if n, ok := t.(*types.Named); ok {
			if c.seen[n.Obj()] {
				if path != "" {
					c.leaf(path, omit)
				}
				return
			}
			c.seen[n.Obj()] = true
			defer delete(c.seen, n.Obj())
		}
		before := len(c.fields)
		c.structFields(u, path, omit)
		if path != "" && len(c.fields) == before {
			c.leaf(path, omit)
		}
	case *types.Slice:
		if b, ok := u.Elem().Underlying().(*types.Basic); ok && b.Kind() == types.Byte {
			c.leafOrOpen(path, omit)
			return
		}
		if path == "" {
			c.open = true
			return
		}
		c.typ(u.Elem(), path+"[]", omit)
	case *types.Array:
		if path == "" {
			c.open = true
			return
		}
		c.typ(u.Elem(), path+"[]", omit)
	default:
		c.leafOrOpen(path, omit)
	}
}

// dynamicType reports whether a decoded value of type t has no fixed shape: json.RawMessage or []byte (decoded again
// later, if at all), a map without its own UnmarshalJSON, or an interface.
func dynamicType(t types.Type) bool {
	if namedIs(t, "encoding/json", "RawMessage") {
		return true
	}
	if _, named := types.Unalias(t).(*types.Named); !named && isBytes(t) {
		return true
	}
	switch t.Underlying().(type) {
	case *types.Interface:
		return true
	case *types.Map:
		c := collector{decode: true}
		return !c.codec(t)
	}
	return false
}

func (c *collector) leafOrOpen(path string, omit bool) {
	if path == "" {
		c.open = true
		return
	}
	c.leaf(path, omit)
}

func (c *collector) structFields(st *types.Struct, prefix string, omit bool) {
	for i := range st.NumFields() {
		f := st.Field(i)
		tag := reflect.StructTag(st.Tag(i)).Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		fieldOmit := omit || strings.Contains(","+opts+",", ",omitempty,") || strings.Contains(","+opts+",", ",omitzero,")
		if f.Embedded() && name == "" {
			ft := types.Unalias(f.Type())
			if ptr, ok := ft.(*types.Pointer); ok {
				ft = types.Unalias(ptr.Elem())
			}
			if est, ok := ft.Underlying().(*types.Struct); ok {
				if n, ok := ft.(*types.Named); ok {
					if c.seen[n.Obj()] {
						continue
					}
					c.seen[n.Obj()] = true
					c.structFields(est, prefix, fieldOmit)
					delete(c.seen, n.Obj())
				} else {
					c.structFields(est, prefix, fieldOmit)
				}
				continue
			}
		}
		if !f.Exported() {
			continue
		}
		if name == "" {
			name = f.Name()
		}
		c.typ(f.Type(), joinPath(prefix, name), fieldOmit)
	}
}
