package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/moby/buildkit/client"
	_ "github.com/moby/buildkit/client/connhelper/dockercontainer"
	"github.com/moby/buildkit/client/llb"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/tonistiigi/fsutil"
	"golang.org/x/sync/errgroup"
)

const (
	localNameContext    = "context"
	containerWorkdir    = "/workspace"
	defaultBuildkitAddr = "docker-container://buildkitd"
)

func run(agent string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle interrupt signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current directory: %w", err)
	}

	buildkitAddr := os.Getenv("BUILDKIT_HOST")
	if buildkitAddr == "" {
		buildkitAddr = defaultBuildkitAddr
	}

	c, err := client.New(ctx, buildkitAddr)
	if err != nil {
		return fmt.Errorf("failed to connect to buildkit at %s: %w\n\nTo start a BuildKit container, run:\n  docker run -d --name buildkitd --privileged moby/buildkit:latest", buildkitAddr, err)
	}
	defer func() { _ = c.Close() }()

	localFS, err := fsutil.NewFS(cwd)
	if err != nil {
		return fmt.Errorf("failed to create local filesystem: %w", err)
	}

	solveOpt := client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			localNameContext: localFS,
		},
	}

	// Progress channel - drain silently for now
	eg, egCtx := errgroup.WithContext(ctx)
	statusCh := make(chan *client.SolveStatus)

	eg.Go(func() error {
		for range statusCh {
		}
		return nil
	})

	// Channel to pass tar data out of the build function
	tarDataCh := make(chan []byte, 1)
	defer close(tarDataCh)

	buildFunc := func(ctx context.Context, gw gateway.Client) (*gateway.Result, error) {
		return buildWithInteractiveContainer(ctx, agent, gw, tarDataCh)
	}

	eg.Go(func() error {
		_, err := c.Build(egCtx, solveOpt, "aild", buildFunc, statusCh)
		return err
	})

	if err := eg.Wait(); err != nil {
		return fmt.Errorf("build failed: %w", err)
	}

	tarData := <-tarDataCh
	if tarData == nil {
		return nil
	}

	if err := extractTarToDir(tarData, cwd); err != nil {
		return fmt.Errorf("failed to apply changes: %w", err)
	}
	fmt.Println("Changes applied successfully.")

	return nil
}

// buildWithInteractiveContainer creates a container with the local context,
// runs an interactive shell, and extracts any changes made.
func buildWithInteractiveContainer(ctx context.Context, agent string, gw gateway.Client, tarDataCh chan<- []byte) (*gateway.Result, error) {
	platform := ocispecs.Platform{
		OS:           "linux",
		Architecture: runtime.GOARCH,
	}

	// Build LLB: alpine base + local files copied to /workspace
	alpine := llb.Image("docker/cagent:latest", llb.Platform(platform))
	local := llb.Local(localNameContext)
	workspace := alpine.File(
		llb.Copy(local, "/", containerWorkdir),
		llb.WithCustomName("copying local context to container"),
	)

	def, err := workspace.Marshal(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal LLB: %w", err)
	}

	res, err := gw.Solve(ctx, gateway.SolveRequest{
		Definition: def.ToPB(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to solve: %w", err)
	}

	ref, err := res.SingleRef()
	if err != nil {
		return nil, fmt.Errorf("failed to get reference: %w", err)
	}

	ctr, err := gw.NewContainer(ctx, gateway.NewContainerRequest{
		Mounts: []gateway.Mount{
			{
				Dest: "/",
				Ref:  ref,
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create container: %w", err)
	}
	defer func() { _ = ctr.Release(ctx) }()

	// Start a long-running init process to keep the container alive.
	// BuildKit containers become unavailable when the init process exits,
	// so we need this to run additional processes (shell, tar) afterward.
	initCtx, initCancel := context.WithCancel(ctx)
	defer initCancel()

	initProc, err := ctr.Start(initCtx, gateway.StartRequest{
		Args: []string{"/bin/sh", "-c", "sleep infinity"},
		Env:  []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		Cwd:  "/",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to start init process: %w", err)
	}

	initDone := make(chan error, 1)
	go func() {
		initDone <- initProc.Wait()
	}()

	// Run the interactive shell
	if err := runInteractiveShell(ctx, agent, ctr); err != nil {
		initCancel()
		return nil, err
	}

	// Ask user if they want to apply changes
	fmt.Println()
	if !askYesNo("Do you want to apply the changes made in the container locally?") {
		fmt.Println("Changes discarded.")
		initCancel()
		tarDataCh <- nil
		return gateway.NewResult(), nil
	}

	// Extract changes while container is still alive
	tarData, err := extractTarFromContainer(ctx, ctr, containerWorkdir)
	if err != nil {
		initCancel()
		return nil, fmt.Errorf("failed to extract changes: %w", err)
	}

	initCancel()
	<-initDone

	tarDataCh <- tarData
	return gateway.NewResult(), nil
}
