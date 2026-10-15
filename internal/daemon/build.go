package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"sync"
)

var (
	buildOnce sync.Once
	buildID   string
)

// BuildID identifies this exact executable: the first 12 hex digits of its
// SHA-256. The version string stays "1.1.0" across builds, so the GUI compares
// this to notice that the installed service is not the build it was started
// from. Empty when the executable cannot be read.
func BuildID() string {
	buildOnce.Do(func() {
		path, err := os.Executable()
		if err != nil {
			return
		}
		f, err := os.Open(path)
		if err != nil {
			return
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return
		}
		buildID = hex.EncodeToString(h.Sum(nil))[:12]
	})
	return buildID
}
