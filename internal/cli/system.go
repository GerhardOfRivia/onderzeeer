package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/GerhardOfRivia/onderzeeer/internal/control"
)

func systemCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags := newFlagSet("system", stderr, "onderzeeer system [--purge] [--socket path]")
	socketPath := flags.String("socket", "", "control socket (defaults to ONDERZEEER_SOCKET or a per-user path)")
	purge := flags.Bool("purge", false, "show inactive queues and confirm permanent removal of their registrations and job history")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usageError{message: "system does not accept positional arguments"}
	}
	client := control.NewClient(*socketPath)
	defer client.CloseIdleConnections()
	var purgeErrors []error
	if *purge {
		ctx, cancel := context.WithTimeout(context.Background(), controlTimeout)
		instances, err := client.List(ctx, true)
		cancel()
		if err != nil {
			return err
		}
		var candidates []control.Instance
		for _, instance := range instances {
			if !instance.Active() {
				candidates = append(candidates, instance)
			}
		}
		if len(candidates) == 0 {
			fmt.Fprintln(stdout, "No inactive queues to purge.")
			return nil
		}
		confirmed, err := confirmSystemPurge(stdin, stdout, candidates)
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Fprintln(stdout, "Purge canceled. No queues were removed.")
			return nil
		}
		// The confirmation may take arbitrarily long. Give deletion a fresh
		// timeout and send only the snapshots the user actually reviewed.
		ctx, cancel = context.WithTimeout(context.Background(), controlTimeout)
		result, err := client.PurgeSelected(ctx, candidates)
		cancel()
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
	ctx, cancel := context.WithTimeout(context.Background(), controlTimeout)
	defer cancel()
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

func confirmSystemPurge(stdin io.Reader, output io.Writer, candidates []control.Instance) (bool, error) {
	if _, err := fmt.Fprintf(output, "WARNING! This will permanently remove %d inactive queue(s):\n\n", len(candidates)); err != nil {
		return false, err
	}
	w := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tSTATE\tDATABASE")
	for _, instance := range candidates {
		fmt.Fprintf(w, "%s\t%s\t%s\t%q\n", instance.ID, instance.Name, instance.State, instance.DatabasePath)
	}
	if err := w.Flush(); err != nil {
		return false, err
	}
	if _, err := fmt.Fprint(output, "\nThis deletes their registrations, queue databases, SQLite sidecar files,\nall jobs, execution history, and captured output.\nConfig files and watched files are kept. Running and stopping instances are kept.\nThis cannot be undone.\n\nAre you sure you want to continue? [y/N] "); err != nil {
		return false, err
	}
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	answer, err := bufio.NewReader(stdin).ReadString('\n')
	if _, writeErr := fmt.Fprintln(output); writeErr != nil {
		return false, writeErr
	}
	if errors.Is(err, io.EOF) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read purge confirmation: %w", err)
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes", nil
}
