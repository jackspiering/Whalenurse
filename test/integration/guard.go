// Package integration holds the tests that run against a real Docker daemon.
// They run in CI on GitHub-hosted runners, never as part of `go test ./...`
// on a developer machine (the tests need -tags integration and
// WHALENURSE_INTEGRATION=1).
//
// SAFETY MODEL (fails closed), a port of test/m0/lib.sh. Everything in this
// package that changes anything goes through Guard, and each method refuses to
// act unless the compose project of its target matches ^whalenurse-test-...$:
//
//	Compose   docker compose, always with -p <project>; refuses -p, --project-name, down and rm
//	Down      the ONLY place that runs `down -v`, and only for matching projects
//	Ctr       stop/start/restart/kill/exec on a container whose project label matches
//	PullBase  pulls only the base images in BaseImages
//	Actor     wraps whalenurse's own docker.Actor so the healer under test cannot
//	          touch a container outside a matching project either
//
// Reads (inspect, ps, logs) use plain docker. This file has no build tag so
// the guard itself is unit-tested without Docker (guard_test.go).
package integration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/jackspiering/whalenurse/internal/config"
	"github.com/jackspiering/whalenurse/internal/docker"
)

// ProjectRE is the only compose project name the tests may act on.
var ProjectRE = regexp.MustCompile(`^whalenurse-test-[a-z0-9][a-z0-9_-]*$`)

// BaseImages are the only images the tests may pull.
var BaseImages = []string{"busybox:1.37"}

// Runner runs an external command and returns its stdout.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
}

// ExecRunner runs real commands with an environment that cannot redirect
// docker or compose at something else.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = cleanEnv(os.Environ())
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

func cleanEnv(env []string) []string {
	var out []string
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "COMPOSE_PROJECT_NAME", "COMPOSE_FILE", "COMPOSE_PROFILES", "COMPOSE_ENV_FILES", "COMPOSE_PATH_SEPARATOR", "DOCKER_CONTEXT", "DOCKER_CONFIG":
			continue
		}
		out = append(out, kv)
	}
	return out
}

// Guard performs the mutating Docker operations of the tests.
type Guard struct {
	R        Runner
	Projects []string // projects touched, for cleanup
}

// NewGuard returns a Guard that runs real commands.
func NewGuard() *Guard { return &Guard{R: ExecRunner{}} }

// ErrRefused wraps every refusal.
var ErrRefused = errors.New("REFUSED")

func refuse(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrRefused, fmt.Sprintf(format, a...))
}

// RequireProject checks a compose project name.
func RequireProject(p string) error {
	if p == "" || !ProjectRE.MatchString(p) {
		return refuse("project %q does not match %s", p, ProjectRE)
	}
	return nil
}

// SanitizeEnv refuses to run unless docker points at the local socket and the
// default context.
func (g *Guard) SanitizeEnv(ctx context.Context) error {
	switch h := os.Getenv("DOCKER_HOST"); h {
	case "", "unix:///var/run/docker.sock", "unix:///run/docker.sock":
	default:
		return refuse("DOCKER_HOST=%s: only the local socket is allowed", h)
	}
	out, err := g.R.Run(ctx, "docker", "context", "show")
	if err != nil {
		return refuse("cannot read the docker context: %v", err)
	}
	if c := strings.TrimSpace(out); c != "default" {
		return refuse("docker context is %q, expected \"default\"", c)
	}
	return nil
}

// Compose runs `docker compose -p <project> args...`.
func (g *Guard) Compose(ctx context.Context, project string, args ...string) (string, error) {
	if err := RequireProject(project); err != nil {
		return "", err
	}
	for _, a := range args {
		switch {
		case a == "down" || a == "rm":
			return "", refuse("compose %q is only allowed through Down", a)
		case strings.HasPrefix(a, "-p") || a == "--project-name" || strings.HasPrefix(a, "--project-name="):
			// -p* also catches the attached form (-pother); --pull and --progress start with two dashes.
			return "", refuse("caller-supplied project name (%s)", a)
		}
	}
	g.touch(project)
	return g.R.Run(ctx, "docker", append([]string{"compose", "-p", project}, args...)...)
}

