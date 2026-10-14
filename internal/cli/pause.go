package cli

import (
	"errors"
	"sync"

	"pingy/internal/monitor"
)

// pauseAll stops those of metas that are running, all at once, keeping
// their history. It returns the ones it paused.
func pauseAll(metas []*monitor.Meta) ([]*monitor.Meta, error) {
	var running []*monitor.Meta
	for _, m := range metas {
		if m.Running() {
			running = append(running, m)
		}
	}
	errs := make([]error, len(running))
	var wg sync.WaitGroup
	for i, m := range running {
		wg.Go(func() { errs[i] = pause(m) })
	}
	wg.Wait()
	return running, errors.Join(errs...)
}

// pause stops m, keeping its history, and marks it paused so autostart
// leaves it be.
func pause(m *monitor.Meta) error {
	fresh, err := m.Reload()
	if err != nil {
		return err
	}
	fresh.Paused = true
	if err := fresh.Save(); err != nil {
		return err
	}
	return fresh.Stop()
}

// resumeAll starts those of metas that are stopped again, all at once. It
// returns the ones it started.
func (a *App) resumeAll(metas []*monitor.Meta) ([]*monitor.Meta, error) {
	var stopped []*monitor.Meta
	for _, m := range metas {
		if !m.Running() {
			stopped = append(stopped, m)
		}
	}
	errs := make([]error, len(stopped))
	var wg sync.WaitGroup
	for i, m := range stopped {
		wg.Go(func() {
			fresh, err := m.Reload()
			if err != nil {
				errs[i] = err
				return
			}
			if !fresh.Foreground {
				fresh.OwnerPID, fresh.Ephemeral = 0, false
			}
			if err := fresh.Save(); err != nil {
				errs[i] = err
				return
			}
			errs[i] = a.spawn(fresh)
		})
	}
	wg.Wait()
	return stopped, errors.Join(errs...)
}

// cmdPause pauses the hosts and views named, or every host.
func (a *App) cmdPause(targets []string) error {
	list := monitor.List(a.root())
	if len(targets) > 0 {
		var err error
		if list, _, err = a.expand(targets); err != nil {
			return err
		}
	}
	paused, err := pauseAll(list)
	u := a.errUI()
	for _, m := range paused {
		if m.Running() {
			continue // reported in err
		}
		a.done("Paused %s; its history is kept", u.bold(m.Label()))
	}
	switch {
	case err != nil:
		return err
	case len(paused) == 0 && len(targets) == 0:
		a.hint("Nothing is being pinged right now.")
	case len(paused) == 0:
		a.hint("Already paused.")
	default:
		a.hint("  pingy -r to ping %s again", plural(len(paused), "it", "them"))
	}
	return nil
}
