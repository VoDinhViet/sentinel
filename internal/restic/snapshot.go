package restic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Snapshot struct {
	ID       string    `json:"id"`
	ShortID  string    `json:"short_id"`
	Time     time.Time `json:"time"`
	Hostname string    `json:"hostname"`
	Tags     []string  `json:"tags"`
	Paths    []string  `json:"paths"`
	Summary  *struct {
		TotalBytes int64 `json:"total_bytes_processed"`
	} `json:"summary"`
}

func (c *Client) Snapshots(ctx context.Context, tags []string) ([]Snapshot, error) {
	args := []string{"snapshots", "--json", "--host", c.Host}
	if len(tags) > 0 {
		args = append(args, "--tag", strings.Join(tags, ","))
	}
	stdout, err := c.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var snaps []Snapshot
	if err := json.Unmarshal(stdout, &snaps); err != nil {
		return nil, fmt.Errorf("parse snapshots: %w", err)
	}
	return snaps, nil
}

// ErrForeignSnapshot means the snapshot is not one of the job's snapshots.
var ErrForeignSnapshot = errors.New("snapshot does not belong to the job")

// JobSnapshot finds the snapshot named by a full id or a unique id prefix among
// the snapshots with tags (a job's tags, on this host). Restoring through it
// keeps one job from restoring, and being told how to load, another job's data.
func (c *Client) JobSnapshot(ctx context.Context, tags []string, idPrefix string) (Snapshot, error) {
	snaps, err := c.Snapshots(ctx, tags)
	if err != nil {
		return Snapshot{}, err
	}
	var matches []Snapshot
	for _, snapshot := range snaps {
		if snapshot.ID == idPrefix {
			return snapshot, nil
		}
		if idPrefix != "" && strings.HasPrefix(snapshot.ID, idPrefix) {
			matches = append(matches, snapshot)
		}
	}
	switch len(matches) {
	case 0:
		return Snapshot{}, fmt.Errorf("%w: %s", ErrForeignSnapshot, idPrefix)
	case 1:
		return matches[0], nil
	}
	return Snapshot{}, fmt.Errorf("snapshot id %q is ambiguous (%d matches); use a longer prefix", idPrefix, len(matches))
}
