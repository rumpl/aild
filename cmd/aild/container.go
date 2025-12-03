package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"

	"github.com/containerd/console"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"golang.org/x/term"
)

// runInteractiveShell starts an interactive shell in the container.
func runInteractiveShell(ctx context.Context, agent string, ctr gateway.Container) error {
	cons := console.Current()
	if cons == nil {
		return fmt.Errorf("no console available")
	}

	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("failed to set terminal raw mode: %w", err)
	}

	proc, err := ctr.Start(ctx, gateway.StartRequest{
		Args:   []string{"/cagent", "run", agent},
		Env:    []string{"TERM=" + os.Getenv("TERM"), "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "ANTHROPIC_API_KEY=" + os.Getenv("ANTHROPIC_API_KEY"), "TERM=" + os.Getenv("TERM"), "COLORTERM=" + os.Getenv("COLORTERM")},
		Cwd:    containerWorkdir,
		Tty:    true,
		Stdin:  cons,
		Stdout: cons,
		Stderr: cons,
	})
	if err != nil {
		_ = term.Restore(fd, oldState)
		return fmt.Errorf("failed to start shell: %w", err)
	}

	// Forward terminal resize events
	resizeCtx, resizeCancel := context.WithCancel(ctx)
	go func() {
		for {
			select {
			case <-resizeCtx.Done():
				return
			default:
				size, err := cons.Size()
				if err != nil {
					return
				}
				_ = proc.Resize(ctx, gateway.WinSize{
					Rows: uint32(size.Height),
					Cols: uint32(size.Width),
				})
			}
		}
	}()

	procErr := proc.Wait()
	resizeCancel()

	_ = term.Restore(fd, oldState)

	return procErr
}

// extractTarFromContainer runs tar inside the container to capture the workspace.
func extractTarFromContainer(ctx context.Context, ctr gateway.Container, path string) ([]byte, error) {
	var buf bytes.Buffer

	proc, err := ctr.Start(ctx, gateway.StartRequest{
		Args:   []string{"/bin/sh", "-c", fmt.Sprintf("cd %s && tar cf - .", path)},
		Env:    []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		Cwd:    "/",
		Stdout: &nopWriteCloser{&buf},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to start tar process: %w", err)
	}

	if err := proc.Wait(); err != nil {
		return nil, fmt.Errorf("tar process failed: %w", err)
	}

	return buf.Bytes(), nil
}

// nopWriteCloser wraps an io.Writer to satisfy io.WriteCloser.
type nopWriteCloser struct {
	io.Writer
}

func (nopWriteCloser) Close() error { return nil }
