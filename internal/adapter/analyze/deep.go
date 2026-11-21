package analyze

import (
	"errors"
	"fmt"
	"go/ast"
	"go/importer"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/go-anything/go-anything/internal/domain"
)

// maxTypeErrors bounds the errors kept per package; one missing import can cause hundreds.
const maxTypeErrors = 20

// checked is the type checker's result for one module package.
type checked struct {
	src   *source
	pkg   *types.Package
	info  *types.Info
	errs  []string
	extra int // errors beyond maxTypeErrors
	state int // 0 not started, 1 in progress, 2 done
}

// moduleImporter type-checks packages of the analyzed module from the files already parsed,
// so that every module package is checked once and shares its objects with its importers.
// Dependencies resolved by one go list call are checked from their listed files; anything
// else goes to the "source" importer.
type moduleImporter struct {
	module   string
	root     string
	fset     *token.FileSet
	byPath   map[string]*checked
	deps     map[string]*listedPkg
	fallback types.ImporterFrom
}

func (m *moduleImporter) Import(path string) (*types.Package, error) {
	return m.ImportFrom(path, m.root, 0)
}

func (m *moduleImporter) ImportFrom(path, dir string, mode types.ImportMode) (*types.Package, error) {
	if path == "unsafe" {
		return types.Unsafe, nil
	}
	if c, ok := m.byPath[path]; ok {
		return m.check(c)
	}
	if inModule(m.module, path) {
		return nil, fmt.Errorf("package %s is not in the analyzed tree", path)
	}
	if p, ok := m.deps[path]; ok {
		return m.checkDep(p)
	}
	if m.fallback == nil {
		return nil, errors.New("source importer unavailable")
	}
	return m.fallback.ImportFrom(path, dir, mode)
}

// check type-checks one module package once. Errors are recorded on the package, never returned
// as a failure of the whole analysis; a partially checked package is still usable by importers.
func (m *moduleImporter) check(c *checked) (*types.Package, error) {
	switch c.state {
	case 2:
		return c.pkg, nil
	case 1:
		return nil, fmt.Errorf("import cycle through %s", c.src.pkg.ImportPath)
	}
	c.state = 1
	defer func() { c.state = 2 }()
	c.info = &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	if len(c.src.files) == 0 { // every file is excluded by build constraints; see BuildIgnored
		return nil, fmt.Errorf("no buildable files in %s", c.src.pkg.ImportPath)
	}
	conf := types.Config{
		Importer:    m,
		FakeImportC: true, // cgo is reported as a risk; its C side cannot be checked here
		Error: func(err error) {
			if len(c.errs) >= maxTypeErrors {
				c.extra++
				return
			}
			var te types.Error
			if errors.As(err, &te) {
				c.errs = append(c.errs, posString(m.fset, m.root, te.Pos)+": "+te.Msg)
				return
			}
			c.errs = append(c.errs, err.Error())
		},
	}
	pkg, err := safeCheck(&conf, c.src.pkg.ImportPath, m.fset, c.src.files, c.info)
	var panicked *checkerPanic
	if errors.As(err, &panicked) {
		c.errs = append(c.errs, panicked.Error())
	} // other errors already went to conf.Error
	c.pkg = pkg
	return pkg, nil
}

// checkerPanic is a panic inside go/types, turned into an error so one package cannot abort
// the whole analysis.
type checkerPanic struct{ value any }

func (p *checkerPanic) Error() string { return fmt.Sprintf("type checker panic: %v", p.value) }

func safeCheck(conf *types.Config, path string, fset *token.FileSet, files []*ast.File, info *types.Info) (pkg *types.Package, err error) {
	defer func() {
		if r := recover(); r != nil {
			pkg, err = nil, &checkerPanic{value: r}
		}
	}()
	return conf.Check(path, fset, files, info)
}

