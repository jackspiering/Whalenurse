package integration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackspiering/whalenurse/internal/docker"
)

// stub is a Runner that records argv and answers inspect lookups from a table.
type stub struct {
	calls    []string
	projects map[string]string // container -> compose project label
	context  string
	psOut    string
}

func (s *stub) Run(_ context.Context, name string, args ...string) (string, error) {
	line := name + " " + strings.Join(args, " ")
	s.calls = append(s.calls, line)
	switch {
	case strings.HasPrefix(line, "docker context show"):
		return s.context + "\n", nil
	case strings.HasPrefix(line, "docker inspect --format {{index .Config.Labels"):
		p, ok := s.projects[args[len(args)-1]]
		if !ok {
			return "", errors.New("no such container")
		}
		return p + "\n", nil
	case strings.HasPrefix(line, "docker inspect --format {{.Id}}"):
		return "id-" + args[len(args)-1] + "\n", nil
	case strings.HasPrefix(line, "docker ps"):
		return s.psOut, nil
	}
	return "", nil
}

func (s *stub) mutations() []string {
	var out []string
	for _, c := range s.calls {
		if !strings.HasPrefix(c, "docker inspect") && !strings.HasPrefix(c, "docker context") && !strings.HasPrefix(c, "docker ps") {
			out = append(out, c)
		}
	}
	return out
}

func refused(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("want a REFUSED error, got %v", err)
	}
}

var badProjects = []string{
	"", "whalenurse-test-", "whalenurse-test", "prod", "default", "whalenurse", "whalenurse-m0-e1", "xwhalenurse-test-a",
	"whalenurse-test-A", "whalenurse-test-a b", "whalenurse-test-a/b", "whalenurse-test-a\nprod", "whalenurse-test--x", "WHALENURSE-TEST-a",
	"whalenurse-test-a;rm", "../whalenurse-test-a", "whalenurse-test-é",
}

func TestProjectRegexRejectsBadNames(t *testing.T) {
	for _, p := range badProjects {
		if err := RequireProject(p); err == nil {
			t.Errorf("project %q was accepted", p)
		}
	}
	for _, p := range []string{"whalenurse-test-a", "whalenurse-test-heal-3f2a", "whalenurse-test-0_x-y"} {
		if err := RequireProject(p); err != nil {
			t.Errorf("project %q was refused: %v", p, err)
		}
	}
}

func TestComposeRefusesBadProjectsAndSmuggling(t *testing.T) {
	for _, p := range badProjects {
		s := &stub{}
		g := &Guard{R: s}
		_, err := g.Compose(context.Background(), p, "ps")
		refused(t, err)
		refused(t, g.Down(context.Background(), p))
		if len(s.calls) != 0 {
			t.Errorf("project %q reached docker: %v", p, s.calls)
		}
	}
	good := "whalenurse-test-a"
	for _, args := range [][]string{
		{"-p", "prod", "ps"}, {"-pprod", "ps"}, {"--project-name", "prod"}, {"--project-name=prod", "ps"},
		{"down"}, {"-f", "x.yml", "down", "-v"}, {"rm", "-f"}, {"up", "-d", "rm"},
	} {
		s := &stub{}
		g := &Guard{R: s}
		_, err := g.Compose(context.Background(), good, args...)
		refused(t, err)
		if len(s.calls) != 0 {
			t.Errorf("%v reached docker: %v", args, s.calls)
		}
	}
	// --pull and --progress are legitimate and start with two dashes.
	s := &stub{}
	g := &Guard{R: s}
	if _, err := g.Compose(context.Background(), good, "up", "-d", "--pull", "never", "--progress", "plain"); err != nil {
		t.Fatal(err)
	}
	if got := s.calls[0]; got != "docker compose -p whalenurse-test-a up -d --pull never --progress plain" {
		t.Fatal(got)
	}
	if err := g.Down(context.Background(), good); err != nil {
		t.Fatal(err)
	}
	if got := s.calls[1]; got != "docker compose -p whalenurse-test-a down -v --timeout 2 --remove-orphans" {
		t.Fatal(got)
	}
}

