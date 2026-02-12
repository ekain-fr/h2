package tmpl

import (
	"fmt"
	"sort"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

// VarDef defines a template variable with optional default.
// Default is a pointer: nil means "required" (no default), non-nil means "optional".
type VarDef struct {
	Description string  `yaml:"description"`
	Default     *string `yaml:"default"`
}

// Required returns true if the variable has no default value.
func (v VarDef) Required() bool {
	return v.Default == nil
}

// Context holds all template data available during rendering.
type Context struct {
	AgentName string
	RoleName  string
	PodName   string
	Index     int
	Count     int
	H2Dir     string
	Var       map[string]string
}

// Render processes a template string with the given context.
// Returns the rendered string or an error with source context.
func Render(templateText string, ctx *Context) (string, error) {
	t, err := template.New("").Funcs(funcMap()).Parse(templateText)
	if err != nil {
		return "", fmt.Errorf("template parse error: %w", err)
	}

	var buf strings.Builder
	if err := t.Execute(&buf, ctx); err != nil {
		return "", fmt.Errorf("template execution error: %w", err)
	}
	return buf.String(), nil
}

// ParseVarDefs extracts variable definitions from raw YAML text.
// Returns the definitions and the YAML text with the variables section removed.
// The variables section must not contain template expressions.
func ParseVarDefs(yamlText string) (map[string]VarDef, string, error) {
	// First, parse the full YAML to extract the variables section.
	var doc map[string]yaml.Node
	if err := yaml.Unmarshal([]byte(yamlText), &doc); err != nil {
		return nil, "", fmt.Errorf("parse YAML for variable extraction: %w", err)
	}

	varsNode, ok := doc["variables"]
	if !ok {
		return nil, yamlText, nil
	}

	// Decode variables section into map[string]VarDef.
	var defs map[string]VarDef
	if err := varsNode.Decode(&defs); err != nil {
		return nil, "", fmt.Errorf("parse variables section: %w", err)
	}
	if defs == nil {
		defs = map[string]VarDef{}
	}

	// Remove the variables section from the YAML text.
	// We do this by finding the "variables:" line and removing it plus its indented block.
	remaining := removeYAMLSection(yamlText, "variables")

	return defs, remaining, nil
}

// removeYAMLSection removes a top-level YAML key and its block from the text.
func removeYAMLSection(yamlText string, key string) string {
	lines := strings.Split(yamlText, "\n")
	var result []string
	inSection := false
	prefix := key + ":"

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if !inSection {
			if trimmed == prefix || strings.HasPrefix(trimmed, prefix+" ") {
				inSection = true
				continue
			}
			result = append(result, line)
		} else {
			// We're inside the section. Stay in it while lines are indented
			// (or blank). Exit when we hit a non-indented, non-blank line.
			if trimmed == "" {
				// Blank lines inside the section are consumed.
				continue
			}
			// Check if this line is indented (belongs to the section).
			if line != trimmed {
				// Line has leading whitespace — still in section.
				continue
			}
			// Non-indented, non-blank line — section is over.
			inSection = false
			result = append(result, line)
		}
	}

	return strings.Join(result, "\n")
}

// ValidateVars checks that all required variables (no default) are provided.
// Returns a descriptive error listing all missing variables with descriptions.
func ValidateVars(defs map[string]VarDef, provided map[string]string) error {
	var missing []string
	for name, def := range defs {
		if def.Required() {
			if _, ok := provided[name]; !ok {
				missing = append(missing, name)
			}
		}
	}
	if len(missing) == 0 {
		return nil
	}

	sort.Strings(missing)

	var buf strings.Builder
	buf.WriteString("required variables not provided:\n\n")
	for _, name := range missing {
		desc := defs[name].Description
		if desc != "" {
			fmt.Fprintf(&buf, "  %-16s — %s\n", name, desc)
		} else {
			fmt.Fprintf(&buf, "  %s\n", name)
		}
	}
	buf.WriteString("\nProvide them with: --var ")
	for i, name := range missing {
		if i > 0 {
			buf.WriteString(" --var ")
		}
		fmt.Fprintf(&buf, "%s=VALUE", name)
	}
	return fmt.Errorf("%s", buf.String())
}

// funcMap returns the custom template functions.
func funcMap() template.FuncMap {
	return template.FuncMap{
		"seq":       seqFunc,
		"split":     splitFunc,
		"join":      joinFunc,
		"default":   defaultFunc,
		"upper":     strings.ToUpper,
		"lower":     strings.ToLower,
		"contains":  strings.Contains,
		"trimSpace": strings.TrimSpace,
		"quote":     quoteFunc,
	}
}

// seqFunc generates an integer sequence [start, end] inclusive.
// Returns an error if the range exceeds 1000 elements.
func seqFunc(start, end int) ([]int, error) {
	if start > end {
		return nil, nil
	}
	count := end - start + 1
	if count > 1000 {
		return nil, fmt.Errorf("seq range too large: %d elements (max 1000)", count)
	}
	result := make([]int, count)
	for i := range result {
		result[i] = start + i
	}
	return result, nil
}

func splitFunc(s, sep string) []string {
	return strings.Split(s, sep)
}

func joinFunc(elems []string, sep string) string {
	return strings.Join(elems, sep)
}

// defaultFunc returns val if non-empty, otherwise fallback.
// String semantics: "0" and "false" are non-empty.
func defaultFunc(val, fallback string) string {
	if val != "" {
		return val
	}
	return fallback
}

// quoteFunc returns a YAML-safe quoted string.
func quoteFunc(s string) string {
	// Use Go %q which produces a double-quoted string with proper escaping.
	return fmt.Sprintf("%q", s)
}
