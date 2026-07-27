package local

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

// noticeAfter is how long a contender waits silently before naming the
// holder on stderr (§8.2: "after ~1s of waiting").
const noticeAfter = time.Second

// Lock takes the one advisory exclusive invocation lock, .git/.dm/lock
// (§8.2). Contenders wait until the lock frees — they never error; after
// noticeAfter a line on notice names the holder. flock-style, so the lock
// dies with the process and can never go stale; the holder pid written into
// the file is informational only.
func (d *Dir) Lock(notice io.Writer) (release func(), err error) {
	fl := flock.New(d.lockPath())
	locked, err := fl.TryLock()
	if err != nil {
		return nil, fmt.Errorf("locking %s: %w", d.lockPath(), err)
	}
	if !locked {
		timer := time.AfterFunc(noticeAfter, func() {
			holder := "unknown pid"
			if b, err := os.ReadFile(d.lockPath()); err == nil {
				if pid := strings.TrimSpace(string(b)); pid != "" {
					holder = "pid " + pid
				}
			}
			fmt.Fprintf(notice, "dm: waiting on %s (held by %s)\n", d.lockPath(), holder)
		})
		err := fl.Lock()
		timer.Stop()
		if err != nil {
			return nil, fmt.Errorf("locking %s: %w", d.lockPath(), err)
		}
	}
	// Best-effort holder marker for the notice above; the flock itself is
	// what serializes.
	_ = os.WriteFile(d.lockPath(), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644)
	return func() { _ = fl.Unlock() }, nil
}
