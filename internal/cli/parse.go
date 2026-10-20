package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/GerhardOfRivia/onderzeeer/internal/commandparse"
)

func parseCommand(args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("parse", stderr, "onderzeeer parse [--name name] -- <program> [argument ...]")
	name := flags.String("name", "", "pipeline step name (defaults from the program)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	invocation := flags.Args()
	if len(invocation) == 0 {
		return usageError{message: "parse requires a command"}
	}
	program := invocation[0]
	if strings.TrimSpace(program) == "" {
		return usageError{message: "parse command must not be blank"}
	}

	nameWasSet := false
	flags.Visit(func(visited *flag.Flag) {
		if visited.Name == "name" {
			nameWasSet = true
		}
	})
	stepName := strings.TrimSpace(*name)
	if nameWasSet && stepName == "" {
		return usageError{message: "parse --name must not be blank"}
	}
	result, warnings, err := commandparse.Parse(invocation, stepName)
	if err != nil {
		return err
	}
	for _, warning := range warnings {
		fmt.Fprintf(stderr, "onderzeeer parse: warning: %s\n", warning)
	}
	if _, err := io.WriteString(stdout, result); err != nil {
		return fmt.Errorf("write pipeline config: %w", err)
	}
	return nil
}
