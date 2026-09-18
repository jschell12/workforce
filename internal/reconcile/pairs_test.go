package reconcile

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

// Concurrent writers must not lose each other's rows.
//
// This is the defect the lock exists for, and it is invisible to a single
// writer: Load-then-Save from two processes keeps whichever wrote last, so a
// spawn landing inside a sweep's window had its pairing silently dropped and
// the session it started never retired.
func TestConcurrentRecordsAreNotLost(t *testing.T) {
	p := FilePairs{Path: filepath.Join(t.TempDir(), "pairs.json")}

	const n = 24
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			Record(p, fmt.Sprintf("watcher-%02d", i),
				Pair{BgID: fmt.Sprintf("bg%02d", i), Watching: fmt.Sprintf("cal%02d", i)})
		}(i)
	}
	wg.Wait()

	got := p.Load()
	if len(got) != n {
		t.Fatalf("lost rows to a concurrent write: have %d of %d: %v", len(got), n, got)
	}
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("watcher-%02d", i)
		if got[name].Watching != fmt.Sprintf("cal%02d", i) {
			t.Errorf("%s did not survive: %+v", name, got[name])
		}
	}
}

// Record is what spawn calls, and nothing covered it.
func TestRecordWritesAndReplaces(t *testing.T) {
	p := FilePairs{Path: filepath.Join(t.TempDir(), "pairs.json")}

	Record(p, "assistant", Pair{BgID: "bb", Watching: "aa"})
	if got := p.Load()["assistant"]; got.BgID != "bb" || got.Watching != "aa" {
		t.Fatalf("not recorded: %+v", got)
	}

	// A second assistant for a different caller replaces the first: the name is
	// the key, and the cap allows only one.
	Record(p, "assistant", Pair{BgID: "cc", Watching: "dd"})
	if got := p.Load()["assistant"]; got.BgID != "cc" || got.Watching != "dd" {
		t.Fatalf("not replaced: %+v", got)
	}
	if len(p.Load()) != 1 {
		t.Errorf("replacing should not accumulate rows: %v", p.Load())
	}
}

// Incomplete records are refused rather than written, because a row with no
// watched ref can never retire and would hold the cap forever.
func TestRecordRefusesAnIncompletePair(t *testing.T) {
	p := FilePairs{Path: filepath.Join(t.TempDir(), "pairs.json")}
	Record(p, "assistant", Pair{BgID: "bb"}) // no Watching
	Record(p, "", Pair{BgID: "bb", Watching: "aa"})
	if len(p.Load()) != 0 {
		t.Errorf("wrote an unretireable row: %v", p.Load())
	}
}

// A missing or unreadable file reads as empty rather than erroring, so a first
// run and a corrupt one both simply retire nothing.
func TestLoadOfAMissingFileIsEmpty(t *testing.T) {
	p := FilePairs{Path: filepath.Join(t.TempDir(), "nope.json")}
	if len(p.Load()) != 0 {
		t.Error("a missing file should read as no pairings")
	}
}

// `wf rm` calls this, so a removal leaves nothing behind for the next sweep to
// trip over, and a removal followed at once by a spawn does not race it.
func TestForgetDropsByRefOrName(t *testing.T) {
	p := FilePairs{Path: filepath.Join(t.TempDir(), "pairs.json")}
	Record(p, "assistant", Pair{BgID: "bb", Watching: "aa"})
	Record(p, "other", Pair{BgID: "cc", Watching: "dd"})

	Forget(p, "bb", "")
	if _, still := p.Load()["assistant"]; still {
		t.Error("not dropped by ref")
	}
	if len(p.Load()) != 1 {
		t.Errorf("dropped too much: %v", p.Load())
	}

	Forget(p, "", "other")
	if len(p.Load()) != 0 {
		t.Errorf("not dropped by name: %v", p.Load())
	}
}

// Forgetting something absent must not disturb the rest.
func TestForgetOfAnUnknownSessionIsHarmless(t *testing.T) {
	p := FilePairs{Path: filepath.Join(t.TempDir(), "pairs.json")}
	Record(p, "assistant", Pair{BgID: "bb", Watching: "aa"})
	Forget(p, "zz", "nobody")
	if len(p.Load()) != 1 {
		t.Errorf("unrelated row disturbed: %v", p.Load())
	}
}
