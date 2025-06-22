package userx

import (
	"log"
	"os"
	"os/user"
	"path/filepath"

	"github.com/james-lawrence/torrent/internal/envx"
)

func Root() user.User {
	return user.User{
		Gid:     "0",
		Uid:     "0",
		HomeDir: "/root",
	}
}

func Zero() user.User {
	return user.User{}
}

// CurrentUserOrDefault returns the current user or the default configured user.
func CurrentUserOrDefault(d user.User) (result *user.User) {
	var (
		err error
	)

	if result, err = user.Current(); err != nil {
		log.Println("failed to retrieve current user, using default", err)
		tmp := d
		return &tmp
	}

	return result
}

// DefaultConfigDir returns the user config directory.
func DefaultConfigDir(rel ...string) string {
	user := CurrentUserOrDefault(Root())
	defaultdir := filepath.Join(user.HomeDir, ".config")
	return filepath.Join(envx.String(defaultdir, "XDG_CONFIG_HOME"), filepath.Join(rel...))
}

// DefaultDirLocation looks for a directory one of the default directory locations.
func DefaultDirLocation(rel string) string {
	user := CurrentUserOrDefault(Root())

	env := filepath.Join(os.Getenv("XDG_CONFIG_HOME"))
	home := filepath.Join(user.HomeDir, ".config")
	system := filepath.Join("/etc")

	return DefaultDirectory(rel, env, home, system)
}

// DefaultCacheDirectory cache directory for storing data.
func DefaultCacheDirectory(rel ...string) string {
	user := CurrentUserOrDefault(Root())
	if user.Uid == Root().Uid {
		return envx.String(filepath.Join("/", "var", "cache"), "CACHE_DIRECTORY")
	}

	defaultdir := filepath.Join(user.HomeDir, ".cache")
	return filepath.Join(envx.String(defaultdir, "CACHE_DIRECTORY", "XDG_CACHE_HOME"), filepath.Join(rel...))
}

func DefaultDataDirectory(rel ...string) string {
	user := CurrentUserOrDefault(Root())
	defaultdir := filepath.Join(user.HomeDir, ".local", "share")
	return filepath.Join(envx.String(defaultdir, "XDG_DATA_HOME"), filepath.Join(rel...))
}

// DefaultDownloadDirectory returns the user config directory.
func DefaultDownloadDirectory(rel ...string) string {
	user := CurrentUserOrDefault(Root())
	auto := filepath.Join(user.HomeDir, "Downloads")
	return filepath.Join(envx.String(auto, "XDG_DOWNLOAD_DIR"), filepath.Join(rel...))
}

// DefaultRuntimeDirectory runtime directory for storing data.
func DefaultRuntimeDirectory(rel ...string) string {
	user := CurrentUserOrDefault(Root())

	if user.Uid == Root().Uid {
		return envx.String(filepath.Join("/", "run"), "RUNTIME_DIRECTORY", "XDG_RUNTIME_DIR")
	}

	defaultdir := filepath.Join("/", "run", "user", user.Uid)
	return filepath.Join(envx.String(defaultdir, "RUNTIME_DIRECTORY", "XDG_RUNTIME_DIR"), filepath.Join(rel...))
}

// return a path relative to the home directory.
func HomeDirectoryRel(rel ...string) (path string, err error) {
	if path, err = os.UserHomeDir(); err != nil {
		return "", err
	}

	return filepath.Join(path, filepath.Join(rel...)), nil
}

// DefaultDirectory finds the first directory root that exists and then returns
// that root directory joined with the relative path provided.
func DefaultDirectory(rel string, roots ...string) (path string) {
	for _, root := range roots {
		path = filepath.Join(root, rel)
		if _, err := os.Stat(root); err == nil {
			return path
		}
	}

	return path
}

// HomeDirectoryOrDefault loads the user home directory or falls back to the provided
// path when an error occurs.
func HomeDirectoryOrDefault(fallback string) (dir string) {
	var (
		err error
	)

	if dir, err = os.UserHomeDir(); err != nil {
		log.Println("unable to get user home directory", err)
		return fallback
	}

	return dir
}
