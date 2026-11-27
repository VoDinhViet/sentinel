package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"sentinel/agent/internal/alertkey"
	"sentinel/agent/internal/backup"
	"sentinel/agent/internal/config"
	"sentinel/pkg/alert"
	"sentinel/pkg/notify"
)

// diskKeyPrefix starts the alert key of every disk alert (see alertkey.Disk).
const diskKeyPrefix = "disk:"

// checkDisksEvery is how often the file systems are looked at.
const checkDisksEvery = 15 * time.Minute

// diskVolume is a path the daemon writes to, and what is kept there.
type diskVolume struct {
	Path     string
	Holds    string
	Explicit bool // listed in disk.paths: a path that cannot be read is worth a warning
}

// diskUsage is how full the file system under a path is.
type diskUsage struct {
	Filesystem string // same for paths on the same file system
	FreeBytes  float64
	TotalBytes float64
}

func (u diskUsage) freePercent() float64 { return u.FreeBytes / u.TotalBytes * 100 }

// DiskWatcher raises a Warning when a file system the daemon writes to has little space left, and
// resolves it when space is back.
type DiskWatcher struct {
	Alerts         *alert.Manager
	MinFreePercent int
	Volumes        []diskVolume
	Usage          func(path string) (diskUsage, error) // statDisk unless a test replaces it
}

// diskTask is the interval task of cfg's disk watcher, or none when the alert is turned off.
func diskTask(cfg *config.Config, alerts *alert.Manager) []Task {
	if cfg.Disk.MinFree() == 0 {
		return nil
	}
	watcher := &DiskWatcher{Alerts: alerts, MinFreePercent: cfg.Disk.MinFree(), Volumes: watchedVolumes(cfg)}
	return []Task{{Name: "disk", Every: checkDisksEvery, Run: watcher.Check}}
}

// watchedVolumes lists what the daemon writes to: the state database, the restic cache, every local
// repository, and the paths of disk.paths.
func watchedVolumes(cfg *config.Config) []diskVolume {
	volumes := []diskVolume{{Path: filepath.Dir(cfg.StorePath), Holds: "state database"}}
	if cacheDir := resticCacheDir(); cacheDir != "" {
		volumes = append(volumes, diskVolume{Path: cacheDir, Holds: "restic cache"})
	}
	for _, name := range slices.Sorted(maps.Keys(cfg.Repositories)) {
		if dir, ok := localRepositoryDir(cfg.Repositories[name].URL); ok {
			volumes = append(volumes, diskVolume{Path: dir, Holds: "repository " + name})
		}
	}
	for _, path := range cfg.Disk.Paths {
		volumes = append(volumes, diskVolume{Path: path, Holds: "disk.paths", Explicit: true})
	}
	return volumes
}

// resticCacheDir is where restic keeps its cache for the daemon's user, "" when that is unknown.
func resticCacheDir() string {
	if dir := os.Getenv("RESTIC_CACHE_DIR"); dir != "" {
		return dir
	}
	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(userCacheDir, "restic")
}

// localRepositoryDir is the directory of a repository stored on this machine ("/srv/repo" or
// "local:/srv/repo"); the others (s3:, sftp:, rclone:) take no space here.
func localRepositoryDir(url string) (string, bool) {
	dir := strings.TrimPrefix(url, "local:")
	return dir, filepath.IsAbs(dir)
}

// diskGroup is the volumes that share one file system, named after the first of them.
type diskGroup struct {
	path  string
	holds []string
	usage diskUsage
}

// Check looks at every volume and opens or resolves one alert per file system.
func (w *DiskWatcher) Check(ctx context.Context) {
	usageOf := w.Usage
	if usageOf == nil {
		usageOf = statDisk
	}
	var groups []*diskGroup
	groupByFilesystem := map[string]*diskGroup{}
	for _, volume := range w.Volumes {
		usage, err := usageOf(volume.Path)
		if err != nil {
			logLevel := slog.LevelDebug // the restic cache does not exist before the first backup
			if volume.Explicit {
				logLevel = slog.LevelWarn
			}
			slog.Log(ctx, logLevel, "cannot read disk space", "path", volume.Path, "error", err)
			continue
		}
		group, seen := groupByFilesystem[usage.Filesystem]
		if !seen {
			group = &diskGroup{path: volume.Path, usage: usage}
			groupByFilesystem[usage.Filesystem] = group
			groups = append(groups, group)
		}
		group.holds = append(group.holds, volume.Holds)
	}
	if len(groups) == 0 {
		return // nothing could be read: do not close alerts on the strength of that
	}
	var keys []string
	for _, group := range groups {
		w.report(ctx, group)
		keys = append(keys, alertkey.Disk(group.path))
	}
	w.Alerts.ResolveMissing(ctx, diskKeyPrefix, keys, "Disk no longer watched")
}

func (w *DiskWatcher) report(ctx context.Context, group *diskGroup) {
	key := alertkey.Disk(group.path)
	free := group.usage.freePercent()
	holds := strings.Join(group.holds, ", ")
	if free >= float64(w.MinFreePercent) {
		w.Alerts.Resolve(ctx, key, "Disk space is back: "+group.path, fmt.Sprintf("%.0f%% free. Holds: %s.", free, holds))
		return
	}
	w.Alerts.Fire(ctx, key, notify.Warning, fmt.Sprintf("Disk almost full: %s (%.0f%% free)", group.path, free),
		fmt.Sprintf("%s free of %s, alert below %d%%. Holds: %s.",
			backup.HumanBytes(int64(group.usage.FreeBytes)), backup.HumanBytes(int64(group.usage.TotalBytes)), w.MinFreePercent, holds))
}

// statDisk reads the space left for an ordinary process on the file system under path.
func statDisk(path string) (diskUsage, error) {
	info, err := os.Stat(path)
	if err != nil {
		return diskUsage{}, err
	}
	var statfs syscall.Statfs_t
	if err := syscall.Statfs(path, &statfs); err != nil {
		return diskUsage{}, err
	}
	if statfs.Blocks == 0 {
		return diskUsage{}, fmt.Errorf("%s has no blocks to measure", path)
	}
	filesystem := path
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		filesystem = fmt.Sprint(stat.Dev)
	}
	blockSize := float64(statfs.Bsize)
	return diskUsage{Filesystem: filesystem, FreeBytes: float64(statfs.Bavail) * blockSize, TotalBytes: float64(statfs.Blocks) * blockSize}, nil
}
