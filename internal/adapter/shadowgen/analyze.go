package shadowgen

import (
	"fmt"
	"go/types"
	"sort"
	"strings"
)

// analysis is the full plan for one shadow package.
type analysis struct {
	report    Report
	funcs     []Func            // shadowed functions
	passFuncs []Func            // exported but always call the original
	types     []*types.TypeName // re-exported as aliases
	consts    []*types.Const    // re-exported
	risky     map[string]bool   // shadowed functions whose arguments are shared
	orig      *types.Package
	rew       *types.Package
}

var errorType = types.Universe.Lookup("error").Type()

// analyze lists every exported top-level name of orig and decides what the shadow package does with it.
func analyze(orig, rew *types.Package) *analysis {
	a := &analysis{orig: orig, rew: rew, risky: map[string]bool{}}
	a.report.Package = orig.Name()
	a.report.EnvVar = "GA_SHADOW_" + strings.ToUpper(orig.Name())
	scope := orig.Scope()
	for _, name := range scope.Names() { // sorted
		obj := scope.Lookup(name)
		if !obj.Exported() {
			continue
		}
		switch o := obj.(type) {
		case *types.Func:
			a.addFunc(o)
		case *types.TypeName:
			a.addType(o)
		case *types.Const:
			a.consts = append(a.consts, o)
			a.report.Reexported = append(a.report.Reexported, "const "+name)
		case *types.Var:
			a.skip(name, "package variable; a copy would not follow the original", false)
		}
	}
	sort.Slice(a.report.NotShadowed, func(i, j int) bool { return a.report.NotShadowed[i].Name < a.report.NotShadowed[j].Name })
	return a
}

func (a *analysis) skip(name, reason string, passthrough bool) {
	a.report.NotShadowed = append(a.report.NotShadowed, Skip{Name: name, Reason: reason, Passthrough: passthrough})
}

func (a *analysis) addType(tn *types.TypeName) {
	name := tn.Name()
	if hasTypeParams(tn.Type()) {
		a.skip(name, "generic type; not re-exported", false)
		return
	}
	a.types = append(a.types, tn)
	a.report.Reexported = append(a.report.Reexported, "type "+name)
	named, ok := types.Unalias(tn.Type()).(*types.Named)
	if !ok || tn.IsAlias() || types.IsInterface(named) {
		return
	}
	for i := 0; i < named.NumMethods(); i++ {
		if m := named.Method(i); m.Exported() {
			a.skip(name+"."+m.Name(), "method; it comes with the re-exported type and always runs the original", true)
		}
	}
}

func hasTypeParams(t types.Type) bool {
	switch t := t.(type) {
	case *types.Named:
		return t.TypeParams().Len() > 0 && t.TypeArgs().Len() == 0
	case *types.Alias:
		return t.TypeParams().Len() > 0 && t.TypeArgs().Len() == 0
	}
	return false
}

func (a *analysis) addFunc(fn *types.Func) {
	name := fn.Name()
	sig := fn.Type().(*types.Signature)
	if sig.TypeParams().Len() > 0 {
		a.skip(name, "generic function; not part of the shadow package", false)
		return
	}
	if bad := unexportedIn(sig); bad != "" {
		a.skip(name, "signature uses unexported type "+bad+"; not part of the shadow package", false)
		return
	}
	f := describe(fn)

	// Reasons that keep the function in the package but always call the original.
	for _, p := range f.Params {
		if kind := sideEffectKind(p.Type, map[types.Type]bool{}); kind != "" {
			a.passFuncs = append(a.passFuncs, f)
			a.skip(name, fmt.Sprintf("parameter %s holds a %s; calling side effects twice is unsafe; always calls the original", p.Name, kind), true)
			return
		}
	}
	for i, r := range f.Results {
		if kind := sideEffectKind(r.Type, map[types.Type]bool{}); kind != "" {
			a.passFuncs = append(a.passFuncs, f)
			a.skip(name, fmt.Sprintf("result %d holds a %s value that cannot be compared; always calls the original", i, kind), true)
			return
		}
	}
	obj := a.rew.Scope().Lookup(name)
	if obj == nil {
		a.passFuncs = append(a.passFuncs, f)
		a.skip(name, "missing in the rewrite; always calls the original", true)
		return
	}
	rf, ok := obj.(*types.Func)
	if !ok {
		a.passFuncs = append(a.passFuncs, f)
		a.skip(name, fmt.Sprintf("the rewrite's %s is not a function; always calls the original", name), true)
		return
	}
	if !types.Identical(sig, rf.Type()) {
		a.passFuncs = append(a.passFuncs, f)
		a.skip(name, fmt.Sprintf("signature mismatch: original %s, rewrite %s; always calls the original",
			types.TypeString(sig, nil), types.TypeString(rf.Type(), nil)), true)
		return
	}
	var shared []string
	for _, p := range f.Params {
		if kind := sharedKind(p.Type, map[types.Type]bool{}); kind != "" {
			shared = append(shared, fmt.Sprintf("parameter %s is a %s", p.Name, kind))
		}
	}
	if len(shared) > 0 {
		a.risky[name] = true
		a.report.Risks = append(a.report.Risks, Risk{Name: name,
			Reason: strings.Join(shared, ", ") + "; both calls share it, so a mutation or read runs twice and the caller sees the rewrite's mutation"})
	}
	a.funcs = append(a.funcs, f)
	a.report.Shadowed = append(a.report.Shadowed, name)
}

