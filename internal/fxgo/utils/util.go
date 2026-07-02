package utils

import (
	"log"
	"syscall"

	"github.com/google/uuid"
)

// SetRlimitNoFile sets the RLIMIT_NOFILE to the maximum allowed value
func SetRlimitNoFile() error {
	var rLimit syscall.Rlimit
	err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rLimit)
	if err != nil {
		return err
	}

	// Set both soft and hard limits to the maximum (hard limit)
	rLimit.Cur = rLimit.Max
	err = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &rLimit)
	if err != nil {
		return err
	}

	log.Printf("RLIMIT_NOFILE set to %d", rLimit.Max)
	return nil
}

func CountTrailingZero(x int) int {
	count := 0
	for (x & 1) == 0 {
		x = x >> 1
		count += 1
	}
	return count
}

// GenerateUUID generates a new UUID string
func GenerateUUID() (string, error) {
	newUUID, err := uuid.NewRandom()
	if err != nil {
		return "", err
	}
	return newUUID.String(), nil
}
