package usecase

import (
	"strings"
	"testing"
)

const pyTraceback = `Traceback (most recent call last):
  File "/home/alice/proj/greet.py", line 17, in <module>
    main()
  File "/home/alice/proj/greet.py", line 6, in parse
    raise ValueError("agent name is classified: " + name)
ValueError: agent name is classified: 007`

const goPanic = `panic: runtime error: index out of range [3] with length 3

goroutine 1 [running]:
main.parse(...)
	/home/bob/src/conv/main.go:12
main.main()
	/home/bob/src/conv/main.go:20 +0x1d

goroutine 7 [chan receive]:
main.worker(0xc000012345)
	/usr/local/go/src/runtime/proc.go:402 +0xce
exit status 2`

const nodeStack = `/Users/carol/app/index.js:3
  throw new TypeError("bad name");
  ^

TypeError: bad name
    at parse (/Users/carol/app/index.js:3:9)
    at Object.<anonymous> (/Users/carol/app/index.js:7:1)
    at node:internal/main/run_main_module:28:49

Node.js v20.11.0`

func TestMaskCrashText(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		want     []string // substrings that must be in the result
		mustNot  []string // substrings that must not be
		exceptID string   // the exception line, kept whole
	}{
		{"python traceback", pyTraceback,
			[]string{`File "greet.py", line 17, in <module>`, `File "greet.py", line 6, in parse`},
			[]string{"/home", "alice", "proj/"},
			"ValueError: agent name is classified: 007"},
		{"go panic with goroutine dump", goPanic,
			[]string{"\tmain.go:12\n", "\tmain.go:20 +0x1d", "\tproc.go:402 +0xce", "goroutine 7 [chan receive]:"},
			[]string{"/home", "bob", "/usr/local"},
			"panic: runtime error: index out of range [3] with length 3"},
		{"node stack", nodeStack,
			[]string{"index.js:3\n", "at parse (index.js:3:9)", "at node:internal/main/run_main_module:28:49"},
			[]string{"/Users", "carol"},
			"TypeError: bad name"},
		{"home dir alone and quoted path", `FileNotFoundError: [Errno 2] No such file: '/home/dave/secret.txt' (cwd /root)`,
			[]string{"'secret.txt'", "(cwd ~)"}, []string{"dave", "/root"}, ""},
		{"windows path", `  File "C:\Users\erin\proj\app.py", line 3 (home C:\Users\erin)`,
			[]string{`File "app.py", line 3 (home ~)`}, []string{"erin", `C:\`}, ""},
		{"url and relative path untouched", `fetch http://example.com/a/b failed in pkg/sub/file.go:3; /rootfs/x/y.txt`,
			[]string{"http://example.com/a/b", "pkg/sub/file.go:3", "y.txt"}, []string{"/rootfs"}, ""},
		{"one segment stays", "open /etc: denied", []string{"open /etc: denied"}, nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := MaskCrashText(c.in)
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("want %q in:\n%s", w, got)
				}
			}
			for _, w := range c.mustNot {
				if strings.Contains(got, w) {
					t.Errorf("must not contain %q:\n%s", w, got)
				}
			}
			if c.exceptID != "" && !strings.Contains(got, c.exceptID+"\n") && !strings.HasSuffix(got, c.exceptID) {
				t.Errorf("exception line %q not kept whole:\n%s", c.exceptID, got)
			}
			if again := MaskCrashText(got); again != got {
				t.Errorf("masking twice must not change short text:\n%s\n---\n%s", got, again)
			}
		})
	}
}

func TestMaskCrashTextClipsAndKeepsExceptionLine(t *testing.T) {
	// A deep Python traceback: the exception line comes last, far past the limit.
	var b strings.Builder
	b.WriteString("Traceback (most recent call last):\n")
	for i := 0; i < 200; i++ {
		b.WriteString("  File \"/home/alice/proj/deep.py\", line 9, in recurse\n    return recurse(n - 1)\n")
	}
	b.WriteString("RecursionError: maximum recursion depth exceeded")
	got := MaskCrashText(b.String())
	if !strings.HasSuffix(got, "\nRecursionError: maximum recursion depth exceeded") {
		t.Fatalf("the exception line must survive clipping; tail: %q", got[max(0, len(got)-120):])
	}
	if !strings.Contains(got, "more bytes not shown]") {
		t.Fatal("clipped text must say how much was cut")
	}
	if strings.Contains(got, "alice") {
		t.Fatal("home directory leaked")
	}
	// Only the clip marker may exceed the limit.
	if len(got) > CrashTextLimit+len(" [... 99999 more bytes not shown]")+1 {
		t.Fatalf("clipped text is %d bytes, limit %d", len(got), CrashTextLimit)
	}

	// A Go panic names the failure first, so plain clipping keeps it.
	long := "panic: boom\n\n" + strings.Repeat("goroutine 9 [select]:\n\t/home/bob/x/y.go:1 +0x1\n", 300)
	got = MaskCrashText(long)
	if !strings.HasPrefix(got, "panic: boom\n") || len(got) > CrashTextLimit+64 {
		t.Fatalf("go panic must keep its first line and be clipped: %d bytes, head %q", len(got), got[:20])
	}
}

func TestExceptionLine(t *testing.T) {
	cases := map[string]string{
		pyTraceback:             "ValueError: agent name is classified: 007",
		goPanic:                 "panic: runtime error: index out of range [3] with length 3",
		nodeStack:               "TypeError: bad name",
		"Traceback\nValueError": "ValueError",
		"usage: x\n\n":          "usage: x",
		"":                      "",
	}
	for in, want := range cases {
		if got := ExceptionLine(in); got != want {
			t.Errorf("ExceptionLine(%q) = %q, want %q", in, got, want)
		}
	}
}