// deepen type-checks every package and fills the function level fields of the analysis.
func deepen(out *domain.Analysis, root, module string, fset *token.FileSet, srcs []*source) {
	imp := &moduleImporter{module: module, root: root, fset: fset, byPath: map[string]*checked{}}
	if fb, ok := importer.ForCompiler(fset, "source", nil).(types.ImporterFrom); ok {
		imp.fallback = fb
	}
	seen := map[string]bool{}
	var external []string
	for _, s := range srcs {
		for _, path := range s.pkg.Imports {
			if !inModule(module, path) && !seen[path] {
				seen[path] = true
				external = append(external, path)
			}
		}
	}
	sort.Strings(external)
	if deps, err := listDeps(root, external); err == nil {
		imp.deps = deps
	} // else every dependency goes through the "source" importer: slower, same result
	all := make([]*checked, 0, len(srcs))
	for _, s := range srcs {
		c := &checked{src: s}
		imp.byPath[s.pkg.ImportPath] = c
		all = append(all, c)
	}
	for _, c := range all {
		_, _ = imp.check(c) // failures are kept on c.errs
	}

	w := &walker{
		module: module, root: root, fset: fset,
		callers:     map[string]map[string]bool{},
		refs:        map[string]int{},
		globals:     map[*types.Var]assignSite{},
		calleeIdent: map[*ast.Ident]bool{},
	}
	for _, c := range all {
		w.walkPackage(c)
	}
	for _, c := range all {
		w.countRefs(c)
	}
	w.globalRisks(all)

	for _, c := range all {
		p := c.src.pkg
		if c.extra > 0 {
			c.errs = append(c.errs, fmt.Sprintf("and %d more", c.extra))
		}
		p.TypeErrors = c.errs
		for i := range p.Functions {
			f := &p.Functions[i]
			id := domain.FuncID(p.ImportPath, f.Receiver, f.Name)
			f.Callers = len(w.callers[id])
			f.Refs = w.refs[id]
			out.CallEdges += len(f.Callees)
			out.DynamicCalls += f.DynamicCalls
		}
		sort.SliceStable(p.RiskSites, func(i, j int) bool { return domain.PosLess(p.RiskSites[i].Pos, p.RiskSites[j].Pos) })
		out.Packages = append(out.Packages, *p)
	}
	out.Unreached = domain.UnreachedFuncs(*out)
	out.StartHere = domain.StartHere(*out)
}

// assignSite is the first place a package-level variable is assigned inside a function.
type assignSite struct {
	pos  token.Pos
	fn   string
	decl token.Pos
}

// walker collects the module-wide relations while visiting each package.
type walker struct {
	module string
	root   string
	fset   *token.FileSet
	// callers maps a callee FuncID to the set of distinct caller FuncIDs.
	callers map[string]map[string]bool
	// refs counts uses of a module function as a value.
	refs map[string]int
	// globals are module package-level variables assigned in a function other than init.
	globals map[*types.Var]assignSite
	// calleeIdent marks identifiers that name the callee of a static call, so they are not refs.
	calleeIdent map[*ast.Ident]bool
}

func (w *walker) walkPackage(c *checked) {
	p := c.src.pkg
	methods := map[string][]string{} // receiver type name -> method names
	for _, f := range c.src.files {
		if p.Name == "" {
			p.Name = f.Name.Name
		}
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv != nil && len(fn.Recv.List) > 0 {
				r := receiverName(fn)
				methods[r] = append(methods[r], fn.Name.Name)
			}
		}
	}
	for _, f := range c.src.files {
		for _, spec := range f.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				continue
			}
			switch path {
			case "unsafe":
				w.risk(p, domain.RiskUnsafe, spec.Pos(), "imports unsafe")
			case "C":
				w.risk(p, domain.RiskCgo, spec.Pos(), "imports C (cgo)")
			case "reflect":
				w.risk(p, domain.RiskReflect, spec.Pos(), "imports reflect")
			}
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.GenDecl:
				if d.Tok == token.TYPE {
					for _, s := range d.Specs {
						p.Types = append(p.Types, w.typeInfo(s.(*ast.TypeSpec), methods))
					}
				}
			case *ast.FuncDecl:
				p.Functions = append(p.Functions, w.funcInfo(c, d))
			}
		}
	}
}

