package usecase

import (
	"fmt"
	"slices"

	"github.com/go-anything/go-anything/internal/domain"
)

// RecordOptions controls a whole-program recording.
type RecordOptions struct {
	// Src captures outbound effects (webhooks, database rows). Nil captures none.
	Src EffectSource
	// Before runs before every case (for example a database reset). Nil runs nothing.
	Before func() error
	// Repeat is how many times each case is run. Answers that differ between runs are recorded
	// as unstable fields, so a flaky response cannot pass as a regression later. Values below 1 mean 1.
	Repeat int
}

// ReplayOptions controls a whole-program replay.
type ReplayOptions struct {
	Src    EffectSource
	Before func() error
	// Compare says how the HTTP body and CLI stdout are compared. The zero value is exact.
	Compare domain.CompareOptions
}

// AppRecordWithEffects is AppRecord plus the outbound calls each case made. With src nil it
// behaves exactly like AppRecord.
func AppRecordWithEffects(subject string, cases []domain.AppCase, runner AppRunner, src EffectSource) (domain.AppRecording, error) {
	return AppRecordFull(subject, cases, runner, RecordOptions{Src: src})
}

// AppRecordFull records every case, optionally repeating it to find fields that vary between runs.
func AppRecordFull(subject string, cases []domain.AppCase, runner AppRunner, opts RecordOptions) (domain.AppRecording, error) {
	repeat := max(opts.Repeat, 1)
	rec := domain.AppRecording{Subject: subject}
	for _, c := range cases {
		runs := make([]domain.AppCase, 0, repeat)
		for i := 0; i < repeat; i++ {
			answered, err := withEffects(c, runner, opts.Src, opts.Before)
			if err != nil {
				return domain.AppRecording{}, fmt.Errorf("app record: case %q: %w", c.Name, err)
			}
			runs = append(runs, answered)
		}
		recorded := withAnswer(c, runs[0])
		recorded.Effects = runs[0].Effects
		recorded.Unstable = unstableAcross(runs)
		rec.Cases = append(rec.Cases, recorded)
	}
	return rec, rec.Validate()
}

// AppReplayWithEffects replays and compares answers and outbound calls.
func AppReplayWithEffects(rec domain.AppRecording, runner AppRunner, src EffectSource) (AppVerdict, error) {
	return AppReplayFull(rec, runner, ReplayOptions{Src: src})
}

// AppReplayFull sends every recorded request again and compares answers and effects. A case that
// fails to run counts as a mismatch, never as a pass.
func AppReplayFull(rec domain.AppRecording, runner AppRunner, opts ReplayOptions) (AppVerdict, error) {
	if err := rec.Validate(); err != nil {
		return AppVerdict{}, fmt.Errorf("app replay: %w", err)
	}
	if err := opts.Compare.Validate(); err != nil {
		return AppVerdict{}, fmt.Errorf("app replay: %w", err)
	}
	v := AppVerdict{Subject: rec.Subject, Total: len(rec.Cases), Inputs: map[string]string{}, Compare: opts.Compare}
	for _, want := range rec.Cases {
		v.Names = append(v.Names, want.Name)
		v.Inputs[want.Name] = AppCaseInput(want)
		got, err := withEffects(want, runner, opts.Src, opts.Before)
		if err != nil {
			v.Mismatches = append(v.Mismatches, AppMismatch{Case: want.Name, What: "run", Want: "answer", Got: err.Error()})
			continue
		}
		diffs := compareAppWith(want, got, opts.Compare)
		if !slices.Contains(want.Unstable, "effects") {
			diffs = append(diffs, compareEffects(want.Name, want.Effects, got.Effects)...)
		}
		if len(diffs) == 0 {
			v.Matched++
			continue
		}
		v.Mismatches = append(v.Mismatches, diffs...)
	}
	return v, nil
}
