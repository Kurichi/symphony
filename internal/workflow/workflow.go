package workflow

import (
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoadedWorkflow holds the parsed WORKFLOW.md content: YAML front matter config
// and the remaining prompt template text.
type LoadedWorkflow struct {
	Config         map[string]any
	PromptTemplate string
	Path           string
}

// Parse splits raw WORKFLOW.md content into YAML config and prompt template.
// Front matter is delimited by "---" lines. If no front matter is present,
// Config is an empty map and the entire content becomes the prompt.
func Parse(content string) (*LoadedWorkflow, error) {
	frontLines, promptLines := splitFrontMatter(content)

	cfg, err := parseFrontMatter(frontLines)
	if err != nil {
		return nil, err
	}

	prompt := strings.TrimSpace(strings.Join(promptLines, "\n"))

	return &LoadedWorkflow{
		Config:         cfg,
		PromptTemplate: prompt,
	}, nil
}

// LoadFile reads a file from disk and parses it as a WORKFLOW.md.
func LoadFile(path string) (*LoadedWorkflow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	wf, err := Parse(string(data))
	if err != nil {
		return nil, err
	}
	wf.Path = path
	return wf, nil
}

// splitFrontMatter mirrors the Elixir Workflow.split_front_matter/1 behaviour.
// Lines are split on any newline variant. If the first line is "---", everything
// up to the next "---" is front matter; the rest is the prompt.
func splitFrontMatter(content string) (frontMatter []string, prompt []string) {
	lines := strings.Split(content, "\n")
	// Normalise possible \r from Windows line endings.
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, "\r")
	}

	if len(lines) == 0 || lines[0] != "---" {
		return nil, lines
	}

	// First line is "---"; scan for the closing delimiter.
	tail := lines[1:]
	var front []string
	for i, l := range tail {
		if l == "---" {
			return front, tail[i+1:]
		}
		front = append(front, l)
	}

	// Closing "---" never found: treat everything after the opening as front matter
	// with an empty prompt (matches Elixir behaviour).
	return front, nil
}

// parseFrontMatter decodes the YAML lines into a map. An empty/blank block
// yields an empty map.
func parseFrontMatter(lines []string) (map[string]any, error) {
	raw := strings.TrimSpace(strings.Join(lines, "\n"))
	if raw == "" {
		return map[string]any{}, nil
	}

	var m map[string]any
	if err := yaml.Unmarshal([]byte(raw), &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}
