package commandparse

import (
	"fmt"
	"strings"
	"unicode"
)

// Tokenize splits a pasted POSIX-style invocation without a shell, expansions,
// or execution. Shell operators and expansions require manual shell editing.
func Tokenize(source string) ([]string, error) {
	var words []string
	var word strings.Builder
	var quote rune
	started := false
	runes := []rune(source)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		if c == 0 {
			return nil, fmt.Errorf("NUL is not supported")
		}
		if quote == '\'' {
			if c == '\'' {
				quote = 0
			} else {
				word.WriteRune(c)
			}
			continue
		}
		if c == '\\' {
			if i+1 == len(runes) {
				return nil, fmt.Errorf("unfinished escape")
			}
			next := runes[i+1]
			if quote == '"' && !strings.ContainsRune("$`\"\\\n", next) {
				word.WriteRune(c)
				continue
			}
			i++
			if next != '\n' {
				word.WriteRune(next)
				started = true
			}
			continue
		}
		if c == '$' || c == '`' {
			return nil, fmt.Errorf("shell expansion is not supported; use literal single quotes or edit a shell step manually")
		}
		if quote == '"' {
			if c == '"' {
				quote = 0
			} else {
				word.WriteRune(c)
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			started = true
			continue
		}
		if strings.ContainsRune("|;&<>()", c) {
			return nil, fmt.Errorf("shell operator %q is not supported; edit a shell step manually", c)
		}
		if c == '#' && !started {
			return nil, fmt.Errorf("shell comments are not supported")
		}
		if unicode.IsSpace(c) {
			if started {
				words = append(words, word.String())
				word.Reset()
				started = false
			}
			if c == '\n' && len(words) > 0 && strings.TrimSpace(string(runes[i+1:])) != "" {
				return nil, fmt.Errorf("multiple command lines require a shell step; use backslash-newline for continuation")
			}
			continue
		}
		word.WriteRune(c)
		started = true
	}
	if quote != 0 {
		return nil, fmt.Errorf("unclosed quote")
	}
	if started {
		words = append(words, word.String())
	}
	if len(words) == 0 {
		return nil, fmt.Errorf("enter a command")
	}
	if strings.Contains(words[0], "=") {
		return nil, fmt.Errorf("environment assignments require manual env fields")
	}
	return words, nil
}
