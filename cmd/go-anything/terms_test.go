package main

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// terminologyScanExts limits the scan to text file types that carry prose.
var terminologyScanExts = map[string]bool{
	".md":         true,
	".go":         true,
	".java":       true,
	".sh":         true,
	".py":         true,
	".sql":        true,
	".yml":        true,
	".toml":       true,
	".xml":        true,
	".properties": true,
}

// terminologySkippedDirs lists directory names that are never scanned.
var terminologySkippedDirs = map[string]bool{
	".git":      true,
	"WORK":      true,
	"target":    true,
	"bin":       true,
	"build":     true,
	"resources": true,
}

// terminologySkippedRelDirs lists repo-root-relative directory paths that are
// never scanned.
var terminologySkippedRelDirs = map[string]bool{
	"docs/task-id":      true,
	"docs/research/raw": true,
}

// checkTerminologyLine reports whether a repo-root-relative file is excluded
// from the terminology scan by name.
func terminologyFileExcluded(rel string, base string) bool {
	if base == "terms_test.go" {
		return true
	}
	return filepath.ToSlash(rel) == "docs/TERMS.md"
}

// bannedKoreanWords are direct loan translations (left column of
// docs/TERMS.md) matched as plain substrings of Hangul text.
var bannedKoreanWords = []string{
	"발산", "수렴", "오라클", "폴백", "프로브", "프로빙", "원장", "장부",
	"거울상", "래퍼", "위탁", "재판정", "드리프트", "언스테이블", "스캐폴드",
	"리파인", "페이싱", "패싱", "옵트인", "앵커", "번들", "렌즈", "스폰",
	"리컨", "계보", "과업", "소관", "되묻기", "완주", "함정", "해밀",
	"양산", "선례", "계수", "은폐", "잔류", "무인자", "스로틀",
	"이터레이션", "심판", "녹화", "웨이브", "몽키패치", "하니스", "헤니스",
	"회차", "깨끗", "체인", "연쇄", "완충", "불확실", "탐침", "트레이스",
}

// wordUnlessPrevViolates reports whether word occurs in line at a position that
// is not directly preceded by allowedPrev.
func wordUnlessPrevViolates(line, word, allowedPrev string) bool {
	for offset := 0; ; {
		idx := strings.Index(line[offset:], word)
		if idx < 0 {
			return false
		}
		start := offset + idx
		if start == 0 || !strings.HasSuffix(line[:start], allowedPrev) {
			return true
		}
		offset = start + len(word)
	}
}

// Go regexp has no look-around, so context-sensitive Korean terms are handled
// with explicit matchers below.
var (
	// 이동 followed by 표본 is always the loanword pair.
	bannedMoveSample = regexp.MustCompile(`이동\s*표본`)
	// 대입 is only banned when it means "rewrite by substitution".
	bannedAssignment = regexp.MustCompile(`대입\s*(구현|재작성|도구|하는|용)`)
	// English loanwords are matched on ASCII word boundaries, case-insensitive.
	// divergen is a stem so divergence, divergent and diverges are all caught.
	bannedEnglishWords = regexp.MustCompile(`(?i)\b(oracle|unstable|divergen\w*)\b`)
)

// containsHangul reports whether the line carries Hangul syllables.
func containsHangul(line string) bool {
	for _, r := range line {
		if r >= 0xAC00 && r <= 0xD7A3 {
			return true
		}
	}
	return false
}

// stripInlineCode removes paired `...` spans so code spans are not scanned.
// An unmatched backtick leaves the line untouched.
func stripInlineCode(line string) string {
	for {
		start := strings.Index(line, "`")
		if start < 0 {
			return line
		}
		rest := line[start+1:]
		end := strings.Index(rest, "`")
		if end < 0 {
			return line
		}
		line = line[:start] + rest[end+1:]
	}
}

// wordAtViolates reports whether any occurrence of word in line is followed by
// text for which violates returns true.
func wordAtViolates(line, word string, violates func(next string) bool) bool {
	for offset := 0; offset <= len(line); {
		idx := strings.Index(line[offset:], word)
		if idx < 0 {
			return false
		}
		if violates(line[offset+idx+len(word):]) {
			return true
		}
		offset += idx + len(word)
	}
	return false
}

