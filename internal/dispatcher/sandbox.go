// Package dispatcher consumes JetStream submit messages and runs untrusted
// code in a Docker sandbox (gVisor runsc in development, default runtime in
// tests/CI), then writes results back to Postgres and publishes a result
// summary to exec.result.<executionID>.
package dispatcher

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"

	rnats "github.com/ishaan-jindal/runnix/internal/nats"
)

// Sandbox resource bounds, matching the legacy posture (see vault Architecture).
const (
	sandboxMemory   = 128 * 1024 * 1024      // 128m
	sandboxMemorySw = 256 * 1024 * 1024      // 128m mem + 128m swap
	sandboxNanoCPUs = 500_000_000            // 0.5 CPU
	sandboxPids     = 32                     // pids-limit
	sandboxTmpSize  = 64 * 1024 * 1024       // tmpfs /tmp cap
	maxOutputBytes  = 64 * 1024              // stdout/stderr truncation cap
	containerTTL    = 5 * time.Second        // grace after SIGKILL before removal
	execLabelKey    = "com.runnix.execution" // label for sweep/cleanup
	tenantLabelKey  = "com.runnix.tenant"
)

// RunResult is the outcome of one sandboxed run.
type RunResult struct {
	ExitCode  int
	Stdout    string
	Stderr    string
	TimedOut  bool
	OOMKilled bool
}

// Runner executes one submission. Implementations must be safe for one
// concurrent Run at a time per instance.
type Runner interface {
	// EnsureImage makes the runner image available locally (pull if absent).
	EnsureImage(ctx context.Context) error
	// Run stages and executes msg, returning the outcome.
	Run(ctx context.Context, msg rnats.SubmitMessage) (RunResult, error)
	// Sweep force-removes leftover sandbox containers from crashed runs.
	Sweep(ctx context.Context) error
}

// dockerRunner runs submissions in per-execution containers. The container
// name embeds the execution id so a re-delivered (at-least-once) message that
// slips past the DB claim guard cannot silently re-run: a create error for an
// existing name is treated as "already running".
type dockerRunner struct {
	cli     *client.Client
	image   string
	runtime string // "" = daemon default; "runsc" = gVisor
}

// NewDockerRunner builds a Runner over the Docker daemon.
func NewDockerRunner(cli *client.Client, image, runtime string) Runner {
	return &dockerRunner{cli: cli, image: image, runtime: runtime}
}

// EnsureImage verifies the image exists locally, pulling it otherwise.
func (r *dockerRunner) EnsureImage(ctx context.Context) error {
	if _, err := r.cli.ImageInspect(ctx, r.image); err == nil {
		return nil
	}
	rc, err := r.cli.ImagePull(ctx, r.image, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("pull %s: %w", r.image, err)
	}
	defer func() { _ = rc.Close() }()
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return fmt.Errorf("pull %s: %w", r.image, err)
	}
	return nil
}

// Sweep removes any leftover runnix sandbox containers (crash leftovers).
func (r *dockerRunner) Sweep(ctx context.Context) error {
	list, err := r.cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", execLabelKey)),
	})
	if err != nil {
		return fmt.Errorf("list sandbox containers: %w", err)
	}
	for _, c := range list {
		_ = r.cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true})
	}
	return nil
}

