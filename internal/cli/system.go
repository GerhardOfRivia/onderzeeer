package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/GerhardOfRivia/onderzeeer/internal/control"
)

func systemCommand(args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("system", stderr, "onderzeeer system [--purge] [--socket path]")
	socketPath := flags.String("socket", "", "control socket (defaults to ONDERZEEER_SOCKET or a per-user path)")
	purge := flags.Bool("purge", false, "permanently remove exited and failed queues, registrations, and job history")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usageError{message: "system does not accept positional arguments"}
	}
	client := control.NewClient(*socketPath)
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), controlTimeout)
	defer cancel()
	var purgeErrors []error
	if *purge {
		result, err := client.PurgeInactive(ctx)
		if err != nil {
			return err
		}
		for _, instance := range result.Removed {
			fmt.Fprintf(stdout, "Removed queue %s (%s): %s\n", instance.Name, instance.ID, instance.DatabasePath)
		}
		fmt.Fprintf(stdout, "Purged %d inactive queue(s).\n\n", len(result.Removed))
		for _, failure := range result.Failures {
			purgeErrors = append(purgeErrors, fmt.Errorf("purge %s (%s): %s", failure.Name, failure.ID, failure.Error))
		}
	}
	info, err := client.System(ctx)
	if err != nil {
		return errors.Join(append(purgeErrors, err)...)
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "Daemon version\t%s\nPID\t%d\nStarted\t%s\n", info.Version, info.PID, formatTime(info.StartedAt))
	fmt.Fprintln(w, "Configuration source\tcommand-line flags and environment (no daemon config file)")
	fmt.Fprintf(w, "Log level\t%s\nLog output\tstderr\n", info.LogLevel)
	fmt.Fprintf(w, "Control socket\t%s\nSocket lock\t%s\n", info.SocketPath, info.SocketLockPath)
	fmt.Fprintf(w, "State directory\t%s\nRegistry database\t%s\nState lock\t%s\nQueue directory\t%s\n",
		info.StateDirectory, info.RegistryPath, info.StateLockPath, info.QueueDirectory)
	if info.WebAddress == "" {
		fmt.Fprintln(w, "Web dashboard\tdisabled")
	} else {
		fmt.Fprintf(w, "Web listen\t%s\nWeb address\t%s\nWeb token file\t%s\n",
			info.WebListen, info.WebAddress, info.WebTokenPath)
	}
	fmt.Fprintf(w, "Web public read\t%t\nActive queues\t%d\nInactive queues\t%d\n",
		info.WebPublicRead, info.ActiveQueues, info.InactiveQueues)
	return errors.Join(append(purgeErrors, w.Flush())...)
}
