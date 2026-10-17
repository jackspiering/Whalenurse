package docker

import "context"

// DryRun is an Actor that changes nothing. In dry-run mode it is the only
// Actor the healer holds, so no code path can mutate a container.
type DryRun struct {
	// Would is called with the verb and ID of every action that was skipped.
	Would func(verb, id string)
}

func (d DryRun) Restart(_ context.Context, id string) error { return d.note("restart", id) }
func (d DryRun) Start(_ context.Context, id string) error   { return d.note("start", id) }

func (d DryRun) note(verb, id string) error {
	if d.Would != nil {
		d.Would(verb, id)
	}
	return nil
}

var _ Actor = DryRun{}
