package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/go-anything/go-anything/internal/adapter/secure"
	"github.com/go-anything/go-anything/internal/domain"
)

// runSecure runs the security function confirmation checklist on root and writes the artifacts under
// outDir (default <root>/.go-anything/security). It is the shared body of `secure` and
// `gate -security`. Writing the report never depends on the check outcome.
func runSecure(root string, noHTML bool, outDir string) (domain.SecureReport, error) {
	secure.Version = version
	rep, err := secure.Run(root)
	if err != nil {
		return rep, err
	}
	if outDir == "" {
		outDir = filepath.Join(root, ".go-anything", "security")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return rep, fmt.Errorf("secure: create %s: %w", outDir, err)
	}
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return rep, err
	}
	if err := os.WriteFile(filepath.Join(outDir, "checklist.json"), data, 0o644); err != nil {
		return rep, fmt.Errorf("secure: write checklist.json: %w", err)
	}
	if !noHTML {
		if err := os.WriteFile(filepath.Join(outDir, "security.html"), []byte(domain.SecureReportHTML(rep)), 0o644); err != nil {
			return rep, fmt.Errorf("secure: write security.html: %w", err)
		}
	}
	return rep, nil
}

// cmdSecure is the standalone security function confirmation run. It is opt-in from every other
// command: nothing else runs the checklist unless the operator asks for it.
func cmdSecure(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("secure", flag.ContinueOnError)
	root := fs.String("root", ".", "Go module root to check")
	out := fs.String("out", "", "directory for checklist.json and security.html (default <root>/.go-anything/security)")
	reportOnly := fs.Bool("report-only", false, "never exit non-zero; write and print the report only")
	noHTML := fs.Bool("no-html", false, "write checklist.json only")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rep, err := runSecure(*root, *noHTML, *out)
	if err != nil {
		return err
	}
	for i, res := range rep.Results {
		it := rep.Items[i]
		mark := string(res.Status)
		detail := ""
		switch {
		case len(res.Findings) > 0:
			detail = fmt.Sprintf("%d finding(s), first at %s", len(res.Findings), res.Findings[0].Pos)
		case res.Note != "":
			detail = res.Note
		case res.Status == domain.SecureStatusManual:
			detail = "for a person to verify (see the guidance in security.html)"
		}
		fmt.Fprintf(stdout, "[%-6s] %-8s %-9s %s | %s\n", mark, it.ID, it.Severity, it.RequirementEN, detail)
	}
	n := rep.CountsPerStatus()
	fmt.Fprintf(stdout, "\nsecure: %d pass, %d fail, %d manual, %d skip over %d scanned file(s)\n",
		n[domain.SecurePass], n[domain.SecureFail], n[domain.SecureStatusManual], n[domain.SecureSkip], rep.ScannedFiles)
	failed := rep.MandatoryFailures()
	if len(failed) > 0 && !*reportOnly {
		ids := make([]string, 0, len(failed))
		for _, r := range failed {
			ids = append(ids, r.Item)
		}
		return fmt.Errorf("secure: %d mandatory item(s) failed: %s (see security.html; -report-only exits zero)", len(failed), joinIDs(ids))
	}
	return nil
}

func joinIDs(ids []string) string {
	out := ""
	for i, id := range ids {
		if i > 0 {
			out += ", "
		}
		out += id
	}
	return out
}
