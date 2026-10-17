//go:build integration

package integration

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackspiering/whalenurse/internal/clock"
	"github.com/jackspiering/whalenurse/internal/config"
	"github.com/jackspiering/whalenurse/internal/daemon"
	"github.com/jackspiering/whalenurse/internal/docker"
	"github.com/jackspiering/whalenurse/internal/engine"
)

const testHost = "itest"

// Run with: WHALENURSE_INTEGRATION=1 go test -tags integration ./test/integration/
// CI does this on a GitHub-hosted runner. Do not run it on a Docker host you care about.
func requireIntegration(t *testing.T) (*Guard, *docker.Moby) {
	t.Helper()
	if os.Getenv("WHALENURSE_INTEGRATION") != "1" {
		t.Skip("set WHALENURSE_INTEGRATION=1 to run the integration tests (they need a disposable Docker host)")
	}
	ctx := context.Background()
	g := NewGuard()
	if err := g.SanitizeEnv(ctx); err != nil {
		t.Fatal(err)
	}
	if err := g.PreflightNoStrangers(ctx); err != nil {
		t.Fatal(err)
	}
	if err := g.PullBase(ctx, "busybox:1.37"); err != nil {
		t.Fatal(err)
	}
	cli, err := docker.NewMoby()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cli.Close() })
	return g, cli
}

type stack struct {
	t       *testing.T
	g       *Guard
	cli     *docker.Moby
	project string
	dir     string
}

func newStack(t *testing.T, g *Guard, cli *docker.Moby, name, compose string) *stack {
	t.Helper()
	var b [3]byte
	_, _ = rand.Read(b[:])
	s := &stack{t: t, g: g, cli: cli, project: "whalenurse-test-" + name + "-" + hex.EncodeToString(b[:]), dir: t.TempDir()}
	if err := RequireProject(s.project); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, "compose.yaml"), []byte(compose), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := g.Down(ctx, s.project); err != nil {
			t.Logf("cleanup of %s: %v", s.project, err)
		}
	})
	return s
}

func (s *stack) compose(args ...string) string {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	full := append([]string{"--project-directory", s.dir, "-f", filepath.Join(s.dir, "compose.yaml")}, args...)
	out, err := s.g.Compose(ctx, s.project, full...)
	if err != nil {
		s.t.Fatalf("compose %v: %v", args, err)
	}
	return out
}

func (s *stack) up(services ...string) {
	s.t.Helper()
	s.compose(append([]string{"up", "-d", "--pull", "never"}, services...)...)
}

// container returns the current container of a service.
func (s *stack) container(service string) docker.Container {
	s.t.Helper()
	ctx := context.Background()
	list, err := s.cli.List(ctx)
	if err != nil {
		s.t.Fatal(err)
	}
	for _, sm := range list {
		if sm.Labels[docker.LabelProject] == s.project && sm.Labels[docker.LabelService] == service {
			c, err := s.cli.Inspect(ctx, sm.ID)
			if err != nil {
				s.t.Fatal(err)
			}
			return c
		}
	}
	s.t.Fatalf("no container for service %s in %s", service, s.project)
	return docker.Container{}
}

func (s *stack) key(service string) string { return testHost + "/" + s.project + "/" + service + "/1" }

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

// app is a service that is healthy while /ok/flag exists. /ok is a tmpfs, so
// a restart clears the flag and the entrypoint recreates it: a restart repairs
// it, which is what the test asserts. Removing the flag makes it unhealthy.
func appService(name string) string {
	return fmt.Sprintf(`
  %s:
    image: busybox:1.37
    labels:
      whalenurse.enable: "true"
    tmpfs: ["/ok"]
    stop_grace_period: 2s
    command: ["sh", "-c", "touch /ok/flag; trap 'exit 0' TERM; while true; do sleep 1; done"]
    healthcheck:
      test: ["CMD", "test", "-f", "/ok/flag"]
      interval: 1s
      timeout: 1s
      retries: 1
      start_period: 1s
`, name)
}

func appStack(t *testing.T, g *Guard, cli *docker.Moby, name string) *stack {
	t.Helper()
	s := newStack(t, g, cli, name, "services:"+appService("app"))
	s.up()
	waitFor(t, "app to be healthy", 60*time.Second, func() bool { return s.container("app").Health == "healthy" })
	return s
}