func (w *walker) typeInfo(s *ast.TypeSpec, methods map[string][]string) domain.TypeInfo {
	t := domain.TypeInfo{Name: s.Name.Name, Pos: posString(w.fset, w.root, s.Pos())}
	switch it := s.Type.(type) {
	case *ast.StructType:
		t.Kind = "struct"
	case *ast.InterfaceType:
		t.Kind = "interface"
		for _, m := range it.Methods.List {
			for _, n := range m.Names {
				t.Methods = append(t.Methods, n.Name)
			}
		}
	default:
		t.Kind = "named"
	}
	if s.Assign.IsValid() {
		t.Kind = "alias"
	}
	if t.Kind != "interface" {
		t.Methods = append(t.Methods, methods[s.Name.Name]...)
	}
	sort.Strings(t.Methods)
	return t
}

func (w *walker) funcInfo(c *checked, fn *ast.FuncDecl) domain.FuncInfo {
	p := c.src.pkg
	f := domain.FuncInfo{Name: fn.Name.Name, Pos: posString(w.fset, w.root, fn.Pos()), Exported: fn.Name.IsExported()}
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		r := receiverName(fn)
		f.Exported = f.Exported && ast.IsExported(r)
		if _, ptr := fn.Recv.List[0].Type.(*ast.StarExpr); ptr {
			r = "*" + r
		}
		f.Receiver = r
	}
	if obj, ok := c.info.Defs[fn.Name].(*types.Func); ok && c.pkg != nil {
		sig := obj.Type().(*types.Signature)
		f.Signature = types.TypeString(sig, types.RelativeTo(c.pkg))
		f.PlainSignature = plainSignature(sig, w.module)
	}
	if fn.Recv == nil && fn.Name.Name == "init" {
		w.risk(p, domain.RiskInit, fn.Pos(), "func init")
	}
	if fn.Body == nil {
		return f
	}
	self := domain.FuncID(p.ImportPath, f.Receiver, f.Name)
	isInit := fn.Recv == nil && fn.Name.Name == "init"
	callees := map[string]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.GoStmt:
			f.GoStatements++
			w.risk(p, domain.RiskGoroutine, n.Pos(), "go statement in "+displayName(f))
		case *ast.CallExpr:
			callee, ident, dynamic := resolveCall(c.info, n)
			if dynamic {
				f.DynamicCalls++
			}
			if callee != nil && callee.Pkg() != nil && inModule(w.module, callee.Pkg().Path()) {
				w.calleeIdent[ident] = true
				id := funcKey(callee)
				if id != self {
					callees[id] = true
					if w.callers[id] == nil {
						w.callers[id] = map[string]bool{}
					}
					w.callers[id][self] = true
				}
			}
		case *ast.AssignStmt:
			if n.Tok != token.DEFINE && !isInit {
				for _, lhs := range n.Lhs {
					w.noteAssign(c.info, lhs, displayName(f))
				}
			}
		case *ast.IncDecStmt:
			if !isInit {
				w.noteAssign(c.info, n.X, displayName(f))
			}
		case *ast.RangeStmt:
			if n.Tok == token.ASSIGN && !isInit {
				w.noteAssign(c.info, n.Key, displayName(f))
				w.noteAssign(c.info, n.Value, displayName(f))
			}
		}
		return true
	})
	for id := range callees {
		f.Callees = append(f.Callees, id)
	}
	sort.Strings(f.Callees)
	return f
}

