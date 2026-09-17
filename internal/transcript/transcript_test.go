package transcript

import (
	"strings"
	"testing"
)

func TestBookkeepingRowsAreSkipped(t *testing.T) {
	for _, row := range []string{
		`{"type":"custom-title","title":"x"}`,
		`{"type":"agent-setting"}`,
		`{"type":"permission-mode","mode":"acceptEdits"}`,
		`not json at all`,
	} {
		if got := Render([]byte(row), DefaultWidth); got != nil {
			t.Errorf("%s should render nothing, got %v", row, got)
		}
	}
}

// A string is a real prompt. A list is tool results coming back, which is most
// of the file and almost never what someone tailing wants.
func TestUserPromptsRenderButToolResultsDoNot(t *testing.T) {
	got := Render([]byte(`{"type":"user","message":{"content":"review PR 7"}}`), DefaultWidth)
	if len(got) != 1 || !strings.Contains(got[0], "review PR 7") {
		t.Fatalf("a prompt should render, got %v", got)
	}
	got = Render([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","content":"huge"}]}}`), DefaultWidth)
	if got != nil {
		t.Fatalf("tool results should be skipped, got %v", got)
	}
}

func TestAssistantTextAndToolUse(t *testing.T) {
	row := `{"type":"assistant","message":{"content":[
	  {"type":"text","text":"Looks fine.\n\nApproving."},
	  {"type":"tool_use","name":"Bash","input":{"command":"gh  pr   view 7"}},
	  {"type":"tool_use","name":"Read","input":{"file_path":"/tmp/x.go"}},
	  {"type":"tool_use","name":"Think","input":{}}]}}`
	got := Render([]byte(row), DefaultWidth)
	want := []string{"  Looks fine.", "  Approving.", "  → Bash: gh pr view 7", "  → Read: /tmp/x.go", "  → Think"}
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestBlankLinesDropped(t *testing.T) {
	got := Render([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"a\n\n\n  \nb"}]}}`), DefaultWidth)
	if len(got) != 2 {
		t.Fatalf("blank lines should be dropped, got %v", got)
	}
}

func TestFindReturnsEmptyForUnknownSession(t *testing.T) {
	if got := Find(""); got != "" {
		t.Errorf("empty session id should find nothing, got %q", got)
	}
	if got := Find("no-such-session-id-at-all"); got != "" {
		t.Errorf("got %q", got)
	}
}
