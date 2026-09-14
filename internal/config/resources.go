package config

import (
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// Inspect YAML types before decoding into strings: yaml.v3 otherwise coerces
// numbers and booleans and treats explicit nulls like omitted fields. Decoding
// mappings also respects YAML merges and aliases, just like the main loader.
func validateResourceFields(source io.Reader) error {
	var fields struct {
		Resources map[string]yaml.Node `yaml:",inline"`
		Watches   []struct {
			Name     string                 `yaml:"name"`
			Pipeline []map[string]yaml.Node `yaml:"pipeline"`
		} `yaml:"watches"`
	}
	if err := yaml.NewDecoder(source).Decode(&fields); err != nil {
		return err
	}
	if node, exists := fields.Resources["resources"]; exists {
		node := resourceNode(&node)
		if node.Kind != yaml.SequenceNode || node.Tag != "!!seq" {
			return fmt.Errorf("resources must be a list of resource names (got %q)", node.Value)
		}
		for index, item := range node.Content {
			if err := validateResourceScalar(item); err != nil {
				return fmt.Errorf("resources[%d]: %w", index, err)
			}
		}
	}
	for _, watch := range fields.Watches {
		for index, step := range watch.Pipeline {
			if node, exists := step["resources"]; exists {
				if err := validateResourceScalar(&node); err != nil {
					name := step["name"]
					return fmt.Errorf("watch %q, step %d (%q): %w", watch.Name, index+1, resourceNode(&name).Value, err)
				}
			}
		}
	}
	return nil
}

func resourceNode(node *yaml.Node) *yaml.Node {
	for node.Kind == yaml.AliasNode {
		node = node.Alias
	}
	return node
}

func validateResourceScalar(node *yaml.Node) error {
	node = resourceNode(node)
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return fmt.Errorf("resource %q must be a single nonempty literal string", node.Value)
	}
	return validateResourceName(node.Value)
}

func validateResourceName(name string) error {
	if name == "" || strings.TrimSpace(name) != name {
		return fmt.Errorf("resource %q must be nonempty and have no surrounding whitespace", name)
	}
	return nil
}

// ValidateResources checks declarations and step references without performing
// I/O or changing the configuration. Validate includes these same checks.
func (c *Config) ValidateResources() error {
	used := make(map[string]bool, len(c.Resources))
	for _, name := range c.Resources {
		if err := validateResourceName(name); err != nil {
			return err
		}
		if _, exists := used[name]; exists {
			return fmt.Errorf("resource %q is declared more than once", name)
		}
		used[name] = false
	}
	for _, watch := range c.Watches {
		for index, step := range watch.Pipeline {
			if step.Resources == "" {
				continue
			}
			prefix := fmt.Sprintf("watch %q, step %d (%q)", watch.Name, index+1, step.Name)
			if err := validateResourceName(step.Resources); err != nil {
				return fmt.Errorf("%s: %w", prefix, err)
			}
			if _, exists := used[step.Resources]; !exists {
				return fmt.Errorf("%s: resource %q is not declared", prefix, step.Resources)
			}
			used[step.Resources] = true
		}
	}
	for _, name := range c.Resources {
		if !used[name] {
			return fmt.Errorf("resource %q is declared but unused by any pipeline step", name)
		}
	}
	return nil
}
