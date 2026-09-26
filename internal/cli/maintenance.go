package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/GerhardOfRivia/onderzeeer/internal/control"
	"github.com/GerhardOfRivia/onderzeeer/internal/queue"
)

func maintenanceCommand(command string, args []string, stdout, stderr io.Writer) error {
	usage := "onderzeeer compact <instance-or-config> [--local] [--timeout duration]"
	if command == "prune" {
		usage = "onderzeeer prune <instance-or-config> --older-than age [--dry-run] [--include-failed] [--local]"
	}
	flags := newFlagSet(command, stderr, usage)
	inspection := inspectionFlags(flags)
	timeout := flags.Duration("timeout", 30*time.Minute, "maximum maintenance duration")
	var ageText string
	var options queue.PruneOptions
	if command == "prune" {
		flags.StringVar(&ageText, "older-than", "", "prune captured output of jobs completed before this age (e.g. 30d or 720h); required")
		flags.BoolVar(&options.DryRun, "dry-run", false, "preview command count and captured output bytes without changing history")
		flags.BoolVar(&options.IncludeFailed, "include-failed", false, "include failed jobs; defaults to successful jobs only")
	}
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if err := requireArguments(flags, "instance or config"); err != nil {
		return err
	}
	if *timeout <= 0 {
		return usageError{message: "--timeout must be greater than zero"}
	}
	if command == "prune" {
		age, err := parsePruneAge(ageText)
		if err != nil {
			return usageError{message: "prune --older-than requires a positive age such as 30d or 720h"}
		}
		options.Before = time.Now().UTC().Add(-age)
	}
	stores, err := inspection.open(flags.Arg(0))
	if err != nil {
		return err
	}
	defer closeStores(stores)
	if len(stores) != 1 {
		return usageError{message: "maintenance requires exactly one queue; select an instance or config file"}
	}
	source := stores[0]
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	var maintenance interface {
		PruneOutput(context.Context, queue.PruneOptions) (queue.PruneResult, error)
		Compact(context.Context) error
	}
	if *inspection.local {
		if options.DryRun {
			maintenance = source.store.(*queue.Store)
		} else {
			store, err := queue.Open(source.config.Database.Path)
			if err != nil {
				return err
			}
			defer store.Close()
			maintenance = store
		}
	} else {
		maintenance = source.store.(*control.QueueReader)
	}
	if command == "compact" {
		if err := maintenance.Compact(ctx); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Compacted database %s.\n", source.config.Database.Path)
		return nil
	}
	result, err := maintenance.PruneOutput(ctx, options)
	if err != nil && result.Commands == 0 {
		return fmt.Errorf("maintenance did not complete; any committed batches are kept and pruning can be retried: %w", err)
	}
	action := "Pruned"
	if options.DryRun {
		action = "Would prune"
	}
	fmt.Fprintf(stdout, "%s captured output from %d command(s): %d bytes of captured output.\n", action, result.Commands, result.OutputBytes)
	fmt.Fprintln(stdout, "Job records, duplicate detection, execution metadata, and source/output files are preserved.")
	if !options.DryRun {
		fmt.Fprintln(stdout, "Freed database pages can be reused. To shrink the file, stop the instance and run onderzeeer compact.")
	}
	if err != nil {
		return fmt.Errorf("maintenance did not complete; committed batches are kept and pruning can be retried: %w", err)
	}
	return nil
}

func parsePruneAge(value string) (time.Duration, error) {
	if strings.HasSuffix(value, "d") {
		days, err := strconv.ParseUint(strings.TrimSuffix(value, "d"), 10, 64)
		if err != nil || days == 0 || days > uint64((1<<63-1)/(24*time.Hour)) {
			return 0, fmt.Errorf("invalid age %q", value)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	age, err := time.ParseDuration(value)
	if err != nil || age <= 0 {
		return 0, fmt.Errorf("invalid age %q", value)
	}
	return age, nil
}

func storageFor(ctx context.Context, source configuredStore) (queue.StorageInfo, error) {
	info, err := source.store.Storage(ctx)
	if _, local := source.store.(*queue.Store); local {
		info.SetWarnings(source.config.Database.WarningThresholds())
	}
	return info, err
}

func printStorage(output io.Writer, info queue.StorageInfo) {
	fmt.Fprintf(output, "DATABASE BYTES\t%d\nWAL BYTES\t%d\nREUSABLE BYTES\t%d\n", info.DatabaseBytes, info.WALBytes, info.ReusableBytes)
	if info.Disk != nil {
		fmt.Fprintf(output, "DISK AVAILABLE BYTES\t%d\nDISK TOTAL BYTES\t%d\n", info.Disk.AvailableBytes, info.Disk.TotalBytes)
	} else {
		fmt.Fprintf(output, "DISK SPACE\tunavailable: %s\n", info.DiskError)
	}
}

func printStorageWarnings(output io.Writer, label string, info queue.StorageInfo) {
	for _, warning := range info.Warnings {
		fmt.Fprintf(output, "WARNING: %s: %s\n", label, warning)
	}
}
