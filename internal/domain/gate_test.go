package domain

import "testing"

func TestGatePassedNeedsAtLeastOneCheckAndAllPass(t *testing.T) {
	if GatePassed(nil) {
		t.Fatal("no checks must not pass: nothing was verified")
	}
	if !GatePassed([]GateCheck{{Name: "a", Passed: true}, {Name: "b", Passed: true}}) {
		t.Fatal("all passing must pass")
	}
	if GatePassed([]GateCheck{{Name: "a", Passed: true}, {Name: "b", Passed: false}}) {
		t.Fatal("one failure must fail the gate")
	}
}

func TestScanUnimplementedFindsPlaceholders(t *testing.T) {
	src := "package p\n\nfunc A() int {\n\tpanic(\"not implemented\")\n}\nfunc B() {}\n"
	f := ScanUnimplemented("a.go", src)
	if len(f) != 1 || f[0].Line != 4 {
		t.Fatalf("want one finding on line 4, got %+v", f)
	}
	if len(ScanUnimplemented("ok.go", "package p\n\nfunc C() { panic(\"boom: real error\") }")) != 0 {
		t.Fatal("a real panic message must not be flagged")
	}
}

func TestScanUnsafeRequiresSafetyComment(t *testing.T) {
	bad := "package p\n\nimport \"unsafe\"\n\nfunc F(x int) uintptr { return unsafe.Sizeof(x) }\n"
	if len(ScanUnsafe("bad.go", bad)) != 1 {
		t.Fatal("an unjustified unsafe use must be a finding")
	}
	good := "package p\n\nimport \"unsafe\"\n\n// SAFETY: x is a plain int; its size is fixed.\nfunc F(x int) uintptr { return unsafe.Sizeof(x) }\n"
	if len(ScanUnsafe("good.go", good)) != 0 {
		t.Fatal("a use with a SAFETY comment above must pass")
	}
	inline := "package p\nimport \"unsafe\"\nfunc F(x int) uintptr { return unsafe.Sizeof(x) } // SAFETY: fixed size\n"
	if len(ScanUnsafe("inline.go", inline)) != 0 {
		t.Fatal("a SAFETY comment on the same line must pass")
	}
	comment := "package p\n// unsafe.Pointer is discussed here\nfunc F() {}\n"
	if len(ScanUnsafe("c.go", comment)) != 0 {
		t.Fatal("a comment that only mentions unsafe is not a use")
	}
}

func TestScanUnimplementedIgnoresMarkerInsideString(t *testing.T) {
	// the scanner's own marker list must not count as a placeholder
	src := "package p\n\nvar markers = []string{\n\t`panic(\"not implemented\"`,\n}\n"
	if len(ScanUnimplemented("scan.go", src)) != 0 {
		t.Fatal("a marker inside a string literal is not a placeholder")
	}
}

func TestScanUnimplementedIgnoresPlaceholderInsideTemplate(t *testing.T) {
	// a code generator holds the placeholder as text: that is not a placeholder body
	src := "package g\n\nconst tpl = `\n\tpanic(\"not implemented\")\n`\n"
	if len(ScanUnimplemented("gen.go", src)) != 0 {
		t.Fatal("a placeholder inside a raw string is text, not a body")
	}
}

func TestScanUnsafeIgnoresMentionInString(t *testing.T) {
	src := "package p\n\nvar s = \"unsafe.Pointer is discussed\"\n"
	if len(ScanUnsafe("s.go", src)) != 0 {
		t.Fatal("the word unsafe inside a string is not a use")
	}
}
