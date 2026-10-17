package docker

import (
	"bufio"
	"io"
	"regexp"
)

var selfIDRe = regexp.MustCompile(`/containers/([0-9a-f]{64})/`)

// SelfID finds whalenurse's own container ID in /proc/self/mountinfo. The
// hostname can be overridden, but the bind mounts Docker adds for
// /etc/hostname, /etc/hosts and /etc/resolv.conf always point into
// /var/lib/docker/containers/<id>/. It returns "" outside a container.
func SelfID(mountinfo io.Reader) string {
	sc := bufio.NewScanner(mountinfo)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		if m := selfIDRe.FindStringSubmatch(sc.Text()); m != nil {
			return m[1]
		}
	}
	return ""
}
