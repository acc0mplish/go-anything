package analyze

// pyScript runs under `python3 -I -c pyScript <root>`. It reads the root from argv (a path is
// never put into the code), walks it with os.walk, parses each .py file with Python's own ast
// module and prints one JSON document on stdout. No analyzed code is imported or run.
//
// Per file it reports the line count, the imports as written, the top-level functions with
// their parameters and the dotted names they call, and the risk sites that need Python
// knowledge to find: calls such as eval or os.system (seen through import aliases) and module
// variables changed inside functions. Import based risks are classified on the Go side, which
// knows which imports stay inside the repo. A file that does not parse is reported with its
// error and line, and the walk goes on. Test files are listed, not parsed.
const pyScript = `
import ast, json, os, sys

SKIP = {"node_modules", ".venv", "venv", "__pycache__", "dist", "build", ".git"}
MUTATORS = {"append", "extend", "insert", "pop", "popitem", "remove", "clear", "update",
            "setdefault", "add", "discard", "sort", "reverse", "appendleft", "popleft",
            "extendleft", "rotate", "__setitem__", "__delitem__"}
MUTABLE_CALLS = {"list", "dict", "set", "bytearray", "defaultdict", "OrderedDict", "Counter", "deque"}
DYNAMIC_BUILTINS = {"eval", "exec", "compile", "__import__"}
PROCESS_CALLS = {"os.system", "os.popen", "os.fork", "os.forkpty", "os.startfile",
                 "os.posix_spawn", "os.posix_spawnp"}
FS_CALLS = {"os.remove", "os.unlink", "os.rename", "os.replace", "os.makedirs", "os.mkdir",
            "os.rmdir", "os.removedirs", "os.truncate", "os.symlink", "os.link", "os.chmod",
            "shutil.rmtree", "shutil.copy", "shutil.copy2", "shutil.copyfile", "shutil.copytree",
            "shutil.move"}
NONDET_CALLS = {"time.time", "time.time_ns", "os.urandom", "os.getrandom", "uuid.uuid1", "uuid.uuid4"}
TRY_TYPES = tuple(t for t in (getattr(ast, "Try", None), getattr(ast, "TryStar", None)) if t)


def is_test(name):
    return (name.startswith("test_") and name.endswith(".py")) or name.endswith("_test.py") or name == "conftest.py"


def dotted(node):
    parts = []
    while isinstance(node, ast.Attribute):
        parts.append(node.attr)
        node = node.value
    if isinstance(node, ast.Name):
        parts.append(node.id)
        return ".".join(reversed(parts))
    return None


def is_mutable(v):
    if isinstance(v, (ast.List, ast.Dict, ast.Set, ast.ListComp, ast.DictComp, ast.SetComp)):
        return True
    if isinstance(v, ast.Call):
        d = dotted(v.func)
        return d is not None and d.split(".")[-1] in MUTABLE_CALLS
    return False


def top_level(body):
    # Statements that run at import time: into if, for, while, with and try blocks, not into defs.
    stack = list(reversed(body))
    while stack:
        st = stack.pop()
        yield st
        blocks = []
        if isinstance(st, (ast.If, ast.For, ast.AsyncFor, ast.While, ast.With, ast.AsyncWith)):
            blocks = [st.body, getattr(st, "orelse", [])]
        elif TRY_TYPES and isinstance(st, TRY_TYPES):
            blocks = [st.body, st.orelse, st.finalbody] + [h.body for h in st.handlers]
        for b in reversed(blocks):
            stack.extend(reversed(b))


def all_functions(tree):
    # Every def with a qualified name: Class.method, outer.inner.
    out = []
    stack = [(st, "") for st in reversed(tree.body)]
    while stack:
        node, prefix = stack.pop()
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
            q = prefix + node.name
            out.append((q, node))
            stack.extend((c, q + ".") for c in reversed(node.body))
        elif isinstance(node, ast.ClassDef):
            stack.extend((c, prefix + node.name + ".") for c in reversed(node.body))
        else:
            for c in ast.iter_child_nodes(node):
                if isinstance(c, (ast.stmt,)):
                    stack.append((c, prefix))
    return out


def params(fn):
    try:
        text = "(" + ast.unparse(fn.args) + ")"
    except Exception:
        text = "(...)"
    if isinstance(fn, ast.AsyncFunctionDef):
        text = "async " + text
    return text


def analyze(path, rel):
    entry = {"path": rel, "lines": 0, "error": None, "imports": [], "funcs": [], "risks": []}
    try:
        with open(path, "rb") as f:
            data = f.read()
    except OSError as e:
        entry["error"] = {"line": 0, "msg": "cannot read: " + str(e)}
        return entry
    entry["lines"] = data.count(b"\n")
    try:
        tree = ast.parse(data, filename=rel)
    except SyntaxError as e:
        entry["error"] = {"line": e.lineno or 0, "msg": str(e.msg)}
        return entry
    except (ValueError, RecursionError, MemoryError) as e:
        entry["error"] = {"line": 0, "msg": type(e).__name__ + ": " + str(e)}
        return entry

    def risk(kind, node, detail):
        entry["risks"].append({"kind": kind, "line": getattr(node, "lineno", 0), "detail": detail})

    aliases = {}
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            for a in node.names:
                entry["imports"].append({"kind": "import", "module": a.name, "level": 0,
                                         "names": [[a.name, a.asname or ""]], "line": node.lineno})
                if a.asname:
                    aliases[a.asname] = a.name
                else:
                    first = a.name.split(".")[0]
                    aliases[first] = first
        elif isinstance(node, ast.ImportFrom):
            entry["imports"].append({"kind": "from", "module": node.module or "", "level": node.level or 0,
                                     "names": [[a.name, a.asname or ""] for a in node.names], "line": node.lineno})
            if not node.level and node.module:
                for a in node.names:
                    if a.name != "*":
                        aliases[a.asname or a.name] = node.module + "." + a.name
    entry["imports"].sort(key=lambda i: i["line"])

    def canon(name):
        first, _, rest = name.partition(".")
        if first in aliases:
            return aliases[first] + ("." + rest if rest else "")
        return name

    for node in ast.walk(tree):
        if not isinstance(node, ast.Call):
            continue
        d = dotted(node.func)
        if d is None:
            continue
        c = canon(d)
        if d in DYNAMIC_BUILTINS and d not in aliases:
            risk("dynamic_code", node, "calls " + d)
        elif c in ("builtins.eval", "builtins.exec", "builtins.compile", "builtins.__import__",
                   "importlib.import_module", "importlib.reload", "importlib.__import__"):
            risk("dynamic_code", node, "calls " + c)
        elif c in PROCESS_CALLS or c.startswith("os.spawn") or c.startswith("os.exec"):
            risk("side_effects", node, "calls " + c)
        elif c in FS_CALLS or (d.endswith((".write_text", ".write_bytes"))
                               and any(w in d.split(".")[-2].lower() for w in ("path", "file"))):
            risk("fs_write", node, "calls " + c)
        elif c in NONDET_CALLS:
            risk("nondeterminism", node, "calls " + c)
        elif c.startswith("datetime.") and c.rsplit(".", 1)[-1] in ("now", "utcnow", "today"):
            risk("nondeterminism", node, "calls " + c)
        elif c in ("open", "io.open", "builtins.open") or d.endswith(".open"):
            # open(path, mode) for the builtin, path.open(mode) for pathlib.
            mode = None
            if c in ("open", "io.open", "builtins.open"):
                if len(node.args) > 1:
                    mode = node.args[1]
            elif node.args:
                mode = node.args[0]
                if not (isinstance(mode, ast.Constant) and isinstance(mode.value, str)
                        and mode.value and set(mode.value) <= set("rwxabt+")):
                    mode = None
            for k in node.keywords:
                if k.arg == "mode":
                    mode = k.value
            if isinstance(mode, ast.Constant) and isinstance(mode.value, str) and any(ch in mode.value for ch in "wax+"):
                risk("fs_write", node, "opens a file with mode " + repr(mode.value))

    # Module variables changed inside functions.
    assigned, mutable = {}, set()
    for st in top_level(tree.body):
        targets = []
        if isinstance(st, ast.Assign):
            targets = st.targets
        elif isinstance(st, (ast.AnnAssign, ast.AugAssign)):
            targets = [st.target]
        for t in targets:
            for n in ast.walk(t):
                if isinstance(n, ast.Name) and n.id not in assigned:
                    assigned[n.id] = st.lineno
                    if isinstance(st, (ast.Assign, ast.AnnAssign)) and st.value is not None and is_mutable(st.value):
                        mutable.add(n.id)
    changed = {}
    for q, fn in all_functions(tree):
        declared = {}
        for n in ast.walk(fn):
            if isinstance(n, ast.Global):
                for name in n.names:
                    declared.setdefault(name, n.lineno)
        local = {a.arg for a in fn.args.args + fn.args.kwonlyargs + getattr(fn.args, "posonlyargs", [])}
        for a in (fn.args.vararg, fn.args.kwarg):
            if a is not None:
                local.add(a.arg)
        for n in ast.walk(fn):
            if isinstance(n, ast.Name) and isinstance(n.ctx, ast.Store) and n.id not in declared:
                local.add(n.id)
        for n in ast.walk(fn):
            name = None
            if isinstance(n, ast.Name) and isinstance(n.ctx, (ast.Store, ast.Del)) and n.id in declared:
                name = n.id
            elif isinstance(n, (ast.Subscript, ast.Attribute)) and isinstance(n.ctx, (ast.Store, ast.Del)) \
                    and isinstance(n.value, ast.Name):
                v = n.value.id
                if v in declared or (v in mutable and v not in local):
                    name = v
            elif isinstance(n, ast.Call) and isinstance(n.func, ast.Attribute) and n.func.attr in MUTATORS \
                    and isinstance(n.func.value, ast.Name):
                v = n.func.value.id
                if v in declared or (v in mutable and v not in local):
                    name = v
            if name is not None and name not in changed:
                changed[name] = (q, n.lineno, assigned.get(name) or declared.get(name, fn.lineno))
    for name, (q, line, decl) in sorted(changed.items(), key=lambda kv: kv[1][2]):
        entry["risks"].append({"kind": "global_mutable", "line": decl,
                               "detail": "module variable %s changed in %s at %s:%d" % (name, q, rel, line)})

    # Top-level functions with the dotted names they call.
    for st in top_level(tree.body):
        if isinstance(st, (ast.FunctionDef, ast.AsyncFunctionDef)):
            calls = set()
            for n in ast.walk(st):
                if isinstance(n, ast.Call):
                    d = dotted(n.func)
                    if d is not None:
                        calls.add(d)
            entry["funcs"].append({"name": st.name, "line": st.lineno, "params": params(st), "calls": sorted(calls)})
    return entry


def main():
    root = sys.argv[1]
    files, tests = [], []
    for dirpath, dirnames, filenames in os.walk(root, onerror=lambda e: None):
        dirnames[:] = sorted(d for d in dirnames if d not in SKIP and not d.startswith("."))
        for name in sorted(filenames):
            if not name.endswith(".py"):
                continue
            path = os.path.join(dirpath, name)
            if not os.path.isfile(path):
                continue
            rel = os.path.relpath(path, root).replace(os.sep, "/")
            if is_test(name):
                tests.append(rel)
                continue
            files.append(analyze(path, rel))
    json.dump({"python": sys.version.split()[0], "files": files, "tests": tests}, sys.stdout)


main()
`