// Down is the only `down -v` in the harness.
func (g *Guard) Down(ctx context.Context, project string) error {
	if err := RequireProject(project); err != nil {
		return err
	}
	_, err := g.R.Run(ctx, "docker", "compose", "-p", project, "down", "-v", "--timeout", "2", "--remove-orphans")
	return err
}

func (g *Guard) touch(p string) {
	for _, q := range g.Projects {
		if q == p {
			return
		}
	}
	g.Projects = append(g.Projects, p)
}

// ProjectOf reads the compose project label of a container.
func (g *Guard) ProjectOf(ctx context.Context, ctr string) (string, error) {
	out, err := g.R.Run(ctx, "docker", "inspect", "--format", `{{index .Config.Labels "com.docker.compose.project"}}`, ctr)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Ctr runs stop/start/restart/kill/exec on a container whose project label
// matches. For exec the arguments follow the container.
func (g *Guard) Ctr(ctx context.Context, verb, ctr string, args ...string) (string, error) {
	switch verb {
	case "stop", "start", "restart", "kill", "exec":
	default:
		return "", refuse("verb %q not allowed", verb)
	}
	if ctr == "" {
		return "", refuse("no container")
	}
	p, err := g.ProjectOf(ctx, ctr)
	if err != nil {
		return "", refuse("cannot determine the project of %q: %v", ctr, err)
	}
	if err := RequireProject(p); err != nil {
		return "", err
	}
	id, err := g.R.Run(ctx, "docker", "inspect", "--format", "{{.Id}}", ctr)
	if err != nil {
		return "", refuse("cannot resolve %q: %v", ctr, err)
	}
	id = strings.TrimSpace(id)
	if verb == "exec" {
		return g.R.Run(ctx, "docker", append([]string{"exec", id}, args...)...)
	}
	return g.R.Run(ctx, "docker", append([]string{verb}, append(args, id)...)...)
}

// PullBase pulls one of BaseImages.
func (g *Guard) PullBase(ctx context.Context, ref string) error {
	for _, b := range BaseImages {
		if b == ref {
			_, err := g.R.Run(ctx, "docker", "pull", "-q", ref)
			return err
		}
	}
	return refuse("pull of %q: not an allowed base image", ref)
}

// PreflightNoStrangers fails if whalenurse could act on anything outside a
// matching project: it refuses WHALENURSE_MODE=all and any container that
// carries whalenurse.enable=true outside a whalenurse-test-* project.
func (g *Guard) PreflightNoStrangers(ctx context.Context) error {
	if m := os.Getenv("WHALENURSE_MODE"); m != "" && m != string(config.ModeLabel) {
		return refuse("WHALENURSE_MODE=%s: integration tests run in label mode only", m)
	}
	out, err := g.R.Run(ctx, "docker", "ps", "-a", "--filter", "label=whalenurse.enable=true",
		"--format", `{{.ID}} {{.Label "com.docker.compose.project"}}`)
	if err != nil {
		return refuse("cannot list opted-in containers: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		id, project, _ := strings.Cut(line, " ")
		if !ProjectRE.MatchString(project) {
			return refuse("container %s is opted in (whalenurse.enable=true) but belongs to project %q", id, project)
		}
	}
	return nil
}

// Actor wraps whalenurse's own Actor: the healer under test can restart or
// start only containers of a matching project.
type Actor struct {
	Reader docker.Reader
	Inner  docker.Actor
}

func (a Actor) check(ctx context.Context, id string) error {
	c, err := a.Reader.Inspect(ctx, id)
	if err != nil {
		return refuse("cannot inspect %q: %v", id, err)
	}
	if p := c.Labels[docker.LabelProject]; !ProjectRE.MatchString(p) {
		return refuse("whalenurse tried to act on %q (project %q)", c.Name, p)
	}
	return nil
}

func (a Actor) Restart(ctx context.Context, id string) error {
	if err := a.check(ctx, id); err != nil {
		return err
	}
	return a.Inner.Restart(ctx, id)
}

func (a Actor) Start(ctx context.Context, id string) error {
	if err := a.check(ctx, id); err != nil {
		return err
	}
	return a.Inner.Start(ctx, id)
}

var _ docker.Actor = Actor{}
