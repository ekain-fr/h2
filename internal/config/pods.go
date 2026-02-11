package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var podNameRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// ValidatePodName checks that a pod name matches [a-z0-9-]+.
func ValidatePodName(name string) error {
	if !podNameRe.MatchString(name) {
		return fmt.Errorf("invalid pod name %q: must match [a-z0-9-]+", name)
	}
	return nil
}

// PodRolesDir returns <h2-dir>/pods/roles/.
func PodRolesDir() string {
	return filepath.Join(ConfigDir(), "pods", "roles")
}

// PodTemplatesDir returns <h2-dir>/pods/templates/.
func PodTemplatesDir() string {
	return filepath.Join(ConfigDir(), "pods", "templates")
}

// LoadPodRole loads a role, checking pod roles first then global roles.
// Only called when --pod is specified. Without --pod, use LoadRole() (global only).
func LoadPodRole(name string) (*Role, error) {
	// Try pod-scoped role first.
	podPath := filepath.Join(PodRolesDir(), name+".yaml")
	if _, err := os.Stat(podPath); err == nil {
		return LoadRoleFrom(podPath)
	}
	// Fall back to global role.
	return LoadRole(name)
}

// ListPodRoles returns roles from <h2-dir>/pods/roles/.
func ListPodRoles() ([]*Role, error) {
	dir := PodRolesDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read pod roles dir: %w", err)
	}

	var roles []*Role
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		role, err := LoadRoleFrom(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		roles = append(roles, role)
	}
	return roles, nil
}

// PodTemplate defines a set of agents to launch together as a pod.
type PodTemplate struct {
	PodName string             `yaml:"pod_name"`
	Agents  []PodTemplateAgent `yaml:"agents"`
}

// PodTemplateAgent defines a single agent within a pod template.
type PodTemplateAgent struct {
	Name string `yaml:"name"`
	Role string `yaml:"role"`
}

// LoadPodTemplate loads a template from <h2-dir>/pods/templates/<name>.yaml.
func LoadPodTemplate(name string) (*PodTemplate, error) {
	path := filepath.Join(PodTemplatesDir(), name+".yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read pod template: %w", err)
	}

	var tmpl PodTemplate
	if err := yaml.Unmarshal(data, &tmpl); err != nil {
		return nil, fmt.Errorf("parse pod template: %w", err)
	}
	return &tmpl, nil
}

// ListPodTemplates returns available pod templates.
func ListPodTemplates() ([]*PodTemplate, error) {
	dir := PodTemplatesDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read pod templates dir: %w", err)
	}

	var templates []*PodTemplate
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		tmpl, err := LoadPodTemplate(strings.TrimSuffix(entry.Name(), ".yaml"))
		if err != nil {
			continue
		}
		templates = append(templates, tmpl)
	}
	return templates, nil
}
