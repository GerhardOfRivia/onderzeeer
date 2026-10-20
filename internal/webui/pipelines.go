package webui

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/GerhardOfRivia/onderzeeer/internal/commandparse"
	"github.com/GerhardOfRivia/onderzeeer/internal/config"
	"gopkg.in/yaml.v3"
)

const maxPipelineBody = 2 << 20

type pipelineDiagnostic struct {
	Kind     string `json:"kind"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Path     string `json:"path,omitempty"`
	Line     int    `json:"line,omitempty"`
	Column   int    `json:"column,omitempty"`
}
type pipelineValidation struct {
	Valid       bool                 `json:"valid"`
	Diagnostics []pipelineDiagnostic `json:"diagnostics"`
}

var diagnosticPath = regexp.MustCompile(`^(?:watches\[\d+\]|queue|database|values|resources)(?:\.[a-z_]+|\[\d+\])*`)
var diagnosticLine = regexp.MustCompile(`line (\d+)`)
var pathParts = regexp.MustCompile(`[a-z_]+|\d+`)

func (api *apiServer) handlePipelineValidate(output http.ResponseWriter, request *http.Request) {
	var body struct {
		YAML string `json:"yaml"`
	}
	if err := decodeMutation(output, request, &body, maxPipelineBody); err != nil {
		writeAPIError(output, 400, "invalid_request", err.Error())
		return
	}
	result := pipelineValidation{Diagnostics: []pipelineDiagnostic{}}
	var root yaml.Node
	decoder := yaml.NewDecoder(strings.NewReader(body.YAML))
	err := decoder.Decode(&root)
	if err == nil {
		var extra yaml.Node
		if next := decoder.Decode(&extra); !errors.Is(next, io.EOF) {
			if next == nil {
				err = fmt.Errorf("multiple YAML documents are not supported")
			} else {
				err = next
			}
		}
	}
	if err != nil && !errors.Is(err, io.EOF) {
		result.Diagnostics = append(result.Diagnostics, locateDiagnostic(&root, "syntax", err.Error()))
	} else {
		cfg, decodeErr := config.Decode([]byte(body.YAML))
		if decodeErr != nil {
			diagnostic := locateDiagnostic(&root, "configuration", decodeErr.Error())
			if errors.Is(decodeErr, io.EOF) || strings.Contains(decodeErr.Error(), "is required") || strings.Contains(decodeErr.Error(), "at least one") {
				diagnostic.Severity = "warning"
			}
			result.Diagnostics = append(result.Diagnostics, diagnostic)
		} else {
			result.Valid = true
			for wi, watch := range cfg.Watches {
				for si, step := range watch.Pipeline {
					for _, warning := range step.Warnings() {
						d := locateDiagnostic(&root, "warning", fmt.Sprintf("watches[%d].pipeline[%d]: %s", wi, si, warning))
						d.Severity = "warning"
						result.Diagnostics = append(result.Diagnostics, d)
					}
				}
			}
		}
	}
	writeJSON(output, http.StatusOK, result)
}

// Missing fields point at their nearest existing parent. Keep the original
// validator message intact; the config package remains the semantic authority.
func locateDiagnostic(root *yaml.Node, kind, message string) pipelineDiagnostic {
	d := pipelineDiagnostic{Kind: kind, Severity: "error", Message: message, Path: diagnosticPath.FindString(message)}
	node := root
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		node = node.Content[0]
	}
	for _, part := range pathParts.FindAllString(d.Path, -1) {
		var next *yaml.Node
		if node.Kind == yaml.MappingNode {
			for i := 0; i < len(node.Content); i += 2 {
				if node.Content[i].Value == part {
					next = node.Content[i+1]
					break
				}
			}
		}
		if node.Kind == yaml.SequenceNode {
			if index, err := strconv.Atoi(part); err == nil && index < len(node.Content) {
				next = node.Content[index]
			}
		}
		if next == nil {
			break
		}
		node = next
	}
	d.Line, d.Column = node.Line, node.Column
	if match := diagnosticLine.FindStringSubmatch(message); len(match) > 1 {
		d.Line, _ = strconv.Atoi(match[1])
	}
	return d
}

func (api *apiServer) handlePipelineParse(output http.ResponseWriter, request *http.Request) {
	var body struct {
		Command string `json:"command"`
		Name    string `json:"name"`
	}
	if err := decodeMutation(output, request, &body, maxPipelineBody); err != nil {
		writeAPIError(output, 400, "invalid_request", err.Error())
		return
	}
	args, err := commandparse.Tokenize(body.Command)
	if err != nil {
		writeAPIError(output, 400, "invalid_command", err.Error())
		return
	}
	fragment, warnings, err := commandparse.Parse(args, strings.TrimSpace(body.Name))
	if err != nil {
		writeAPIError(output, 400, "invalid_command", err.Error())
		return
	}
	if warnings == nil {
		warnings = []string{}
	}
	writeJSON(output, 200, struct {
		YAML     string   `json:"yaml"`
		Warnings []string `json:"warnings"`
	}{fragment, warnings})
}
