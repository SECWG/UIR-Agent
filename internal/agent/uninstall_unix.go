//go:build !windows

package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
)

func uninstallAvailable() bool { return true }

func prepareUninstallCleanup(plan uninstallPlan) (func(uninstallResult) error, func(), error) {
	return func(result uninstallResult) error {
		file, err := os.Open(plan.Executable)
		if err == nil {
			hash := sha256.New()
			_, err = io.Copy(hash, file)
			file.Close()
			if err == nil && hex.EncodeToString(hash.Sum(nil)) == plan.SHA256 {
				err = os.Remove(plan.Executable)
			} else {
				err = errors.New("executable changed")
			}
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			result.Status = "partial"
			result.Residuals = append(result.Residuals, plan.Executable)
		}
		return reportUninstall(plan, result)
	}, func() {}, nil
}
