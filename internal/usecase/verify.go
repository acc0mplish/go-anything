package usecase

import (
	"fmt"

	"github.com/go-anything/go-anything/internal/domain"
)

// Mismatch describes one case where the subject diverged from the recording.
type Mismatch struct {
	Case     string
	Status   domain.CaseStatus
	Expected string
	Got      string
	Err      error
	// Detail names the first difference (a JSON path or a line) when the outputs differ.
	Detail string
}

// Verdict is the result of replaying a recording against a subject.
type Verdict struct {
	Subject string
	Total   int
	Matched int
	// Names lists every case in recording order, matched or not, so reports can name each one.
	Names      []string
	Mismatches []Mismatch
	// Inputs maps a case name to its recorded input, so a report can show how to reproduce a failure.
	Inputs map[string]string
	// Stale is set when the original's source changed after the recording was made, or the
	// recording has no fingerprint to check against. A stale recording describes what the
	// original used to do, so it cannot pass the gate.
	Stale bool
	// DriftChecked is false when no source directory was given, so drift was not verified.
	DriftChecked bool
	// Compare is how outputs were compared. The zero value is exact.
	Compare domain.CompareOptions
}

// Passed reports whether every recorded case matched and the recording is not stale.
func (v Verdict) Passed() bool { return v.Total > 0 && v.Matched == v.Total && !v.Stale }

// Verify replays every recorded case against the subject (the diff step).
// A recorded error must be reproduced as an error; a recorded output must be reproduced exactly.
func Verify(rec domain.Recording, subject Subject) (Verdict, error) {
	return VerifyWith(rec, subject, domain.CompareOptions{})
}

// VerifyWith is Verify with recorded outputs compared under opt (domain.EqualBody): JSON semantic
// and a float tolerance when asked for. Recorded errors are still matched as errors.
func VerifyWith(rec domain.Recording, subject Subject, opt domain.CompareOptions) (Verdict, error) {
	if err := rec.Validate(); err != nil {
		return Verdict{}, fmt.Errorf("verify: %w", err)
	}
	if err := opt.Validate(); err != nil {
		return Verdict{}, fmt.Errorf("verify: %w", err)
	}
	v := Verdict{Subject: rec.Subject, Total: len(rec.Cases), Inputs: map[string]string{}, Compare: opt}
	for _, c := range rec.Cases {
		v.Names = append(v.Names, c.Name)
		v.Inputs[c.Name] = c.Input
		got, err := subject.Run(c.Input)
		switch {
		case c.Error != "":
			if err == nil {
				v.Mismatches = append(v.Mismatches, Mismatch{Case: c.Name, Status: domain.StatusMismatch, Expected: "error: " + c.Error, Got: got})
				continue
			}
			v.Matched++
		case err != nil:
			v.Mismatches = append(v.Mismatches, Mismatch{Case: c.Name, Status: domain.StatusError, Expected: c.Output, Err: err})
		default:
			if eq, why := domain.EqualBody(domain.Normalize(c.Output), domain.Normalize(got), opt); !eq {
				v.Mismatches = append(v.Mismatches, Mismatch{Case: c.Name, Status: domain.StatusMismatch, Expected: c.Output, Got: got, Detail: why})
				continue
			}
			v.Matched++
		}
	}
	return v, nil
}

// CheckDrift compares the recorded source fingerprint with the current source. A recording
// without a fingerprint cannot be checked, so it is reported as stale rather than trusted.
func CheckDrift(rec domain.Recording, now domain.Fingerprint, nowKnown bool) (stale bool) {
	if rec.Source == nil || !nowKnown {
		return true
	}
	return rec.Source.Drifted(now)
}
