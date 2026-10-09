package base

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Now is the clock; tests may replace it.
var Now = func() time.Time { return time.Now().UTC().Truncate(time.Second) }

// ErrLockTimeout is returned when another process holds the table lock.
var ErrLockTimeout = errors.New("lock timeout")

// AtomicWrite writes data through a same-directory temp file, fsync and rename.
// On rename failure the temp file is kept for manual recovery.
func AtomicWrite(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_ = f.Chmod(0o644)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename to %s failed, temp kept at %s: %w", path, tmp, err)
	}
	return nil
}

// lock takes an exclusive cross-process lock via O_EXCL on path+".lock".
// A lock whose pid is dead is reclaimed. Waits up to 3 seconds.
func lock(path string) (func(), error) {
	lockPath := path + ".lock"
	deadline := time.Now().Add(3 * time.Second)
	for {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprint(f, os.Getpid())
			f.Close()
			return func() { os.Remove(lockPath) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if b, rerr := os.ReadFile(lockPath); rerr == nil {
			pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
			if pid <= 0 || pid == os.Getpid() || !pidAlive(pid) {
				os.Remove(lockPath)
				continue
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%w: %s", ErrLockTimeout, lockPath)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func pidAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return p.Signal(syscall.Signal(0)) == nil
}