// checkTerminologyLine returns every banned term present in one content line.
func checkTerminologyLine(line string) []string {
	var hits []string
	for _, w := range bannedKoreanWords {
		if strings.Contains(line, w) {
			hits = append(hits, w)
		}
	}
	if bannedMoveSample.MatchString(line) {
		hits = append(hits, "이동 표본")
	}
	// 클린 is allowed only inside 클린룸 and 클린 아키텍처.
	if wordAtViolates(line, "클린", func(next string) bool {
		next = strings.TrimLeft(next, " ")
		return !strings.HasPrefix(next, "룸") && !strings.HasPrefix(next, "아키")
	}) {
		hits = append(hits, "클린")
	}
	// 편차 is a statistics term except in 편차 기록 / 편차로.
	if wordAtViolates(line, "편차", func(next string) bool {
		return strings.HasPrefix(next, " 기록") || strings.HasPrefix(next, "로")
	}) {
		hits = append(hits, "편차")
	}
	if bannedAssignment.MatchString(line) {
		hits = append(hits, "대입")
	}
	// 무화 is banned except in 의무화, the ordinary word for "make mandatory".
	if wordUnlessPrevViolates(line, "무화", "의") {
		hits = append(hits, "무화")
	}
	for _, m := range bannedEnglishWords.FindAllString(line, -1) {
		hits = append(hits, strings.ToLower(m))
	}
	return hits
}

// scanTerminologyFile checks one file and returns hits as "<line>: <word>".
func scanTerminologyFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var hits []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	inFence := false
	for scanner.Scan() {
		lineNo++
		raw := scanner.Text()
		trimmed := strings.TrimRight(raw, " \t\r")
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if strings.Contains(raw, "terms:allow") {
			continue
		}
		if !containsHangul(raw) {
			continue
		}
		for _, w := range checkTerminologyLine(stripInlineCode(raw)) {
			hits = append(hits, fmt.Sprintf("%d: %s", lineNo, w))
		}
	}
	return hits, scanner.Err()
}

// scanTerminology walks root and returns violations as "file:line: word".
// Files are visited in sorted order, so the output is deterministic.
func scanTerminology(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			if rel == "." {
				return nil
			}
			if terminologySkippedDirs[d.Name()] ||
				terminologySkippedRelDirs[filepath.ToSlash(rel)] {
				return filepath.SkipDir
			}
			return nil
		}
		if !terminologyScanExts[strings.ToLower(filepath.Ext(d.Name()))] {
			return nil
		}
		if terminologyFileExcluded(rel, d.Name()) {
			return nil
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no scannable files found under %s", root)
	}
	sort.Strings(files)

	var violations []string
	for _, rel := range files {
		hits, err := scanTerminologyFile(filepath.Join(root, rel))
		if err != nil {
			return nil, err
		}
		for _, h := range hits {
			violations = append(violations, rel+":"+h)
		}
	}
	return violations, nil
}

// TestTerminology fails when repository prose uses the direct loanword
// translations listed in docs/TERMS.md instead of the standard Korean IT
// terms. Every violation is reported as "file:line: word".
func TestTerminology(t *testing.T) {
	violations, err := scanTerminology(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("terminology scan failed: %v", err)
	}
	for _, v := range violations {
		t.Errorf("%s", v)
	}
}

