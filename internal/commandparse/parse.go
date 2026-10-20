// Package commandparse translates argument vectors without executing commands.
package commandparse

import (
	"bytes"
	"fmt"
	"github.com/GerhardOfRivia/onderzeeer/internal/config"
	"gopkg.in/yaml.v3"
	"path/filepath"
	"strings"
)

type generatedPipeline struct {
	Pipeline []generatedPipelineStep `yaml:"pipeline"`
}

type generatedPipelineStep struct {
	Name          string              `yaml:"name"`
	Executor      config.ExecutorType `yaml:"executor"`
	Program       string              `yaml:"program,omitempty"`
	Args          []string            `yaml:"args,omitempty"`
	Image         string              `yaml:"image,omitempty"`
	ContainerArgs []string            `yaml:"container_args,omitempty"`
	Mounts        []generatedMount    `yaml:"mounts,omitempty"`
	ContainerEnv  map[string]string   `yaml:"container_env,omitempty"`
	Command       string              `yaml:"command,omitempty"`
	CommandArgs   []string            `yaml:"command_args,omitempty"`
}

type generatedMount struct {
	Source  string   `yaml:"source"`
	Target  string   `yaml:"target"`
	Options []string `yaml:"options,omitempty"`
}

// Parse reuses the CLI translation rules for an already tokenized invocation.
func Parse(invocation []string, stepName string) (string, []string, error) {
	if len(invocation) == 0 || strings.TrimSpace(invocation[0]) == "" {
		return "", nil, fmt.Errorf("parse requires a command")
	}
	program := invocation[0]
	var warnings []string
	executor := generatedExecutor(program)
	commandArgs := append([]string(nil), invocation[1:]...)
	if stepName == "" {
		stepName = generatedStepName(program, executor, commandArgs)
	}
	step := generatedPipelineStep{
		Name:     stepName,
		Executor: executor,
		Args:     commandArgs,
	}
	if executor == config.ExecutorCommand || program != string(executor) {
		step.Program = program
	}
	if executor == config.ExecutorDocker || executor == config.ExecutorPodman {
		structured, fallbackReason := parseStructuredContainerRun(executor, commandArgs)
		if structured != nil {
			step.Args = nil
			step.Image = structured.image
			step.ContainerArgs = structured.containerArgs
			step.Mounts = structured.mounts
			step.ContainerEnv = structured.containerEnv
			step.Command = structured.command
			step.CommandArgs = structured.commandArgs
		} else if fallbackReason != "" {
			warnings = append(warnings, fallbackReason+"; emitted raw runtime args")
		}
	}
	command := config.CommandConfig{Executor: executor, Program: program, Args: commandArgs}
	for _, warning := range command.Warnings() {
		warnings = append(warnings, warning)
	}

	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(generatedPipeline{Pipeline: []generatedPipelineStep{step}}); err != nil {
		return "", nil, err
	}
	return output.String(), warnings, encoder.Close()
}
func generatedExecutor(program string) config.ExecutorType {
	switch filepath.Base(program) {
	case string(config.ExecutorDocker):
		return config.ExecutorDocker
	case string(config.ExecutorPodman):
		return config.ExecutorPodman
	case string(config.ExecutorApptainer):
		return config.ExecutorApptainer
	default:
		return config.ExecutorCommand
	}
}

func generatedStepName(program string, executor config.ExecutorType, args []string) string {
	name := strings.TrimSpace(filepath.Base(program))
	if name == "" {
		name = "command"
	}
	if executor != config.ExecutorCommand && len(args) > 0 {
		action := args[0]
		if action == "container" && len(args) > 1 {
			action = args[1]
		}
		if action == "run" || action == "exec" {
			name += "-" + action
		}
	}
	return name
}