func breakApp(t *testing.T, g *Guard, s *stack) {
	t.Helper()
	c := s.container("app")
	if _, err := g.Ctr(context.Background(), "exec", c.ID, "rm", "/ok/flag"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "app to turn unhealthy", 30*time.Second, func() bool { return s.container("app").Health == "unhealthy" })
}

// startDaemon runs the healer in-process with short timings. Its actor is
// wrapped in the guard so it cannot touch anything outside a test project.
func startDaemon(t *testing.T, g *Guard, cli *docker.Moby, dry bool) *engine.Recorder {
	t.Helper()
	cfg := config.Defaults()
	cfg.HostID = testHost
	cfg.Grace = 2 * time.Second
	cfg.PollInterval = 5 * time.Second
	cfg.Cooldown = 5 * time.Second
	cfg.StartupSettle = 0
	cfg.RateLimit = 100
	cfg.DryRun = dry
	var actor docker.Actor = Actor{Reader: cli, Inner: cli}
	if dry {
		actor = docker.DryRun{}
	}
	rec := &engine.Recorder{}
	bus := &engine.Bus{}
	bus.Subscribe(rec.Record)
	bus.Subscribe(func(e engine.Event) { t.Logf("event %-18s %s: %s", e.Type, e.Key, e.Message) })
	d := daemon.New(daemon.Options{Config: cfg, Reader: cli, Actor: actor, Clock: clock.Real{}, Bus: bus})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = d.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return rec
}

func countFor(rec *engine.Recorder, typ engine.EventType, key string) int {
	n := 0
	for _, e := range rec.Events() {
		if e.Type == typ && e.Key == key {
			n++
		}
	}
	return n
}

func TestUnhealthyContainerIsRestarted(t *testing.T) {
	g, cli := requireIntegration(t)
	s := appStack(t, g, cli, "restart")
	before := s.container("app")
	breakApp(t, g, s)

	rec := startDaemon(t, g, cli, false)
	waitFor(t, "the app to be restarted", 90*time.Second, func() bool {
		return s.container("app").StartedAt.After(before.StartedAt)
	})
	after := s.container("app")
	if after.ID != before.ID {
		t.Fatalf("a restart must keep the container ID: %s -> %s", before.ID, after.ID)
	}
	waitFor(t, "the app to be healthy again", 60*time.Second, func() bool { return s.container("app").Health == "healthy" })
	waitFor(t, "the Recovered event", 60*time.Second, func() bool { return countFor(rec, engine.Recovered, s.key("app")) == 1 })
	if n := countFor(rec, engine.ActionStarted, s.key("app")); n != 1 {
		t.Fatalf("want exactly one restart, got %d", n)
	}
}

func TestDryRunDoesNothing(t *testing.T) {
	g, cli := requireIntegration(t)
	s := appStack(t, g, cli, "dry")
	before := s.container("app")
	breakApp(t, g, s)

	rec := startDaemon(t, g, cli, true)
	waitFor(t, "the dry-run decision", 60*time.Second, func() bool {
		for _, e := range rec.Events() {
			if e.Type == engine.ActionResult && e.Key == s.key("app") && e.Attrs["dry_run"] == "true" {
				return true
			}
		}
		return false
	})
	time.Sleep(15 * time.Second) // several polls and cooldowns
	after := s.container("app")
	if after.ID != before.ID || !after.StartedAt.Equal(before.StartedAt) || after.RestartCount != before.RestartCount {
		t.Fatalf("dry-run changed the container: before %+v after %+v", before.StartedAt, after.StartedAt)
	}
	if after.Health != "unhealthy" {
		t.Fatalf("health %q: dry-run must leave it broken", after.Health)
	}
}

