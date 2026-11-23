package backup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"sentinel/internal/restic"
)

// localRepoDir is the folder of a repository kept on this machine: an absolute path, or
// "local:" followed by one. Remote locations (s3:, sftp:, rclone:, rest:...) are not.
func localRepoDir(url string) (string, bool) {
	dir := strings.TrimPrefix(url, "local:")
	if strings.HasPrefix(dir, "/") {
		return dir, true
	}
	return "", false
}

// initLocalRepo creates the repository the first time a backup goes to a folder of this machine,
// so nobody has to run `sentinel backup init` first. It only does so for a folder that does not
// exist yet or is empty: a folder with other things in it is never turned into a repository,
// and a remote repository is never created behind the user's back (a mistyped bucket or host
// would otherwise start a new, empty one). created is false when nothing was done.
func initLocalRepo(ctx context.Context, client *restic.Client) (created bool, err error) {
	dir, local := localRepoDir(client.Repo.URL)
	if !local {
		return false, nil
	}
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return false, fmt.Errorf("repository folder %s: %w", dir, err)
	case len(entries) > 0:
		return false, fmt.Errorf("%s is not a restic repository and the folder is not empty: not creating one there (run `sentinel backup init <job>` to do it on purpose)", dir)
	}
	if _, err := client.Init(ctx); err != nil {
		return false, fmt.Errorf("create repository in %s: %w", dir, err)
	}
	return true, nil
}
