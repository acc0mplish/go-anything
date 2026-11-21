package wiregen

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// scaffoldMarker is the text go-anything frontdoor scaffold writes on the first line of the front door's
// main file. A test keeps it in step with the scaffold templates.
const scaffoldMarker = "go-anything frontdoor scaffold wrote it."

// ScaffoldedFrontDoor reports whether the module's main file is a front door written by
// go-anything frontdoor scaffold. Such a front door starts the original itself and sets GA_ORIGINAL_URL
// (server) or GA_ORIGINAL_CLI (command line) for the units, so the operator does not set them.
// Only the leading comment block is read; an unreadable file is not a front door.
func ScaffoldedFrontDoor(root, mainFile string) bool {
	f, err := os.Open(filepath.Join(root, mainFile))
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "//") {
			return false
		}
		if strings.Contains(line, scaffoldMarker) {
			return true
		}
	}
	return false
}