// TestDependentWithParentGoneIsNotRestarted is E1 end to end: restarting a
// network_mode dependent whose parent was recreated turns it from running into
// exited. The dependent here is also unhealthy, so a naive healer would
// restart it. whalenurse must record "needs recreate" and leave it alone.
func TestDependentWithParentGoneIsNotRestarted(t *testing.T) {
	g, cli := requireIntegration(t)
	const loop = `["sh", "-c", "trap 'exit 0' TERM; while true; do sleep 1; done"]`
	s := newStack(t, g, cli, "dep", `
services:
  parent:
    image: busybox:1.37
    labels:
      whalenurse.enable: "true"
    stop_grace_period: 2s
    command: `+loop+`
  dep:
    image: busybox:1.37
    network_mode: service:parent
    labels:
      whalenurse.enable: "true"
    stop_grace_period: 2s
    command: `+loop+`
    healthcheck:
      test: ["CMD", "false"]
      interval: 1s
      timeout: 1s
      retries: 1
`)
	s.up()
	oldParent := s.container("parent")
	dep := s.container("dep")
	if docker.ContainerRef(dep.NetworkMode) != oldParent.ID {
		t.Fatalf("setup: dependent network mode %q, parent %s", dep.NetworkMode, oldParent.ID)
	}

	s.compose("up", "-d", "--force-recreate", "--no-deps", "--pull", "never", "parent")
	newParent := s.container("parent")
	if newParent.ID == oldParent.ID {
		t.Fatal("setup: the parent was not recreated")
	}
	dep = s.container("dep")
	if docker.ContainerRef(dep.NetworkMode) != oldParent.ID || !dep.Running {
		t.Fatalf("setup: the dependent should be running with a stale network mode: %+v", dep)
	}
	waitFor(t, "the dependent to be unhealthy", 30*time.Second, func() bool { return s.container("dep").Health == "unhealthy" })

	rec := startDaemon(t, g, cli, false)
	waitFor(t, "NeedsRecreate for the dependent", 60*time.Second, func() bool {
		return countFor(rec, engine.NeedsRecreate, s.key("dep")) >= 1
	})
	time.Sleep(20 * time.Second) // let polls and the unhealthy healthcheck tempt it

	after := s.container("dep")
	if after.ID != dep.ID || !after.StartedAt.Equal(dep.StartedAt) || !after.Running {
		t.Fatalf("the dependent was touched (ID %s->%s, started %v->%v, status %s)", dep.ID, after.ID, dep.StartedAt, after.StartedAt, after.Status)
	}
	if n := countFor(rec, engine.NeedsRecreate, s.key("dep")); n != 1 {
		t.Fatalf("NeedsRecreate should be recorded once, got %d", n)
	}
	for _, typ := range []engine.EventType{engine.ActionStarted, engine.IncidentOpened, engine.GaveUp} {
		if n := countFor(rec, typ, s.key("dep")); n != 0 {
			t.Fatalf("%d %s events for the dependent", n, typ)
		}
	}
}

// buildBinary compiles cmd/whalenurse with CGO_ENABLED=0.
func buildBinary(t *testing.T) string {
	t.Helper()
	root, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").Output()
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "whalenurse")
	cmd := exec.Command("go", "build", "-trimpath", "-o", out, "./cmd/whalenurse")
	cmd.Dir = strings.TrimSpace(string(root))
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, b)
	}
	return out
}

// TestBinaryDryRunLogsDecisions is the milestone's done-criterion: `whalenurse
// run --dry-run` against a test stack logs the expected decisions and changes
// nothing; `whalenurse healthcheck` passes while it runs.
func TestBinaryDryRunLogsDecisions(t *testing.T) {
	g, cli := requireIntegration(t)
	s := appStack(t, g, cli, "bin")
	before := s.container("app")
	breakApp(t, g, s)

	bin := buildBinary(t)
	sock := filepath.Join(t.TempDir(), "w.sock")
	env := append(os.Environ(),
		"WHALENURSE_HOST_ID="+testHost, "WHALENURSE_GRACE=2s", "WHALENURSE_POLL_INTERVAL=5s", "WHALENURSE_COOLDOWN=5s",
		"WHALENURSE_STARTUP_SETTLE=0s", "WHALENURSE_CONTROL_SOCKET="+sock)
	cmd := exec.Command(bin, "run", "--dry-run")
	cmd.Env = env
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	lines := make(chan map[string]any, 256)
	go func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var m map[string]any
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				t.Log(sc.Text())
				lines <- m
			}
		}
		close(lines)
	}()

	want := map[string]bool{"dry-run banner": false, "incident opened": false, "would restart": false, "dry-run result": false}
	deadline := time.After(90 * time.Second)