func TestCtrChecksTheContainersProjectFirst(t *testing.T) {
	s := &stub{projects: map[string]string{"ours": "whalenurse-test-a", "theirs": "prod", "m0": "whalenurse-m0-e1", "plain": ""}}
	g := &Guard{R: s}
	ctx := context.Background()
	for _, ctr := range []string{"theirs", "m0", "plain", "unknown", ""} {
		_, err := g.Ctr(ctx, "restart", ctr)
		refused(t, err)
	}
	if m := s.mutations(); len(m) != 0 {
		t.Fatalf("mutations reached docker: %v", m)
	}
	for _, verb := range []string{"rm", "prune", "pause", "update", ""} {
		_, err := g.Ctr(ctx, verb, "ours")
		refused(t, err)
	}
	if _, err := g.Ctr(ctx, "restart", "ours"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Ctr(ctx, "exec", "ours", "rm", "/ok/flag"); err != nil {
		t.Fatal(err)
	}
	want := []string{"docker restart id-ours", "docker exec id-ours rm /ok/flag"}
	if got := s.mutations(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("%v", got)
	}
}

func TestPullOnlyBaseImages(t *testing.T) {
	s := &stub{}
	g := &Guard{R: s}
	refused(t, g.PullBase(context.Background(), "alpine:latest"))
	refused(t, g.PullBase(context.Background(), "busybox:1.37; rm -rf /"))
	if len(s.calls) != 0 {
		t.Fatal(s.calls)
	}
	if err := g.PullBase(context.Background(), "busybox:1.37"); err != nil {
		t.Fatal(err)
	}
}

func TestSanitizeEnv(t *testing.T) {
	ctx := context.Background()
	t.Setenv("DOCKER_HOST", "")
	if err := (&Guard{R: &stub{context: "default"}}).SanitizeEnv(ctx); err != nil {
		t.Fatal(err)
	}
	refused(t, (&Guard{R: &stub{context: "remote-prod"}}).SanitizeEnv(ctx))
	for _, h := range []string{"tcp://prod:2375", "ssh://root@prod", "unix:///tmp/other.sock"} {
		t.Setenv("DOCKER_HOST", h)
		refused(t, (&Guard{R: &stub{context: "default"}}).SanitizeEnv(ctx))
	}
	t.Setenv("DOCKER_HOST", "unix:///var/run/docker.sock")
	if err := (&Guard{R: &stub{context: "default"}}).SanitizeEnv(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestCleanEnvDropsRedirectingVariables(t *testing.T) {
	got := cleanEnv([]string{"PATH=/bin", "COMPOSE_PROJECT_NAME=prod", "DOCKER_CONTEXT=prod", "COMPOSE_FILE=x", "DOCKER_CONFIG=/x", "HOME=/h"})
	if strings.Join(got, ",") != "PATH=/bin,HOME=/h" {
		t.Fatal(got)
	}
}

func TestPreflightFindsOptedInStrangers(t *testing.T) {
	ctx := context.Background()
	t.Setenv("WHALENURSE_MODE", "")
	ok := &stub{psOut: "abc whalenurse-test-a\ndef whalenurse-test-b\n"}
	if err := (&Guard{R: ok}).PreflightNoStrangers(ctx); err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{"abc prod\n", "abc \n", "abc whalenurse-m0-x\n", "abc whalenurse-test-a\ndef media\n"} {
		refused(t, (&Guard{R: &stub{psOut: out}}).PreflightNoStrangers(ctx))
	}
	t.Setenv("WHALENURSE_MODE", "all")
	refused(t, (&Guard{R: ok}).PreflightNoStrangers(ctx))
}

func TestGuardedActorRefusesOtherProjects(t *testing.T) {
	f := docker.NewFake()
	f.Put(docker.Container{ID: "ours", Name: "ours", Labels: map[string]string{docker.LabelProject: "whalenurse-test-a"}})
	f.Put(docker.Container{ID: "theirs", Name: "theirs", Labels: map[string]string{docker.LabelProject: "media"}})
	f.Put(docker.Container{ID: "plain", Name: "plain", Labels: map[string]string{}})
	a := Actor{Reader: f, Inner: f}
	ctx := context.Background()
	for _, id := range []string{"theirs", "plain", "missing"} {
		refused(t, a.Restart(ctx, id))
		refused(t, a.Start(ctx, id))
	}
	if len(f.Calls()) != 0 {
		t.Fatalf("calls reached the inner actor: %v", f.Calls())
	}
	if err := a.Restart(ctx, "ours"); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(ctx, "ours"); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(f.Calls()); !strings.Contains(got, "restart") || len(f.Calls()) != 2 {
		t.Fatal(got)
	}
}
