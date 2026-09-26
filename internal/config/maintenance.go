package config

import (
	"errors"
	"math"
	"time"
)

// WarningThresholds returns the database+WAL size and minimum available disk
// percentage that trigger warnings. Explicit zero disables that warning.
func (c DatabaseConfig) WarningThresholds() (int64, float64) {
	size, free := int64(10<<30), float64(10)
	if c.WarnSizeBytes != nil {
		size = *c.WarnSizeBytes
	}
	if c.WarnFreePercent != nil {
		free = *c.WarnFreePercent
	}
	return size, free
}

func (c DatabaseConfig) RetentionAge() time.Duration {
	if c.OutputRetention == nil {
		return 0
	}
	return c.OutputRetention.Duration
}

func (c DatabaseConfig) ValidateMaintenance() error {
	if c.RetentionAge() < 0 {
		return errors.New("database.output_retention must not be negative")
	}
	size, free := c.WarningThresholds()
	if size < 0 {
		return errors.New("database.warn_size_bytes must not be negative")
	}
	if math.IsNaN(free) || math.IsInf(free, 0) || free < 0 || free > 100 {
		return errors.New("database.warn_free_percent must be between 0 and 100")
	}
	return nil
}