loop:
	for {
		select {
		case m, ok := <-lines:
			if !ok {
				t.Fatal("whalenurse exited early")
			}
			msg, _ := m["msg"].(string)
			key, _ := m["key"].(string)
			switch {
			case strings.HasPrefix(msg, "DRY RUN"):
				want["dry-run banner"] = true
			case m["type"] == "IncidentOpened" && key == s.key("app"):
				want["incident opened"] = true
			case msg == "dry-run: would act" && m["verb"] == "restart":
				want["would restart"] = true
			case m["type"] == "ActionResult" && key == s.key("app") && m["attr.dry_run"] == "true":
				want["dry-run result"] = true
			}
			all := true
			for _, v := range want {
				all = all && v
			}
			if all {
				break loop
			}
		case <-deadline:
			t.Fatalf("timed out; saw %v", want)
		}
	}

	hc := exec.Command(bin, "healthcheck")
	hc.Env = env
	if out, err := hc.CombinedOutput(); err != nil {
		t.Fatalf("healthcheck: %v\n%s", err, out)
	}

	after := s.container("app")
	if after.ID != before.ID || !after.StartedAt.Equal(before.StartedAt) || after.Health != "unhealthy" {
		t.Fatalf("--dry-run changed the container: %+v", after)
	}

	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("whalenurse did not exit cleanly on SIGTERM: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("whalenurse did not stop on SIGTERM")
	}
}

// TestImage runs the built image the way the docs say to: read-only rootfs,
// the socket's group via group_add, a writable /run, no capabilities. It must
// become healthy through its own HEALTHCHECK and heal a test container.
func TestImage(t *testing.T) {
	img := os.Getenv("WHALENURSE_TEST_IMAGE")
	g, cli := requireIntegration(t)
	if img == "" {
		t.Skip("set WHALENURSE_TEST_IMAGE to a locally built image to test it")
	}
	if !strings.HasPrefix(img, "whalenurse-test/") {
		t.Fatalf("image %q: only whalenurse-test/* local tags are used", img)
	}
	fi, err := os.Stat("/var/run/docker.sock")
	if err != nil {
		t.Fatal(err)
	}
	gid := fi.Sys().(*syscall.Stat_t).Gid

	s := newStack(t, g, cli, "image", fmt.Sprintf(`
services:
  whalenurse:
    image: %s
    read_only: true
    cap_drop: [ALL]
    security_opt: ["no-new-privileges:true"]
    group_add: ["%d"]
    tmpfs: ["/run:uid=65532,gid=65532,mode=0700"]
    volumes: ["/var/run/docker.sock:/var/run/docker.sock"]
    environment:
      WHALENURSE_HOST_ID: %s
      WHALENURSE_GRACE: 2s
      WHALENURSE_POLL_INTERVAL: 5s
      WHALENURSE_COOLDOWN: 5s
      WHALENURSE_STARTUP_SETTLE: 0s
`, img, gid, testHost)+appService("app"))
	s.up("app")
	waitFor(t, "app to be healthy", 60*time.Second, func() bool { return s.container("app").Health == "healthy" })
	before := s.container("app")
	breakApp(t, g, s)
	s.up("whalenurse")

	if !s.container("whalenurse").Running {
		t.Fatal("whalenurse is not running")
	}
	if ro := inspectReadonly(t, s.container("whalenurse").ID); !ro {
		t.Fatal("the image test must run with a read-only root filesystem")
	}
	waitFor(t, "the app to be restarted by the containerized whalenurse", 90*time.Second, func() bool {
		return s.container("app").StartedAt.After(before.StartedAt)
	})
	waitFor(t, "the image HEALTHCHECK to pass", 90*time.Second, func() bool { return s.container("whalenurse").Health == "healthy" })
}

func inspectReadonly(t *testing.T, id string) bool {
	t.Helper()
	out, err := ExecRunner{}.Run(context.Background(), "docker", "inspect", "--format", "{{.HostConfig.ReadonlyRootfs}}", id)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out) == "true"
}
