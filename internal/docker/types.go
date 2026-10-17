// Package docker is the only place that talks to the Docker Engine API. It
// exposes small snapshot types and interfaces so the rest of whalenurse never
// imports the (pre-1.0) moby client.
package docker

import (
	"context"
	"errors"
	"strings"
	"time"
)

// ErrNotFound is returned when a container (or reference) does not exist.
var ErrNotFound = errors.New("no such container")

// Compose and whalenurse label keys used across packages.
const (
	LabelProject       = "com.docker.compose.project"
	LabelService       = "com.docker.compose.service"
	LabelNumber        = "com.docker.compose.container-number"
	LabelOneOff        = "com.docker.compose.oneoff"
	LabelSwarmTaskID   = "com.docker.swarm.task.id"
	containerRefPrefix = "container:"
)

// Container is the subset of `docker inspect` that the rules need.
type Container struct {
	ID     string
	Name   string // without the leading slash
	Labels map[string]string

	Status     string // created running paused restarting removing exited dead
	Running    bool
	Paused     bool
	Restarting bool
	OOMKilled  bool
	ExitCode   int
	Error      string // State.Error: non-empty means a failed start
	Created    time.Time
	StartedAt  time.Time
	FinishedAt time.Time
	Health     string // "" (no healthcheck), starting, healthy, unhealthy

	RestartCount  int
	RestartPolicy string // no, always, unless-stopped, on-failure
	MaxRetries    int

	NetworkMode string
	IpcMode     string
	PidMode     string
}

// Summary is one row of the container list; enough for the poll to decide
// which containers deserve an inspect.
type Summary struct {
	ID          string
	Name        string
	Labels      map[string]string
	State       string // same values as Container.Status
	Health      string // "" when there is no healthcheck
	NetworkMode string
	Created     time.Time
}

// Event is a container event, reduced to what whalenurse reads. Action is the
// bare action: "health_status: unhealthy" is split into Action "health_status"
// and Detail "unhealthy"; "exec_start: cmd" into "exec_start" and the command.
type Event struct {
	Action   string
	Detail   string
	ID       string
	Name     string
	Labels   map[string]string // every attribute of the event, not only labels
	ExitCode int
	HasExit  bool
	Signal   string
	Time     time.Time
}

// Stream is a live subscription. Events is closed when the stream ends and
// Err then carries the reason.
type Stream struct {
	Events <-chan Event
	Err    <-chan error
}

// Reader is the read-only half of the client. In dry-run mode the healer is
// wired with a Reader and a no-op Actor.
type Reader interface {
	Ping(ctx context.Context) error
	// List returns every container, running or not.
	List(ctx context.Context) ([]Summary, error)
	// Inspect accepts an ID or a name and returns ErrNotFound when it is gone.
	Inspect(ctx context.Context, ref string) (Container, error)
	// Events subscribes to container events from since (zero means now).
	Events(ctx context.Context, since time.Time) Stream
}

// Actor is the mutating half: rung 1 of the ladder. Nothing else in the
// codebase changes containers.
type Actor interface {
	Restart(ctx context.Context, id string) error
	Start(ctx context.Context, id string) error
}

// Client is a Reader and an Actor.
type Client interface {
	Reader
	Actor
	Close() error
}

// ComposeKeyParts returns project, service and container number when the
// labels identify a compose container.
func ComposeKeyParts(labels map[string]string) (project, service, number string, ok bool) {
	project, service = labels[LabelProject], labels[LabelService]
	number = labels[LabelNumber]
	if number == "" {
		number = "1"
	}
	return project, service, number, project != "" && service != ""
}

// StateKey builds the state key host/project/service/container-number from
// compose labels, never from the container ID because a recreate changes it.
// Containers without compose labels fall back to host/name.
func StateKey(host, name string, labels map[string]string) string {
	if p, s, n, ok := ComposeKeyParts(labels); ok {
		return host + "/" + p + "/" + s + "/" + n
	}
	return host + "/" + strings.TrimPrefix(name, "/")
}

// ContainerRef returns X from a "container:X" mode, or "" if the mode is not
// a container reference.
func ContainerRef(mode string) string {
	if r, ok := strings.CutPrefix(mode, containerRefPrefix); ok {
		return r
	}
	return ""
}

// MatchesRef reports whether a container is the target of a container:<ref>
// reference. Compose writes the full ID; users can write a name or an ID prefix.
func MatchesRef(id, name, ref string) bool {
	if ref == "" {
		return false
	}
	if id == ref || (len(ref) >= 4 && strings.HasPrefix(id, ref)) {
		return true
	}
	return strings.TrimPrefix(name, "/") == strings.TrimPrefix(ref, "/")
}
