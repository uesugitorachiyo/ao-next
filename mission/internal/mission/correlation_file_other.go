//go:build !darwin && !linux && !windows

package mission

import (
	"errors"
	"os"
)

func openCorrelationInput(_ string) (*os.File, error) {
	return nil, errors.New("correlation input safety is unsupported on this platform")
}