// resolveCall finds the function a call statically invokes. dynamic is true when the call goes
// through an interface method, a function-typed variable or field, or another computed value.
// Conversions, builtins and calls the type checker could not resolve are neither.
func resolveCall(info *types.Info, call *ast.CallExpr) (callee *types.Func, ident *ast.Ident, dynamic bool) {
	fun := ast.Unparen(call.Fun)
	if tv, ok := info.Types[fun]; ok && tv.IsType() {
		return nil, nil, false // conversion
	}
	switch e := fun.(type) {
	case *ast.IndexExpr: // explicit instantiation f[T](...), or indexing a slice or map of funcs
		if fn, id := resolveName(info, e.X); fn != nil {
			return staticOrInterface(fn, id)
		}
		return nil, nil, isFuncValue(info, fun)
	case *ast.IndexListExpr:
		if fn, id := resolveName(info, e.X); fn != nil {
			return staticOrInterface(fn, id)
		}
		return nil, nil, isFuncValue(info, fun)
	case *ast.FuncLit:
		return nil, nil, false // its body is walked as part of the enclosing function
	case *ast.Ident, *ast.SelectorExpr:
		if fn, id := resolveName(info, e); fn != nil {
			return staticOrInterface(fn, id)
		}
		return nil, nil, isFuncValue(info, fun)
	}
	return nil, nil, isFuncValue(info, fun)
}

// resolveName returns the *types.Func an identifier or selector names, if it names one.
func resolveName(info *types.Info, e ast.Expr) (*types.Func, *ast.Ident) {
	switch e := ast.Unparen(e).(type) {
	case *ast.Ident:
		if fn, ok := info.Uses[e].(*types.Func); ok {
			return fn, e
		}
	case *ast.SelectorExpr:
		if sel, ok := info.Selections[e]; ok {
			if sel.Kind() == types.FieldVal {
				return nil, nil // a func-typed field
			}
			if fn, ok := sel.Obj().(*types.Func); ok {
				return fn, e.Sel
			}
			return nil, nil
		}
		if fn, ok := info.Uses[e.Sel].(*types.Func); ok { // qualified pkg.F
			return fn, e.Sel
		}
	}
	return nil, nil
}

func staticOrInterface(fn *types.Func, id *ast.Ident) (*types.Func, *ast.Ident, bool) {
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil && types.IsInterface(sig.Recv().Type()) {
		return nil, nil, true // interface method or type parameter method
	}
	return fn.Origin(), id, false
}

// isFuncValue reports whether the called expression is a value of function type the checker knows.
func isFuncValue(info *types.Info, e ast.Expr) bool {
	tv, ok := info.Types[e]
	if !ok || tv.Type == nil || tv.IsBuiltin() {
		return false
	}
	_, ok = tv.Type.Underlying().(*types.Signature)
	return ok
}

// funcKey is the domain.FuncID of a function object.
func funcKey(fn *types.Func) string {
	path := fn.Pkg().Path()
	sig, _ := fn.Type().(*types.Signature)
	if sig == nil || sig.Recv() == nil {
		return domain.FuncID(path, "", fn.Name())
	}
	t := sig.Recv().Type()
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	if n, ok := types.Unalias(t).(*types.Named); ok {
		return domain.FuncID(path, n.Obj().Name(), fn.Name())
	}
	return domain.FuncID(path, "?", fn.Name())
}

// noteAssign records lhs when it writes to a module package-level variable, directly or
// through a field, index or pointer of it.
func (w *walker) noteAssign(info *types.Info, lhs ast.Expr, fn string) {
	v := rootVar(info, lhs)
	if v == nil || v.Pkg() == nil || v.Parent() != v.Pkg().Scope() || !inModule(w.module, v.Pkg().Path()) {
		return
	}
	if _, seen := w.globals[v]; !seen {
		w.globals[v] = assignSite{pos: lhs.Pos(), fn: fn, decl: v.Pos()}
	}
}

func rootVar(info *types.Info, e ast.Expr) *types.Var {
	for {
		switch x := e.(type) {
		case nil:
			return nil
		case *ast.Ident:
			if x.Name == "_" {
				return nil
			}
			v, _ := info.Uses[x].(*types.Var)
			return v
		case *ast.ParenExpr:
			e = x.X
		case *ast.StarExpr:
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.SelectorExpr:
			if _, isField := info.Selections[x]; isField {
				e = x.X
				continue
			}
			v, _ := info.Uses[x.Sel].(*types.Var) // qualified pkg.V
			return v
		default:
			return nil
		}
	}
}