// Run stages the submission and executes it in a fresh container.
//
// The work files travel as a tar over the container's stdin: /work is a tmpfs
// (writable despite the read-only rootfs) owned by the sandbox user, and the
// entrypoint extracts then runs. This avoids bind mounts entirely, so it works
// both when the dispatcher runs on the host and when it drives Docker through
// a mounted socket from inside its own container.
func (r *dockerRunner) Run(ctx context.Context, msg rnats.SubmitMessage) (RunResult, error) {
	pids := int64(sandboxPids)
	cfg := &container.Config{
		Image:           r.image,
		User:            "65534:65534", // nobody
		WorkingDir:      "/work",
		Entrypoint:      []string{"/bin/sh", "-c", "tar -x -C /work >/dev/null 2>&1; exec python /work/main.py < /work/stdin.txt"},
		AttachStdin:     true,
		OpenStdin:       true,
		StdinOnce:       true,
		NetworkDisabled: true,
		Labels: map[string]string{
			execLabelKey:   msg.ExecutionID,
			tenantLabelKey: msg.TenantID,
		},
	}
	host := &container.HostConfig{
		NetworkMode:    container.NetworkMode("none"),
		ReadonlyRootfs: true,
		Tmpfs: map[string]string{
			"/tmp":  fmt.Sprintf("rw,nosuid,nodev,size=%d,uid=65534,gid=65534", sandboxTmpSize),
			"/work": fmt.Sprintf("rw,nosuid,nodev,size=%d,uid=65534,gid=65534", sandboxTmpSize),
		},
		CapDrop: []string{"ALL"},
		Runtime: r.runtime,
		Resources: container.Resources{
			Memory:     sandboxMemory,
			MemorySwap: sandboxMemorySw,
			NanoCPUs:   sandboxNanoCPUs,
			PidsLimit:  &pids,
		},
	}

	name := "runnix-exec-" + msg.ExecutionID
	created, err := r.cli.ContainerCreate(ctx, cfg, host, nil, nil, name)
	if err != nil {
		// A leftover container with this name (crash) blocks creation. The
		// name is deterministic per execution id, so reclaim it.
		if removeErr := r.cli.ContainerRemove(ctx, name, container.RemoveOptions{Force: true}); removeErr == nil {
			created, err = r.cli.ContainerCreate(ctx, cfg, host, nil, nil, name)
		}
		if err != nil {
			return RunResult{}, fmt.Errorf("create container: %w", err)
		}
	}
	defer func() {
		_ = r.cli.ContainerRemove(context.Background(), created.ID, container.RemoveOptions{Force: true})
	}()

	hij, err := r.cli.ContainerAttach(ctx, created.ID, container.AttachOptions{Stream: true, Stdin: true})
	if err != nil {
		return RunResult{}, fmt.Errorf("attach: %w", err)
	}
	defer hij.Close()

	if err := r.cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return RunResult{}, fmt.Errorf("start container: %w", err)
	}
	if _, err := hij.Conn.Write(workArchive(msg.Source, msg.Stdin)); err != nil {
		return RunResult{}, fmt.Errorf("send work archive: %w", err)
	}
	_ = hij.CloseWrite()

	res, err := r.wait(ctx, created.ID, time.Duration(msg.TimeoutS)*time.Second)
	if err != nil {
		return res, err
	}

	out, errOut, err := r.readLogs(ctx, created.ID)
	if err != nil {
		return res, fmt.Errorf("read logs: %w", err)
	}
	info, err := r.cli.ContainerInspect(ctx, created.ID)
	if err != nil {
		return res, fmt.Errorf("inspect container: %w", err)
	}
	res.Stdout = truncateOutput(out)
	res.Stderr = truncateOutput(errOut)
	res.OOMKilled = info.State != nil && info.State.OOMKilled
	return res, nil
}

// workArchive builds a tar containing main.py and stdin.txt for the sandbox
// entrypoint to extract into /work.
func workArchive(source, stdin string) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, content := range map[string]string{
		"main.py":   source,
		"stdin.txt": stdin,
	} {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))})
		_, _ = tw.Write([]byte(content))
	}
	_ = tw.Close()
	return buf.Bytes()
}

// wait blocks until the container exits or the timeout elapses (killing it on
// timeout). It returns the exit code and whether the timeout fired.
func (r *dockerRunner) wait(ctx context.Context, id string, timeout time.Duration) (RunResult, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	waitCh, errCh := r.cli.ContainerWait(runCtx, id, container.WaitConditionNotRunning)
	var res RunResult
	select {
	case err := <-errCh:
		if !errors.Is(err, context.DeadlineExceeded) {
			return res, err
		}
		res.TimedOut = true
	case <-runCtx.Done():
		res.TimedOut = true
	case resp := <-waitCh:
		if resp.Error != nil {
			return res, fmt.Errorf("container error: %s", resp.Error.Message)
		}
		res.ExitCode = int(resp.StatusCode)
	}

	if res.TimedOut {
		// Hard-kill, then wait for the container to actually stop so logs
		// (partial output) are readable before we remove it.
		_ = r.cli.ContainerKill(context.Background(), id, "SIGKILL")
		select {
		case <-waitCh:
		case <-errCh:
		case <-time.After(containerTTL):
		}
	}
	return res, nil
}

// readLogs demultiplexes the container's stdout/stderr streams.
func (r *dockerRunner) readLogs(ctx context.Context, id string) (string, string, error) {
	rc, err := r.cli.ContainerLogs(ctx, id, container.LogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		return "", "", err
	}
	defer func() { _ = rc.Close() }()
	var out, errOut bytes.Buffer
	if _, err := stdcopy.StdCopy(&out, &errOut, rc); err != nil {
		return "", "", err
	}
	return out.String(), errOut.String(), nil
}

// truncateOutput caps output to maxOutputBytes with an overflow marker.
func truncateOutput(s string) string {
	if len(s) <= maxOutputBytes {
		return s
	}
	return s[:maxOutputBytes] + fmt.Sprintf("\n... [truncated %d bytes]", len(s)-maxOutputBytes)
}

var _ Runner = (*dockerRunner)(nil)

// classify maps a sandbox outcome to the executions status enum and an
// optional exit code for the DB.
func classify(res RunResult) (status string, exitCode *int, stderr string) {
	switch {
	case res.TimedOut:
		return "timeout", nil, res.Stderr
	case res.OOMKilled:
		return "failed", nil, strings.TrimSpace(res.Stderr) + "\n[container killed: out of memory]"
	case res.ExitCode == 0:
		return "succeeded", &res.ExitCode, res.Stderr
	default:
		return "failed", &res.ExitCode, res.Stderr
	}
}
