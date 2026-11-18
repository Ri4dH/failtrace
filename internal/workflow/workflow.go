// Package workflow parses the parts of a GitHub Actions workflow file that
// matter for locating and reproducing a failed step.
package workflow

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Workflow is a parsed workflow file.
type Workflow struct {
	Name     string            `json:"name,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Defaults Defaults          `json:"defaults"`
	Jobs     []*Job            `json:"jobs"`
}

// Defaults holds `defaults.run` settings.
type Defaults struct {
	Shell            string `json:"shell,omitempty"`
	WorkingDirectory string `json:"working_directory,omitempty"`
}

// Job is a single job definition.
type Job struct {
	ID        string            `json:"id"`
	Name      string            `json:"name,omitempty"`
	RunsOn    []string          `json:"runs_on,omitempty"`
	Needs     []string          `json:"needs,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Defaults  Defaults          `json:"defaults"`
	Container string            `json:"container,omitempty"`
	Services  []Service         `json:"services,omitempty"`
	Matrix    *Matrix           `json:"matrix,omitempty"`
	Steps     []Step            `json:"steps,omitempty"`
	Uses      string            `json:"uses,omitempty"` // reusable workflow call
}

// Service is a service container attached to a job.
type Service struct {
	Name  string `json:"name"`
	Image string `json:"image,omitempty"`
}

// Matrix is a job's strategy.matrix. Keys preserve definition order, which is
// the order GitHub uses in generated job names like "test (ubuntu-latest, 1.22)".
type Matrix struct {
	Keys       []string            `json:"keys,omitempty"`
	Values     map[string][]string `json:"values,omitempty"`
	HasInclude bool                `json:"has_include,omitempty"`
	HasExclude bool                `json:"has_exclude,omitempty"`
	Expression string              `json:"expression,omitempty"` // set when the whole matrix is computed
}

