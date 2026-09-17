package forge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func stubGH(t *testing.T, body string) Client {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "gh")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ncat <<'JSON'\n"+body+"\nJSON\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return Client{Bin: bin}
}

// The regression: a body with no usable state unmarshals cleanly to "", and a
// caller asking "is it still OPEN" reads that as no. reconcile then stops and
// removes a live reviewer mid-review, taking its transcript, every 120 seconds.
func TestUnreadableStateIsAnErrorNotAnEmptyString(t *testing.T) {
	for _, body := range []string{`null`, `{}`, `{"other":1}`, `{"state":""}`, `{"state":"WAT"}`} {
		state, err := stubGH(t, body).PRState("o/r", "7")
		if err == nil {
			t.Errorf("body %s returned state %q with no error; that retires a live reviewer", body, state)
		}
		if state != "" {
			t.Errorf("body %s returned %q alongside an error", body, state)
		}
	}
}

func TestRecognisedStatesPassThrough(t *testing.T) {
	for _, want := range []string{"OPEN", "MERGED", "CLOSED"} {
		got, err := stubGH(t, `{"state":"`+want+`"}`).PRState("o/r", "7")
		if err != nil || got != want {
			t.Errorf("state %s: got %q, %v", want, got, err)
		}
	}
}

// Approval binds to a commit, so a verdict at an earlier head is stale.
func TestSignedAtMatchesOnlyTheReviewedHead(t *testing.T) {
	body := `{"comments":[{"body":"AGENT-REVIEW: APPROVED\nReviewed-Commit: abc1234def567"}]}`
	c := stubGH(t, body)
	for _, tc := range []struct {
		head string
		want bool
	}{
		{"abc1234def567", true},
		{"abc1234", true},           // the verdict abbreviates
		{"abc1234def5678901", true}, // the head is longer
		{"999888777", false},
		{"", false},
	} {
		got, err := c.SignedAt("o/r", "7", tc.head)
		if err != nil || got != tc.want {
			t.Errorf("head %q: got %v (%v), want %v", tc.head, got, err, tc.want)
		}
	}
}

func TestHeadOID(t *testing.T) {
	got, err := stubGH(t, `{"headRefOid":"8981d4309e2f"}`).HeadOID("o/r", "7")
	if err != nil || got != "8981d4309e2f" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestEveryReadNamesTheRepository(t *testing.T) {
	// PR numbers collide across repos and the wrong answer is a real answer
	// about a real pull request, so --repo is not optional.
	dir := t.TempDir()
	bin := filepath.Join(dir, "gh")
	out := filepath.Join(dir, "args")
	script := "#!/bin/sh\necho \"$@\" > " + out + "\necho '{\"state\":\"OPEN\"}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := (Client{Bin: bin}).PRState("owner/repo", "7"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	if !strings.Contains(string(b), "--repo owner/repo") {
		t.Errorf("the read did not name the repository: %s", b)
	}
}