// TestTerminologyRules exercises the scan rules against a synthetic tree in a
// private temporary directory so the real repositories stay untouched and two
// copies of this test (one per repository) cannot collide on a shared path.
func TestTerminologyRules(t *testing.T) {
	root := t.TempDir()
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("clean %s: %v", root, err)
	}
	files := map[string]string{
		"cmd/x/banned.md":          "이 작업은 중요한 과업이다.\n",
		"cmd/x/inline_code.md":     "이 값은 `과업` 이름과 무관하다.\n",
		"cmd/x/allowed.md":         "이 과업은 기록 예외 항목이다. // terms:allow\n",
		"cmd/x/clean_ok.md":        "우리는 클린룸 규칙과 클린 아키텍처를 따른다.\n",
		"cmd/x/assign_ok.md":       "초기화는 값 대입에서 시작한다.\n",
		"cmd/x/rest_ok.md":         "준비가 끝나면 다시 되묻다.\n",
		"cmd/x/deviation_ok.md":    "측정 편차가 작다.\n",
		"cmd/x/duty_ok.md":         "이 규칙은 의무화되어 있다.\n",
		"cmd/x/english_ok.md":      "여기는 한국어 안내 줄이다.\nThe oracle resolves this automatically.\n",
		"cmd/x/code_block.md":      "```text\n여기 안의 과업은 세지 않는다.\n```\n밖의 정상 문장이다.\n",
		"docs/banned/clean.md":     "이번 클린 라운드를 시작한다.\n",
		"docs/banned/assign.md":    "이 로직은 대입 구현을 요구한다.\n",
		"docs/banned/oracle.md":    "이 경로는 oracle 함수를 부른다.\n",
		"docs/banned/move.md":      "이동 표본을 다시 찍는다.\n",
		"docs/banned/deviation.md": "편차 기록을 남긴다.\n",
		"docs/banned/suppress.md":  "결함을 무화하지 않는다.\n",
		"docs/banned/jargon2.md":   "이 판정은 불확실하며 인증 연쇄와 탐침, 트레이스가 남아 있다.\n",
		"docs/banned/divergen.md":  "이 리포트는 divergence 를 적는다.\n",
	}
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	violations, err := scanTerminology(root)
	if err != nil {
		t.Fatalf("terminology scan failed: %v", err)
	}
	hitsByFile := map[string][]string{}
	for _, v := range violations {
		parts := strings.SplitN(v, ":", 2)
		hitsByFile[parts[0]] = append(hitsByFile[parts[0]], parts[1])
	}
	wordHit := func(file, word string) bool {
		for _, h := range hitsByFile[file] {
			if strings.Contains(h, word) {
				return true
			}
		}
		return false
	}

	cases := []struct {
		name string
		file string
		word string
		want bool
	}{
		{"banned word caught", "cmd/x/banned.md", "과업", true},
		{"inline code ignored", "cmd/x/inline_code.md", "과업", false},
		{"terms allow ignored", "cmd/x/allowed.md", "과업", false},
		{"cleanroom allowed", "cmd/x/clean_ok.md", "클린", false},
		{"clean round caught", "docs/banned/clean.md", "클린", true},
		{"clean architecture allowed", "cmd/x/clean_ok.md", "아키", false},
		{"rest verb allowed", "cmd/x/rest_ok.md", "되묻", false},
		{"value assignment allowed", "cmd/x/assign_ok.md", "대입", false},
		{"assignment rewrite caught", "docs/banned/assign.md", "대입", true},
		{"statistical deviation allowed", "cmd/x/deviation_ok.md", "편차", false},
		{"deviation record caught", "docs/banned/deviation.md", "편차", true},
		{"mandatory word allowed", "cmd/x/duty_ok.md", "무화", false},
		{"suppression caught", "docs/banned/suppress.md", "무화", true},
		{"uncertain caught", "docs/banned/jargon2.md", "불확실", true},
		{"chain caught", "docs/banned/jargon2.md", "연쇄", true},
		{"probe synonym caught", "docs/banned/jargon2.md", "탐침", true},
		{"trace caught", "docs/banned/jargon2.md", "트레이스", true},
		{"english divergence caught", "docs/banned/divergen.md", "divergence", true},
		{"english only line ignored", "cmd/x/english_ok.md", "oracle", false},
		{"korean line english caught", "docs/banned/oracle.md", "oracle", true},
		{"code block ignored", "cmd/x/code_block.md", "과업", false},
		{"move sample caught", "docs/banned/move.md", "이동 표본", true},
	}
	for _, tc := range cases {
		if got := wordHit(tc.file, tc.word); got != tc.want {
			t.Errorf("%s: %s hit=%v, want %v (violations: %v)",
				tc.name, tc.file, got, tc.want, hitsByFile[tc.file])
		}
	}

	// An empty tree must fail loudly instead of passing silently.
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.RemoveAll(empty); err != nil {
		t.Fatalf("clean %s: %v", empty, err)
	}
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", empty, err)
	}
	if _, err := scanTerminology(empty); err == nil {
		t.Errorf("empty root scan passed silently, want error")
	}
}
