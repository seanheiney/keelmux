// read-child is a finite owned PTY fixture. It never invokes a provider or reads
// credentials. Its private measurements let product tests distinguish refusal
// at the bridge from bytes or a resize that actually reached the native child.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/charmbracelet/x/term"
)

const output = "\x1b[36mKEELMUX_READ_CHILD café 世界 🦭\x1b[0m\r\n"

type measurement struct {
	Tick          uint64 `json:"tick"`
	ReceivedBytes uint64 `json:"received_bytes"`
	Rows          int    `json:"rows"`
	Cols          int    `json:"cols"`
	SizeChanges   uint64 `json:"size_changes"`
	WinchSignals  uint64 `json:"winch_signals"`
	Quiet         bool   `json:"quiet"`
}

func main() {
	if len(os.Args) != 4 {
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2], os.Args[3]); err != nil {
		// No private path or input bytes appear in diagnostics.
		fmt.Fprintln(os.Stderr, "owned read child failed")
		os.Exit(1)
	}
}

func run(metrics, input, quiet string) error {
	state, err := term.MakeRaw(os.Stdin.Fd())
	if err != nil {
		return err
	}
	defer term.Restore(os.Stdin.Fd(), state)
	received, err := os.OpenFile(input, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer received.Close()
	var count atomic.Uint64
	inputError := make(chan error, 1)
	go func() {
		var buffer [256]byte
		for {
			n, err := os.Stdin.Read(buffer[:])
			if n > 0 {
				if count.Add(uint64(n)) > 4096 {
					inputError <- io.ErrShortBuffer
					return
				}
				if _, err := received.Write(buffer[:n]); err != nil {
					inputError <- err
					return
				}
			}
			if err != nil {
				inputError <- err
				return
			}
		}
	}()
	cols, rows, err := term.GetSize(os.Stdin.Fd())
	if err != nil || cols <= 0 || rows <= 0 {
		return fmt.Errorf("initial terminal size unavailable")
	}
	m := measurement{Rows: rows, Cols: cols}
	resizes := make(chan os.Signal, 8)
	signal.Notify(resizes, syscall.SIGWINCH)
	defer signal.Stop(resizes)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	limit := time.NewTimer(90 * time.Second)
	defer limit.Stop()
	for {
		select {
		case <-limit.C:
			return nil
		case err := <-inputError:
			return err
		case <-resizes:
			// A resize followed by restoration between polls still leaves a
			// window-change signal, so a transient native resize cannot pass.
			m.WinchSignals++
		case <-ticker.C:
			cols, rows, err := term.GetSize(os.Stdin.Fd())
			if err != nil {
				return err
			}
			if cols != m.Cols || rows != m.Rows {
				m.SizeChanges++
			}
			m.Cols, m.Rows = cols, rows
			m.Tick++
			m.ReceivedBytes = count.Load()
			_, err = os.Lstat(quiet)
			m.Quiet = err == nil
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			if !m.Quiet {
				if _, err := io.WriteString(os.Stdout, output); err != nil {
					return err
				}
			}
			bytes, err := json.Marshal(m)
			if err != nil {
				return err
			}
			// Only paths supplied by this fixture's owner are used. Rename lets the
			// test observe a complete measurement while the child remains alive.
			if err := os.WriteFile(metrics+".next", bytes, 0600); err != nil {
				return err
			}
			if err := os.Rename(metrics+".next", metrics); err != nil {
				return err
			}
		}
	}
}
