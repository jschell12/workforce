package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jschell12/workforce/internal/transcript"
	"github.com/spf13/cobra"
)

func newTailCmd(env *Env) *cobra.Command {
	var follow bool
	var n int
	cmd := &cobra.Command{
		Use:   "tail <session>",
		Short: "Follow a session's transcript",
		Long: `Follow a session's transcript.

Reviewers run with disableRemoteControl, so they are invisible to the app and to
the agent list by design. This is how you watch one without giving that up.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			f, err := resolveTarget(env, args[0])
			if err != nil {
				return err
			}
			path := transcript.Find(f.Row.SessionID)
			if path == "" {
				return fmt.Errorf("no transcript on disk for %s [%s]", dash(f.Row.Name), f.Row.Ref())
			}
			fh, err := os.Open(path)
			if err != nil {
				return err
			}
			defer fh.Close()

			var rendered []string
			sc := bufio.NewScanner(fh)
			sc.Buffer(make([]byte, 0, 1<<20), 16<<20) // transcript lines are long
			for sc.Scan() {
				rendered = append(rendered, transcript.Render(sc.Bytes(), transcript.DefaultWidth)...)
			}
			if n > 0 && len(rendered) > n {
				rendered = rendered[len(rendered)-n:]
			}
			for _, l := range rendered {
				fmt.Fprintln(env.Out, l)
			}
			if !follow {
				return nil
			}

			fmt.Fprintf(env.Err, "  -- following %s [%s], ctrl-c to stop --\n", dash(f.Row.Name), f.Row.Ref())
			ctx, stop := signal.NotifyContext(c.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return followFile(ctx.Done(), fh, env)
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep watching for new output")
	cmd.Flags().IntVarP(&n, "lines", "n", 40, "lines of history to show first")
	return cmd
}

// followFile tails by byte offset rather than re-reading.
//
// The file only ever grows, and a partial final line is normal while a write is
// in flight, so an incomplete line is held back until it ends in a newline.
// Rendering half a JSON object produces nothing at best and a wrong line at
// worst, and the whole point of tailing a reviewer is to read what it actually
// said.
func followFile(done <-chan struct{}, fh *os.File, env *Env) error {
	offset, err := fh.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	var buf strings.Builder
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-done:
			return nil
		case <-tick.C:
		}
		fi, err := fh.Stat()
		if err != nil {
			return err
		}
		if fi.Size() < offset {
			// Truncated or replaced: start again from the beginning rather
			// than reading from an offset that now means something else.
			offset = 0
			buf.Reset()
		}
		if fi.Size() == offset {
			continue
		}
		chunk := make([]byte, fi.Size()-offset)
		read, err := fh.ReadAt(chunk, offset)
		if err != nil && err != io.EOF {
			return err
		}
		offset += int64(read)
		buf.Write(chunk[:read])
		text := buf.String()
		last := strings.LastIndexByte(text, '\n')
		if last < 0 {
			continue // nothing complete yet
		}
		for _, line := range strings.Split(text[:last], "\n") {
			if line == "" {
				continue
			}
			for _, l := range transcript.Render([]byte(line), transcript.DefaultWidth) {
				fmt.Fprintln(env.Out, l)
			}
		}
		buf.Reset()
		buf.WriteString(text[last+1:])
	}
}
