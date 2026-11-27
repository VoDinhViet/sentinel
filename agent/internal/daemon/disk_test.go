package daemon

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"sentinel/agent/internal/alert"
	"sentinel/agent/internal/config"
)

// usageByPath answers a disk check from a table, in percent free of a 100 GB file system.
type usageByPath map[string]struct {
	filesystem  string
	freePercent float64
}

func (u usageByPath) usage(path string) (diskUsage, error) {
	entry, ok := u[path]
	if !ok {
		return diskUsage{}, errors.New("no such directory")
	}
	const total = 100e9
	return diskUsage{Filesystem: entry.filesystem, FreeBytes: total * entry.freePercent / 100, TotalBytes: total}, nil
}

func newDiskWatcher(rec *recorder, usage usageByPath, volumes ...diskVolume) *DiskWatcher {
	return &DiskWatcher{
		Alerts:         &alert.Manager{Notifier: rec, Cooldown: time.Hour},
		MinFreePercent: 10, Volumes: volumes, Usage: usage.usage,
	}
}

func TestDiskWatcherOpensAndResolvesOneAlertPerFilesystem(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	usage := usageByPath{
		"/var/lib/sentinel":   {"sda", 4},
		"/root/.cache/restic": {"sda", 4}, // the same file system as the state database
		"/srv/repo":           {"sdb", 60},
	}
	watcher := newDiskWatcher(rec, usage,
		diskVolume{Path: "/var/lib/sentinel", Holds: "state database"},
		diskVolume{Path: "/root/.cache/restic", Holds: "restic cache"},
		diskVolume{Path: "/srv/repo", Holds: "repository local"})

	watcher.Check(ctx)
	if got, want := rec.titles(), []string{"WARNING Disk almost full: /var/lib/sentinel (4% free)"}; !slices.Equal(got, want) {
		t.Fatalf("alerts = %v, want %v", got, want)
	}
	watcher.Check(ctx)
	if got := rec.titles(); len(got) != 0 {
		t.Fatalf("a full disk was announced again within the cooldown: %v", got)
	}

	usage["/var/lib/sentinel"] = struct {
		filesystem  string
		freePercent float64
	}{"sda", 35}
	usage["/root/.cache/restic"] = usage["/var/lib/sentinel"]
	watcher.Check(ctx)
	if got, want := rec.titles(), []string{"RESOLVED Disk space is back: /var/lib/sentinel"}; !slices.Equal(got, want) {
		t.Fatalf("alerts = %v, want %v", got, want)
	}
}

func TestDiskWatcherNamesWhatTheFullFilesystemHolds(t *testing.T) {
	rec := &recorder{}
	watcher := newDiskWatcher(rec, usageByPath{"/var/lib/sentinel": {"sda", 3}, "/srv/repo": {"sda", 3}},
		diskVolume{Path: "/var/lib/sentinel", Holds: "state database"},
		diskVolume{Path: "/srv/repo", Holds: "repository local"})
	watcher.Check(context.Background())
	if len(rec.msgs) != 1 || !strings.Contains(rec.msgs[0].Body, "state database, repository local") || !strings.Contains(rec.msgs[0].Body, "below 10%") {
		t.Fatalf("messages = %+v, want one that names both and the threshold", rec.msgs)
	}
}

func TestDiskWatcherSkipsAPathItCannotRead(t *testing.T) {
	rec := &recorder{}
	watcher := newDiskWatcher(rec, usageByPath{"/srv/repo": {"sdb", 50}},
		diskVolume{Path: "/root/.cache/restic", Holds: "restic cache"}, // not created before the first backup
		diskVolume{Path: "/srv/repo", Holds: "repository local"})
	watcher.Check(context.Background())
	if got := rec.titles(); len(got) != 0 {
		t.Fatalf("alerts = %v, want none", got)
	}
}

func TestWatchedVolumes(t *testing.T) {
	t.Setenv("RESTIC_CACHE_DIR", "/cache/restic")
	cfg := &config.Config{
		StorePath: "/var/lib/sentinel/state.db",
		Repositories: map[string]config.Repository{
			"s3":     {URL: "s3:s3.amazonaws.com/bucket"},
			"disk":   {URL: "/srv/repo"},
			"local":  {URL: "local:/mnt/backup"},
			"gdrive": {URL: "rclone:gdrive:backups"},
		},
		Disk: config.DiskSettings{Paths: []string{"/data"}},
	}
	var got []string
	for _, volume := range watchedVolumes(cfg) {
		got = append(got, volume.Path)
	}
	want := []string{"/var/lib/sentinel", "/cache/restic", "/srv/repo", "/mnt/backup", "/data"}
	if !slices.Equal(got, want) {
		t.Errorf("watched = %v, want %v", got, want)
	}
}

func TestDiskTaskIsOffAtZero(t *testing.T) {
	zero := 0
	if tasks := diskTask(&config.Config{Disk: config.DiskSettings{MinFreePercent: &zero}}, &alert.Manager{}); len(tasks) != 0 {
		t.Errorf("tasks = %v with min_free_percent 0, want none", tasks)
	}
	if tasks := diskTask(&config.Config{StorePath: "/x/state.db"}, &alert.Manager{}); len(tasks) != 1 {
		t.Errorf("tasks = %v by default, want the disk task", tasks)
	}
}

// statDisk reads a real file system: the temp directory exists, so its numbers must make sense.
func TestStatDiskReadsARealDirectory(t *testing.T) {
	usage, err := statDisk(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if usage.TotalBytes <= 0 || usage.FreeBytes < 0 || usage.FreeBytes > usage.TotalBytes || usage.Filesystem == "" {
		t.Errorf("usage = %+v", usage)
	}
	if _, err := statDisk("/no/such/directory"); err == nil {
		t.Error("statDisk accepted a missing directory")
	}
}

// A disk that left the config (or whose first path changed) must not keep its alert open for ever.
func TestDiskAlertOfAPathNoLongerWatchedIsResolved(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	usage := usageByPath{"/var/lib/sentinel": {"sda", 4}, "/srv/old": {"sdb", 2}}
	watcher := newDiskWatcher(rec, usage,
		diskVolume{Path: "/var/lib/sentinel", Holds: "state database"},
		diskVolume{Path: "/srv/old", Holds: "disk.paths"})
	watcher.Check(ctx)
	rec.titles()

	watcher.Volumes = watcher.Volumes[:1] // /srv/old was removed from the config
	watcher.Check(ctx)
	if got, want := rec.titles(), []string{"RESOLVED Disk no longer watched: /srv/old"}; !slices.Equal(got, want) {
		t.Fatalf("alerts = %v, want %v", got, want)
	}

	// When nothing can be read, nothing is closed on that evidence.
	watcher.Usage = func(string) (diskUsage, error) { return diskUsage{}, errors.New("unreadable") }
	watcher.Check(ctx)
	if got := rec.titles(); len(got) != 0 {
		t.Fatalf("alerts = %v when no path could be read, want none", got)
	}
}
