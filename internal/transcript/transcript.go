// Package transcript renders a session's conversation log.
//
// It exists because a reviewer runs with disableRemoteControl and is therefore
// invisible to the app by design. This is how you watch one without giving that
// up. `claude logs` is raw ANSI terminal replay and is not readable; the
// transcript is line-delimited JSON and is.
package transcript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Find locates a session's .jsonl under ~/.claude/projects.
//
// By session id rather than by guessing the directory, because that name is a
// mangled form of the working directory and a reviewer's cwd is not always the
// one you would guess.
func Find(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	root := filepath.Join(home, ".claude", "projects")
	dirs, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	for _, d := range dirs {
		f := filepath.Join(root, d.Name(), sessionID+".jsonl")
		if fi, err := os.Stat(f); err == nil && !fi.IsDir() {
			return f
		}
	}
	return ""
}

type event struct {
	Type    string `json:"type"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type block struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	Name  string          `json:"name"`
	Input map[string]any  `json:"input"`
	_     json.RawMessage `json:"-"`
}

// DefaultWidth bounds a rendered line.
const DefaultWidth = 150

// Render turns one transcript row into at most a few lines, or none to skip it.
//
// Everything other than user and assistant rows is bookkeeping: titles, agent
// settings, permission modes, attachments, latches. Rendering them would bury
// the conversation in the thing that exists to show the conversation.
func Render(line []byte, width int) []string {
	var e event
	if err := json.Unmarshal(line, &e); err != nil {
		return nil
	}
	if e.Type != "user" && e.Type != "assistant" {
		return nil
	}

	if e.Type == "user" {
		// A string is a real prompt. A list is tool results coming back, which
		// is the bulk of the file and almost never what someone tailing wants.
		var s string
		if json.Unmarshal(e.Message.Content, &s) == nil && strings.TrimSpace(s) != "" {
			return []string{"» " + clip(strings.TrimSpace(s), width)}
		}
		return nil
	}

	var blocks []block
	if json.Unmarshal(e.Message.Content, &blocks) != nil {
		return nil
	}
	var out []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			for _, l := range strings.Split(strings.TrimSpace(b.Text), "\n") {
				if strings.TrimSpace(l) != "" {
					out = append(out, "  "+clip(l, width))
				}
			}
		case "tool_use":
			name := b.Name
			if name == "" {
				name = "?"
			}
			// The one field worth showing differs per tool; command and
			// file_path cover most of what a session actually does.
			hint := ""
			for _, k := range []string{"command", "file_path", "pattern", "description"} {
				if v, ok := b.Input[k]; ok {
					if s, ok := v.(string); ok && s != "" {
						hint = strings.Join(strings.Fields(s), " ")
						break
					}
				}
			}
			line := "  → " + name
			if hint != "" {
				line += ": " + clip(hint, max(8, width-len(name)-6))
			}
			out = append(out, line)
		}
	}
	return out
}

func clip(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n]
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