// describe turns a go/types function into the Func data shape with wrapper-safe parameter names.
func describe(fn *types.Func) Func {
	sig := fn.Type().(*types.Signature)
	f := Func{Name: fn.Name(), Signature: sig, Variadic: sig.Variadic()}
	used := map[string]bool{}
	for i := 0; i < sig.Params().Len(); i++ {
		used[sig.Params().At(i).Name()] = true
	}
	for i := 0; i < sig.Params().Len(); i++ {
		v := sig.Params().At(i)
		name := v.Name()
		if name == "" || name == "_" || strings.HasPrefix(name, "ga") {
			name = freshName(used, i)
		}
		f.Params = append(f.Params, Param{Name: name, Type: v.Type()})
	}
	for i := 0; i < sig.Results().Len(); i++ {
		t := sig.Results().At(i).Type()
		f.Results = append(f.Results, Result{Type: t, IsError: types.Identical(t, errorType)})
	}
	if n := len(f.Results); n > 0 {
		f.LastError = f.Results[n-1].IsError
	}
	return f
}

func freshName(used map[string]bool, i int) string {
	for n := i; ; n++ {
		name := fmt.Sprintf("arg%d", n)
		if !used[name] {
			used[name] = true
			return name
		}
	}
}

// sideEffectKind reports "func" or "channel" if t holds such a value anywhere outside an interface.
func sideEffectKind(t types.Type, seen map[types.Type]bool) string {
	if seen[t] {
		return ""
	}
	seen[t] = true
	switch u := types.Unalias(t).(type) {
	case *types.Named:
		return sideEffectKind(u.Underlying(), seen)
	case *types.Signature:
		return "func"
	case *types.Chan:
		return "channel"
	case *types.Pointer:
		return sideEffectKind(u.Elem(), seen)
	case *types.Slice:
		return sideEffectKind(u.Elem(), seen)
	case *types.Array:
		return sideEffectKind(u.Elem(), seen)
	case *types.Map:
		if k := sideEffectKind(u.Key(), seen); k != "" {
			return k
		}
		return sideEffectKind(u.Elem(), seen)
	case *types.Struct:
		for i := 0; i < u.NumFields(); i++ {
			if k := sideEffectKind(u.Field(i).Type(), seen); k != "" {
				return k
			}
		}
	}
	return ""
}

// sharedKind reports the first kind of reference in t through which both calls see the same memory.
func sharedKind(t types.Type, seen map[types.Type]bool) string {
	if seen[t] {
		return ""
	}
	seen[t] = true
	switch u := types.Unalias(t).(type) {
	case *types.Named:
		return sharedKind(u.Underlying(), seen)
	case *types.Pointer:
		return "pointer"
	case *types.Slice:
		return "slice"
	case *types.Map:
		return "map"
	case *types.Interface:
		return "interface"
	case *types.Basic:
		if u.Kind() == types.UnsafePointer {
			return "unsafe pointer"
		}
	case *types.Array:
		return sharedKind(u.Elem(), seen)
	case *types.Struct:
		for i := 0; i < u.NumFields(); i++ {
			if k := sharedKind(u.Field(i).Type(), seen); k != "" {
				return "struct holding a " + k
			}
		}
	}
	return ""
}

// unexportedIn returns the name of a type in t that another package cannot write, or "".
func unexportedIn(t types.Type) string {
	seen := map[types.Type]bool{}
	var walk func(t types.Type) string
	walk = func(t types.Type) string {
		if t == nil || seen[t] {
			return ""
		}
		seen[t] = true
		switch u := t.(type) {
		case *types.Alias:
			if u.Obj().Pkg() != nil && !u.Obj().Exported() {
				return u.Obj().Name()
			}
			for i := 0; i < u.TypeArgs().Len(); i++ {
				if b := walk(u.TypeArgs().At(i)); b != "" {
					return b
				}
			}
		case *types.Named:
			if u.Obj().Pkg() != nil && !u.Obj().Exported() {
				return u.Obj().Name()
			}
			for i := 0; i < u.TypeArgs().Len(); i++ {
				if b := walk(u.TypeArgs().At(i)); b != "" {
					return b
				}
			}
		case *types.Pointer:
			return walk(u.Elem())
		case *types.Slice:
			return walk(u.Elem())
		case *types.Array:
			return walk(u.Elem())
		case *types.Chan:
			return walk(u.Elem())
		case *types.Map:
			if b := walk(u.Key()); b != "" {
				return b
			}
			return walk(u.Elem())
		case *types.Signature:
			if b := walk(u.Params()); b != "" {
				return b
			}
			return walk(u.Results())
		case *types.Tuple:
			for i := 0; i < u.Len(); i++ {
				if b := walk(u.At(i).Type()); b != "" {
					return b
				}
			}
		case *types.Struct:
			for i := 0; i < u.NumFields(); i++ {
				f := u.Field(i)
				if !f.Exported() && f.Pkg() != nil {
					return "struct field " + f.Name()
				}
				if b := walk(f.Type()); b != "" {
					return b
				}
			}
		case *types.Interface:
			for i := 0; i < u.NumExplicitMethods(); i++ {
				m := u.ExplicitMethod(i)
				if !m.Exported() {
					return "interface method " + m.Name()
				}
				if b := walk(m.Type()); b != "" {
					return b
				}
			}
			for i := 0; i < u.NumEmbeddeds(); i++ {
				if b := walk(u.EmbeddedType(i)); b != "" {
					return b
				}
			}
		}
		return ""
	}
	return walk(t)
}