// Step is a single step definition.
type Step struct {
	ID               string            `json:"id,omitempty"`
	Name             string            `json:"name,omitempty"`
	Uses             string            `json:"uses,omitempty"`
	Run              string            `json:"run,omitempty"`
	Shell            string            `json:"shell,omitempty"`
	WorkingDirectory string            `json:"working_directory,omitempty"`
	If               string            `json:"if,omitempty"`
	With             map[string]string `json:"with,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
}

// DisplayName is the name GitHub shows for the step when `name` is unset.
func (s Step) DisplayName() string {
	if s.Name != "" {
		return s.Name
	}
	if s.Uses != "" {
		return "Run " + s.Uses
	}
	first, _, _ := strings.Cut(strings.TrimSpace(s.Run), "\n")
	return "Run " + strings.TrimSpace(first)
}

type rawWorkflow struct {
	Name     string      `yaml:"name"`
	Env      stringMap   `yaml:"env"`
	Defaults rawDefaults `yaml:"defaults"`
	Jobs     yaml.Node   `yaml:"jobs"`
}

type rawDefaults struct {
	Run struct {
		Shell            string `yaml:"shell"`
		WorkingDirectory string `yaml:"working-directory"`
	} `yaml:"run"`
}

type rawJob struct {
	Name      string      `yaml:"name"`
	RunsOn    yaml.Node   `yaml:"runs-on"`
	Needs     yaml.Node   `yaml:"needs"`
	Env       stringMap   `yaml:"env"`
	Defaults  rawDefaults `yaml:"defaults"`
	Container yaml.Node   `yaml:"container"`
	Services  yaml.Node   `yaml:"services"`
	Strategy  struct {
		Matrix yaml.Node `yaml:"matrix"`
	} `yaml:"strategy"`
	Steps []rawStep `yaml:"steps"`
	Uses  string    `yaml:"uses"`
}

type rawStep struct {
	ID               string    `yaml:"id"`
	Name             string    `yaml:"name"`
	Uses             string    `yaml:"uses"`
	Run              string    `yaml:"run"`
	Shell            string    `yaml:"shell"`
	WorkingDirectory string    `yaml:"working-directory"`
	If               string    `yaml:"if"`
	With             stringMap `yaml:"with"`
	Env              stringMap `yaml:"env"`
}

// Parse parses workflow YAML.
func Parse(data []byte) (*Workflow, error) {
	var raw rawWorkflow
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse workflow: %w", err)
	}
	if raw.Jobs.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("parse workflow: missing or invalid 'jobs' section")
	}
	wf := &Workflow{Name: raw.Name, Env: raw.Env, Defaults: raw.Defaults.toDefaults()}
	for i := 0; i+1 < len(raw.Jobs.Content); i += 2 {
		id := raw.Jobs.Content[i].Value
		var rj rawJob
		if err := raw.Jobs.Content[i+1].Decode(&rj); err != nil {
			return nil, fmt.Errorf("parse job %q: %w", id, err)
		}
		wf.Jobs = append(wf.Jobs, rj.toJob(id))
	}
	return wf, nil
}

func (d rawDefaults) toDefaults() Defaults {
	return Defaults{Shell: d.Run.Shell, WorkingDirectory: d.Run.WorkingDirectory}
}

func (rj rawJob) toJob(id string) *Job {
	job := &Job{
		ID:        id,
		Name:      rj.Name,
		RunsOn:    parseRunsOn(&rj.RunsOn),
		Needs:     scalarList(&rj.Needs),
		Env:       rj.Env,
		Defaults:  rj.Defaults.toDefaults(),
		Container: parseContainer(&rj.Container),
		Services:  parseServices(&rj.Services),
		Matrix:    parseMatrix(&rj.Strategy.Matrix),
		Uses:      rj.Uses,
	}
	for _, rs := range rj.Steps {
		job.Steps = append(job.Steps, Step{
			ID: rs.ID, Name: rs.Name, Uses: rs.Uses, Run: rs.Run, Shell: rs.Shell,
			WorkingDirectory: rs.WorkingDirectory, If: rs.If, With: rs.With, Env: rs.Env,
		})
	}
	return job
}

// stringMap decodes a YAML mapping of scalars, tolerating expression values.
type stringMap map[string]string

func (m *stringMap) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return nil // e.g. `env: ${{ fromJSON(...) }}`: not statically known
	}
	out := make(stringMap, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		out[n.Content[i].Value] = nodeString(n.Content[i+1])
	}
	*m = out
	return nil
}

func nodeString(n *yaml.Node) string {
	if n.Kind == yaml.ScalarNode {
		return n.Value
	}
	b, err := yaml.Marshal(n)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func scalarList(n *yaml.Node) []string {
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Value == "" {
			return nil
		}
		return []string{n.Value}
	case yaml.SequenceNode:
		out := make([]string, 0, len(n.Content))
		for _, c := range n.Content {
			out = append(out, nodeString(c))
		}
		return out
	}
	return nil
}

func mappingValue(n *yaml.Node, key string) *yaml.Node {
	if n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func parseRunsOn(n *yaml.Node) []string {
	if n.Kind != yaml.MappingNode {
		return scalarList(n)
	}
	var out []string
	if g := mappingValue(n, "group"); g != nil {
		out = append(out, "group:"+g.Value)
	}
	if l := mappingValue(n, "labels"); l != nil {
		out = append(out, scalarList(l)...)
	}
	return out
}

func parseContainer(n *yaml.Node) string {
	switch n.Kind {
	case yaml.ScalarNode:
		return n.Value
	case yaml.MappingNode:
		if img := mappingValue(n, "image"); img != nil {
			return img.Value
		}
	}
	return ""
}

func parseServices(n *yaml.Node) []Service {
	if n.Kind != yaml.MappingNode {
		return nil
	}
	var out []Service
	for i := 0; i+1 < len(n.Content); i += 2 {
		svc := Service{Name: n.Content[i].Value}
		if img := mappingValue(n.Content[i+1], "image"); img != nil {
			svc.Image = img.Value
		}
		out = append(out, svc)
	}
	return out
}

func parseMatrix(n *yaml.Node) *Matrix {
	switch n.Kind {
	case yaml.ScalarNode:
		return &Matrix{Expression: n.Value}
	case yaml.MappingNode:
	default:
		return nil
	}
	m := &Matrix{Values: map[string][]string{}}
	var include *yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, val := n.Content[i].Value, n.Content[i+1]
		switch key {
		case "include":
			m.HasInclude, include = true, val
		case "exclude":
			m.HasExclude = true
		default:
			m.Keys = append(m.Keys, key)
			m.Values[key] = scalarList(val)
		}
	}
	if include != nil && include.Kind == yaml.SequenceNode {
		for _, entry := range include.Content {
			if entry.Kind != yaml.MappingNode {
				continue
			}
			for j := 0; j+1 < len(entry.Content); j += 2 {
				k, v := entry.Content[j].Value, nodeString(entry.Content[j+1])
				if _, ok := m.Values[k]; !ok {
					m.Keys = append(m.Keys, k)
				}
				if !contains(m.Values[k], v) {
					m.Values[k] = append(m.Values[k], v)
				}
			}
		}
	}
	return m
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