// globalRisks turns the assigned package variables into risk sites on the declaring package.
func (w *walker) globalRisks(all []*checked) {
	byPath := map[string]*domain.Package{}
	for _, c := range all {
		byPath[c.src.pkg.ImportPath] = c.src.pkg
	}
	for v, a := range w.globals {
		p := byPath[v.Pkg().Path()]
		if p == nil {
			continue
		}
		w.risk(p, domain.RiskGlobalMutable, a.decl,
			fmt.Sprintf("var %s assigned in %s at %s", v.Name(), a.fn, posString(w.fset, w.root, a.pos)))
	}
}

// countRefs counts uses of module functions that are not the callee of a static call.
func (w *walker) countRefs(c *checked) {
	for id, obj := range c.info.Uses {
		fn, ok := obj.(*types.Func)
		if !ok || fn.Pkg() == nil || !inModule(w.module, fn.Pkg().Path()) || w.calleeIdent[id] {
			continue
		}
		if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil && types.IsInterface(sig.Recv().Type()) {
			continue
		}
		w.refs[funcKey(fn.Origin())]++
	}
}

func (w *walker) risk(p *domain.Package, kind string, pos token.Pos, detail string) {
	p.RiskSites = append(p.RiskSites, domain.RiskSite{Kind: kind, Pos: posString(w.fset, w.root, pos), Detail: detail})
}

func displayName(f domain.FuncInfo) string {
	if f.Receiver == "" {
		return f.Name
	}
	return "(" + f.Receiver + ")." + f.Name
}

// plainSignature reports whether a function is easy to port by its shape alone: no type
// parameters, and every receiver, parameter and result is a plain type.
func plainSignature(sig *types.Signature, module string) bool {
	if sig.TypeParams().Len() > 0 || sig.RecvTypeParams().Len() > 0 {
		return false
	}
	if r := sig.Recv(); r != nil && !plainType(r.Type(), module) {
		return false
	}
	for _, tuple := range []*types.Tuple{sig.Params(), sig.Results()} {
		for i := range tuple.Len() {
			if !plainType(tuple.At(i).Type(), module) {
				return false
			}
		}
	}
	return true
}

var errorType = types.Universe.Lookup("error").Type()

// plainType accepts basic types, error, module-local types whose underlying type is a struct
// or basic type, and pointers, slices, arrays and maps of those.
func plainType(t types.Type, module string) bool {
	t = types.Unalias(t)
	if types.Identical(t, errorType) {
		return true
	}
	switch u := t.(type) {
	case *types.Basic:
		return u.Kind() != types.UnsafePointer && u.Kind() != types.Invalid
	case *types.Pointer:
		return plainType(u.Elem(), module)
	case *types.Slice:
		return plainType(u.Elem(), module)
	case *types.Array:
		return plainType(u.Elem(), module)
	case *types.Map:
		return plainType(u.Key(), module) && plainType(u.Elem(), module)
	case *types.Named:
		obj := u.Obj()
		if obj.Pkg() == nil || !inModule(module, obj.Pkg().Path()) || u.TypeArgs().Len() > 0 {
			return false
		}
		switch b := u.Underlying().(type) {
		case *types.Struct:
			return true
		case *types.Basic:
			return b.Kind() != types.UnsafePointer
		}
	}
	return false
}

func inModule(module, path string) bool {
	return path == module || strings.HasPrefix(path, module+"/")
}

// posString writes a position as "dir/file.go:line" relative to root, with forward slashes.
func posString(fset *token.FileSet, root string, pos token.Pos) string {
	if !pos.IsValid() {
		return "?"
	}
	p := fset.Position(pos)
	name := p.Filename
	if rel, err := filepath.Rel(root, name); err == nil && !strings.HasPrefix(rel, "..") {
		name = rel
	}
	return filepath.ToSlash(name) + ":" + strconv.Itoa(p.Line)
}
