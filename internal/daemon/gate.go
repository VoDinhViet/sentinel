package daemon

import (
	"context"
	"sync"
)

// repoGate lets backups of one repository run side by side and a prune run alone. restic prune takes the
// repository's exclusive lock, and a backup that meets it fails at once, so the daemon keeps the two apart
// itself instead of leaving the backups to give up after their lock retries (a prune can take hours).
// A prune that is waiting for the running backups to end also holds back the ones that want to start.
type repoGate struct {
	mu      sync.Mutex
	users   int           // backups inside
	pruning bool          // a prune is inside
	waiting int           // prunes waiting for the backups to end
	changed chan struct{} // closed and replaced at every change, to wake the waiters
}

// enter waits until no prune runs or waits, then counts a backup in. The caller calls leave when done.
func (g *repoGate) enter(ctx context.Context) error {
	for {
		g.mu.Lock()
		if !g.pruning && g.waiting == 0 {
			g.users++
			g.mu.Unlock()
			return nil
		}
		changed := g.wake()
		g.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (g *repoGate) leave() {
	g.mu.Lock()
	g.users--
	g.broadcast()
	g.mu.Unlock()
}

// exclusive waits until no backup runs and no other prune does, then holds the repository alone. The caller
// calls release when done.
func (g *repoGate) exclusive(ctx context.Context) error {
	g.mu.Lock()
	g.waiting++
	g.broadcast() // nothing new may enter from now on
	for g.users > 0 || g.pruning {
		changed := g.wake()
		g.mu.Unlock()
		select {
		case <-changed:
			g.mu.Lock()
		case <-ctx.Done():
			g.mu.Lock()
			g.waiting--
			g.broadcast()
			g.mu.Unlock()
			return ctx.Err()
		}
	}
	g.waiting--
	g.pruning = true
	g.mu.Unlock()
	return nil
}

func (g *repoGate) release() {
	g.mu.Lock()
	g.pruning = false
	g.broadcast()
	g.mu.Unlock()
}

// wake returns the channel that is closed at the next change. The caller holds g.mu.
func (g *repoGate) wake() <-chan struct{} {
	if g.changed == nil {
		g.changed = make(chan struct{})
	}
	return g.changed
}

// broadcast wakes every waiter. The caller holds g.mu.
func (g *repoGate) broadcast() {
	if g.changed != nil {
		close(g.changed)
		g.changed = nil
	}
}
