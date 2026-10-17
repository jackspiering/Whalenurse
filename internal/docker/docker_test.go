package docker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseInspectDecodesRealFixtures(t *testing.T) {
	files, _ := filepath.Glob("../../testdata/inspect/*.json")
	if len(files) == 0 {
		t.Fatal("no fixtures found")
	}
	for _, f := range files {
		raw, _ := os.ReadFile(f)
		c, err := ParseInspect(raw)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if c.ID == "" || c.Name == "" || strings.HasPrefix(c.Name, "/") || c.Status == "" || c.Labels[LabelProject] == "" {
			t.Errorf("%s: incomplete decode: %+v", f, c)
		}
	}
	raw, _ := os.ReadFile("../../testdata/inspect/e8-stuck-failed-restart.json")
	c, _ := ParseInspect(raw)
	if c.Status != "exited" || c.ExitCode != 1 || c.RestartCount != 1 || c.RestartPolicy != "unless-stopped" || !strings.Contains(c.Error, "failed to fulfil mount request") {
		t.Fatalf("%+v", c)
	}
	if !c.StartedAt.After(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) || !c.FinishedAt.After(c.StartedAt) {
		t.Fatalf("times: %v %v", c.StartedAt, c.FinishedAt)
	}
	raw, _ = os.ReadFile("../../testdata/inspect/e1-dep-stale-after-parent-recreate.json")
	c, _ = ParseInspect(raw)
	if ContainerRef(c.NetworkMode) != "48d0a0133f45a83e707be12da7d90c65ef6e116524f09f4799d109b3471ec612" {
		t.Fatalf("network mode %q", c.NetworkMode)
	}
	raw, _ = os.ReadFile("../../testdata/inspect/e9-hc-unhealthy.json")
	c, _ = ParseInspect(raw)
	if c.Health != "unhealthy" {
		t.Fatalf("health %q", c.Health)
	}
	if c, _ := ParseInspect([]byte(`[{"Id":"x","Name":"/n","State":{"Status":"created","StartedAt":"0001-01-01T00:00:00Z"}}]`)); !c.StartedAt.IsZero() {
		t.Fatal("the zero time should stay zero")
	}
	if _, err := ParseInspect([]byte(`[]`)); err == nil {
		t.Fatal("empty array should be an error")
	}
}

func TestParseEventSplitsActions(t *testing.T) {
	ev, err := ParseEvent([]byte(`{"Type":"container","Action":"health_status: unhealthy","Actor":{"ID":"abc","Attributes":{"name":"c1","com.docker.compose.project":"p"}},"timeNano":1500000000}`))
	if err != nil || ev.Action != "health_status" || ev.Detail != "unhealthy" || ev.Name != "c1" || ev.ID != "abc" {
		t.Fatalf("%+v %v", ev, err)
	}
	ev, _ = ParseEvent([]byte(`{"Type":"container","Action":"die","Actor":{"ID":"abc","Attributes":{"name":"c1","exitCode":"137"}}}`))
	if !ev.HasExit || ev.ExitCode != 137 {
		t.Fatalf("%+v", ev)
	}
	if _, err := ParseEvent([]byte(`{"Type":"network","Action":"connect","Actor":{"ID":"n"}}`)); err == nil {
		t.Fatal("non-container events are rejected")
	}
}

func TestStateKey(t *testing.T) {
	l := map[string]string{LabelProject: "media", LabelService: "web", LabelNumber: "2"}
	if got := StateKey("box", "media-web-2", l); got != "box/media/web/2" {
		t.Fatal(got)
	}
	// The container number defaults to 1, and the key never contains the ID.
	if got := StateKey("box", "x", map[string]string{LabelProject: "p", LabelService: "s"}); got != "box/p/s/1" {
		t.Fatal(got)
	}
	if got := StateKey("box", "/plain", nil); got != "box/plain" {
		t.Fatal(got)
	}
}

func TestMatchesRef(t *testing.T) {
	id := strings.Repeat("ab", 32)
	for ref, want := range map[string]bool{id: true, id[:12]: true, "web": true, "/web": true, "ab": false, "": false, "other": false, "cd" + id[2:]: false} {
		if got := MatchesRef(id, "web", ref); got != want {
			t.Errorf("ref %q: %v", ref, got)
		}
	}
}

func TestSelfID(t *testing.T) {
	id := strings.Repeat("1f", 32)
	mi := "36 25 0:32 / / rw - overlay overlay rw\n" +
		"412 36 8:1 /var/lib/docker/containers/" + id + "/resolv.conf /etc/resolv.conf rw - ext4 /dev/sda1 rw\n"
	if got := SelfID(strings.NewReader(mi)); got != id {
		t.Fatalf("got %q", got)
	}
	if got := SelfID(strings.NewReader("36 25 0:32 / / rw - overlay overlay rw\n")); got != "" {
		t.Fatalf("outside a container: %q", got)
	}
}

func TestContainerRef(t *testing.T) {
	if ContainerRef("container:abc") != "abc" || ContainerRef("host") != "" || ContainerRef("p_default") != "" {
		t.Fatal("ContainerRef")
	}
}
