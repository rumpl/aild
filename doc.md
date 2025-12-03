# AILD - AI Linux Development Environment

AILD provides an interactive Linux container environment powered by BuildKit. It allows you to work in an isolated container with your local files, make changes, and optionally apply those changes back to your local filesystem.

## How It Works

### Overview

AILD uses BuildKit's frontend gateway API to create interactive containers. The workflow is:

1. Copy your current directory into a container
2. Drop you into an interactive shell
3. Let you make changes
4. Extract and apply changes back to your local filesystem

### Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                         Local Machine                           │
│  ┌───────────────┐                         ┌─────────────────┐  │
│  │  Current Dir  │ ◄───── tar extract ──── │   AILD CLI      │  │
│  │  (your files) │                         │                 │  │
│  └───────────────┘                         └────────┬────────┘  │
│         │                                           │           │
│         │ fsutil                                    │ BuildKit  │
│         │                                           │ Client    │
│         ▼                                           ▼           │
│  ┌─────────────────────────────────────────────────────────────┐│
│  │                      BuildKit Daemon                        ││
│  │  ┌───────────────────────────────────────────────────────┐  ││
│  │  │                  Gateway Container                     │  ││
│  │  │                                                        │  ││
│  │  │  ┌────────────────┐  ┌────────────────────────────┐   │  ││
│  │  │  │ Init Process   │  │  /workspace                │   │  ││
│  │  │  │ (sleep inf)    │  │  └── <your files>          │   │  ││
│  │  │  └────────────────┘  └────────────────────────────┘   │  ││
│  │  │                                                        │  ││
│  │  │  ┌────────────────┐  ┌────────────────────────────┐   │  ││
│  │  │  │ Shell Process  │  │ Tar Process (on export)    │   │  ││
│  │  │  │ /bin/sh (tty)  │  │ tar cf - .                 │   │  ││
│  │  │  └────────────────┘  └────────────────────────────┘   │  ││
│  │  └───────────────────────────────────────────────────────┘  ││
│  └─────────────────────────────────────────────────────────────┘│
└─────────────────────────────────────────────────────────────────┘
```

### Key Components

#### BuildKit Client Connection

AILD connects to a BuildKit daemon, defaulting to a Docker container named `buildkitd`:

```go
buildkitAddr := os.Getenv("BUILDKIT_HOST")
if buildkitAddr == "" {
    buildkitAddr = "docker-container://buildkitd"
}
c, err := client.New(ctx, buildkitAddr)
```

#### LLB (Low-Level Build) Definition

The container image is built using BuildKit's LLB:

```go
alpine := llb.Image("alpine:latest", llb.Platform(platform))
local := llb.Local(localNameContext)
workspace := alpine.File(
    llb.Copy(local, "/", containerWorkdir),
)
```

This creates a state that:
1. Starts from `alpine:latest`
2. Copies all files from the local context into `/workspace`

#### Gateway Container Lifecycle

This is the most critical part. BuildKit containers have a specific lifecycle tied to their **init process**:

1. **Container Creation**: A gateway container is created with the solved LLB reference mounted at `/`
2. **Init Process**: The first process started becomes the "init" process. When it exits, the container becomes unavailable
3. **Exec Processes**: Additional processes can be started via `container.Start()` while the init process runs

**The Key Insight**: We must keep an init process running while we need the container. AILD uses `sleep infinity`:

```go
// Start init process to keep container alive
initProc, err := ctr.Start(initCtx, gateway.StartRequest{
    Args: []string{"/bin/sh", "-c", "sleep infinity"},
})

// Run shell as separate process
runInteractiveShell(ctx, ctr)

// Extract tar as another process (container still alive!)
extractTarFromContainer(ctx, ctr, containerWorkdir)

// Now we can stop init
initCancel()
```

#### Interactive Shell

The shell runs with TTY support for a proper terminal experience:

```go
proc, err := ctr.Start(ctx, gateway.StartRequest{
    Args:   []string{"/bin/sh"},
    Cwd:    containerWorkdir,
    Tty:    true,
    Stdin:  cons,
    Stdout: cons,
    Stderr: cons,
})
```

Terminal handling:
- Raw mode is enabled for proper character-by-character input
- Terminal resize events are forwarded to the container process
- Terminal state is restored after the shell exits

#### Change Extraction

When the user chooses to apply changes, AILD:

1. Runs `tar` inside the container to create an archive of `/workspace`
2. Streams the tar data back to the client
3. Extracts the tar archive to the current working directory

```go
proc, err := ctr.Start(ctx, gateway.StartRequest{
    Args:   []string{"/bin/sh", "-c", "cd /workspace && tar cf - ."},
    Stdout: &buffer,
})
```

### Process Flow

```
┌──────────────┐
│    Start     │
└──────┬───────┘
       │
       ▼
┌──────────────────────┐
│ Connect to BuildKit  │
└──────────┬───────────┘
           │
           ▼
┌──────────────────────┐
│ Create LLB:          │
│ alpine + local files │
└──────────┬───────────┘
           │
           ▼
┌──────────────────────┐
│ Solve LLB            │
│ (build container)    │
└──────────┬───────────┘
           │
           ▼
┌──────────────────────┐
│ Create gateway       │
│ container            │
└──────────┬───────────┘
           │
           ▼
┌──────────────────────┐
│ Start init process   │
│ (sleep infinity)     │
└──────────┬───────────┘
           │
           ▼
┌──────────────────────┐
│ Start interactive    │
│ shell (/bin/sh)      │
└──────────┬───────────┘
           │
           ▼
┌──────────────────────┐
│ User works in shell  │
│ ... makes changes    │
│ ... exits shell      │
└──────────┬───────────┘
           │
           ▼
┌──────────────────────┐
│ Apply changes? [y/n] │
└──────────┬───────────┘
           │
     ┌─────┴─────┐
     │           │
     ▼           ▼
┌─────────┐ ┌─────────────────┐
│ No:     │ │ Yes:            │
│ Discard │ │ Run tar in ctr  │
└─────────┘ │ Extract locally │
            └────────┬────────┘
                     │
                     ▼
            ┌─────────────────┐
            │ Stop init       │
            │ Release ctr     │
            └─────────────────┘
```

## Usage

### Prerequisites

Start a BuildKit daemon:

```bash
docker run -d --name buildkitd --privileged moby/buildkit:latest
```

### Running AILD

```bash
# From any directory
./aild

# With custom BuildKit address
BUILDKIT_HOST=tcp://localhost:1234 ./aild
```

### Example Session

```
$ ./aild
/workspace # ls
myfile.txt  src/
/workspace # echo "hello" > newfile.txt
/workspace # exit

Do you want to apply the changes made in the container locally? [y/n]: y
Changes applied successfully.

$ ls
myfile.txt  newfile.txt  src/
```

## Technical Notes

### Why `sleep infinity`?

BuildKit's container implementation removes the container from its "running" map when the init process exits. Any subsequent `Exec` calls fail with "container not found". By keeping a `sleep infinity` process running, we maintain the ability to:
- Run the interactive shell
- Run the tar extraction
- All within the same container with the same filesystem state

### Why tar instead of BuildKit export?

BuildKit's export functionality (`client.ExporterLocal`) exports the **solved LLB state**, not the runtime modifications made in a gateway container. Gateway containers use an overlay filesystem where changes are ephemeral. The `tar` approach captures the actual modified filesystem.

### Security Considerations

- Tar extraction includes path traversal protection
- The container runs with the default BuildKit security settings
- Files are extracted with their original permissions from the container
