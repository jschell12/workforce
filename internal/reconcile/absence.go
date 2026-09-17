package reconcile

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// AbsentBeforeClear is how long a session must be CONTINUOUSLY absent from the
// live listing before anything deletes it.
//
// ABSENT ONCE IS NOT FINISHED, and getting this wrong cost a live review. The
// first version of the clear pass deleted anything present in --all and absent
// from the plain listing on ONE sighting. On 2026-09-11 it removed a reviewer
// nine minutes after it was spawned, mid-review, and `claude rm` took the
// transcript with it, so there is not even a record of what it had found. The
// reasoning had come from a session absent for a DAY: a sustained condition,
// implemented as an instantaneous one.
//
// Thirty minutes is longer than any hiccup and far shorter than a day.
const AbsentBeforeClear = 1800

// Absence remembers when each session was first seen missing. A single sweep
// cannot tell "gone" from "not listed at this instant", so the clock has to
// outlive the process.
type Absence interface {
	Load() map[string]int64
	Save(map[string]int64)
}

// FileAbsence persists to JSON.
type FileAbsence struct{ Path string }

func (f FileAbsence) Load() map[string]int64 {
	b, err := os.ReadFile(f.Path)
	if err != nil {
		return map[string]int64{}
	}
	var m map[string]int64
	if err := json.Unmarshal(b, &m); err != nil {
		// A corrupt clock file must not wedge the sweep. Starting the clock
		// over is the safe direction: it delays a deletion, never hastens one.
		return map[string]int64{}
	}
	return m
}

func (f FileAbsence) Save(m map[string]int64) {
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
		return
	}
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	tmp := f.Path + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, f.Path)
	}
}

// MemAbsence is for tests and for a run told not to persist.
type MemAbsence struct{ M map[string]int64 }

func (m *MemAbsence) Load() map[string]int64 {
	if m.M == nil {
		m.M = map[string]int64{}
	}
	return m.M
}
func (m *MemAbsence) Save(v map[string]int64) { m.M = v }
