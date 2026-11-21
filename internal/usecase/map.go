package usecase

import (
	"fmt"
	"os"

	"github.com/go-anything/go-anything/internal/domain"
)

// Status is the drift state of one recorded unit against its current source.
type Status string

const (
	StatusOK            Status = "ok"             // source unchanged since record
	StatusDrifted       Status = "drifted"        // source changed since record: record again
	StatusNoFingerprint Status = "no-fingerprint" // recording never saved a fingerprint: cannot trust it
	StatusMissingSource Status = "missing-source" // the source directory does not exist
	StatusError         Status = "error"          // the recording or source could not be read
)

// UnitStatus reports one recording's drift state.
type UnitStatus struct {
	Unit   string
	Status Status
	Cases  int
	Detail string
}

// Target pairs one recording file with the source directory it was recorded from.
type Target struct {
	Recording string
	Source    string
}

// Map checks every target and reports each unit's drift state. It does not stop at the first
// problem: a map is only useful when it shows the whole picture.
func Map(targets []Target, load func(string) (domain.Recording, error)) []UnitStatus {
	out := make([]UnitStatus, 0, len(targets))
	for _, t := range targets {
		out = append(out, checkOne(t, load))
	}
	return out
}

func checkOne(t Target, load func(string) (domain.Recording, error)) UnitStatus {
	rec, err := load(t.Recording)
	if err != nil {
		return UnitStatus{Unit: t.Recording, Status: StatusError, Detail: err.Error()}
	}
	// Identify the unit by its recording file: the subject is the command line, and several
	// units can share one command (e.g. the same binary), which would make the report ambiguous.
	us := UnitStatus{Unit: t.Recording, Cases: len(rec.Cases)}
	if fi, err := os.Stat(t.Source); err != nil || !fi.IsDir() {
		us.Status = StatusMissingSource
		us.Detail = t.Source
		return us
	}
	if rec.Source == nil {
		us.Status = StatusNoFingerprint
		us.Detail = "record again with -src to enable drift checks"
		return us
	}
	now, err := domain.FingerprintDir(t.Source)
	if err != nil {
		us.Status = StatusError
		us.Detail = err.Error()
		return us
	}
	if rec.Source.Drifted(now) {
		us.Status = StatusDrifted
		us.Detail = fmt.Sprintf("was %d files, now %d", rec.Source.Files, now.Files)
		return us
	}
	us.Status = StatusOK
	return us
}

// Problems counts units that cannot be trusted as-is: drifted, unfingerprinted, missing or unreadable.
func Problems(units []UnitStatus) int {
	n := 0
	for _, u := range units {
		if u.Status != StatusOK {
			n++
		}
	}
	return n
}
