package rules

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/cel-go/cel"
	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// Rule represents a cleanup rule
type Rule struct {
	ID         string   `yaml:"id"`
	Resources  []string `yaml:"resources"`
	Expression string   `yaml:"expression"`
	TTL        string   `yaml:"ttl"`
}

// File represents a collection of rules from a YAML file
type File struct {
	Rules []Rule `yaml:"rules"`
}

// Engine is the rules evaluation engine
type Engine struct {
	rules []compiledRule
}

type compiledRule struct {
	rule        Rule
	program     cel.Program
	ttlDuration time.Duration
}

var idRegex = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// LoadFromFile loads rules from a YAML file
func LoadFromFile(path string) (*Engine, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read rules file: %w", err)
	}

	var rulesFile File
	if err := yaml.Unmarshal(data, &rulesFile); err != nil {
		return nil, fmt.Errorf("failed to parse rules file: %w", err)
	}

	return New(rulesFile.Rules)
}

// New creates a new rules engine
func New(rules []Rule) (*Engine, error) {
	env, err := cel.NewEnv(
		cel.Variable("object", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("_context", cel.MapType(cel.StringType, cel.DynType)),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create CEL environment: %w", err)
	}

	engine := &Engine{
		rules: make([]compiledRule, 0, len(rules)),
	}

	for _, rule := range rules {
		// Validate rule ID
		if !idRegex.MatchString(rule.ID) {
			return nil, fmt.Errorf("invalid rule ID '%s': must be lowercase and match ^[a-z][a-z0-9-]*$", rule.ID)
		}

		// Parse TTL
		ttlDuration, err := parseExtendedDuration(rule.TTL)
		if err != nil {
			return nil, fmt.Errorf("invalid TTL '%s' in rule '%s': %w", rule.TTL, rule.ID, err)
		}

		// Compile expression
		ast, issues := env.Compile(rule.Expression)
		if issues != nil && issues.Err() != nil {
			return nil, fmt.Errorf("failed to compile expression for rule '%s': %w", rule.ID, issues.Err())
		}

		program, err := env.Program(ast)
		if err != nil {
			return nil, fmt.Errorf("failed to create program for rule '%s': %w", rule.ID, err)
		}

		engine.rules = append(engine.rules, compiledRule{
			rule:        rule,
			program:     program,
			ttlDuration: ttlDuration,
		})
	}

	return engine, nil
}

// Evaluate evaluates all rules against an object and returns the first matching rule
func (e *Engine) Evaluate(obj *unstructured.Unstructured) (*Rule, time.Duration) {
	for _, compiledRule := range e.rules {
		if matches := e.evaluateRule(compiledRule, obj); matches {
			return &compiledRule.rule, compiledRule.ttlDuration
		}
	}
	return nil, 0
}

func (e *Engine) evaluateRule(rule compiledRule, obj *unstructured.Unstructured) bool {
	// Check if resource type matches
	if !e.resourceMatches(rule.rule.Resources, obj.GetKind()) {
		return false
	}

	// Prepare input for CEL evaluation
	input := map[string]interface{}{
		"object":   obj.Object,
		"_context": make(map[string]interface{}),
	}

	// Evaluate expression
	out, _, err := rule.program.Eval(input)
	if err != nil {
		logrus.WithError(err).WithField("rule", rule.rule.ID).Debug("Failed to evaluate rule expression")
		return false
	}

	// Check if result is truthy
	switch v := out.Value().(type) {
	case bool:
		return v
	case string:
		return v != ""
	case []interface{}:
		return len(v) > 0
	case map[string]interface{}:
		return len(v) > 0
	default:
		return false
	}
}

func (e *Engine) resourceMatches(resources []string, kind string) bool {
	for _, r := range resources {
		if r == "*" || r == kind || r == pluralize(kind) {
			return true
		}
	}
	return false
}

// Simple pluralization function for common Kubernetes resources
func pluralize(kind string) string {
	// Handle special cases
	switch kind {
	case "Pod":
		return "pods"
	case "Service":
		return "services"
	case "Deployment":
		return "deployments"
	case "StatefulSet":
		return "statefulsets"
	case "DaemonSet":
		return "daemonsets"
	case "ReplicaSet":
		return "replicasets"
	case "ConfigMap":
		return "configmaps"
	case "Secret":
		return "secrets"
	case "PersistentVolumeClaim":
		return "persistentvolumeclaims"
	case "PersistentVolume":
		return "persistentvolumes"
	case "Namespace":
		return "namespaces"
	case "Ingress":
		return "ingresses"
	case "NetworkPolicy":
		return "networkpolicies"
	default:
		// Default: lowercase and add 's'
		return kind + "s"
	}
}

// parseExtendedDuration parses duration strings with extended units:
// - Standard Go units: h, m, s, ms, us, ns
// - Extended units: d (days), w (weeks), month/months
// Examples: "7d", "2w", "1month", "2w3d", "1month2w3d12h30m"
func parseExtendedDuration(s string) (time.Duration, error) {
	// First try standard Go duration parsing
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}

	// Extended parsing with regex
	re := regexp.MustCompile(`(\d+(?:\.\d+)?)\s*(months?|w|d|h|m|s|ms|us|µs|ns)`)
	matches := re.FindAllStringSubmatch(s, -1)

	if len(matches) == 0 {
		return 0, fmt.Errorf("invalid duration format: %s", s)
	}

	var totalDuration time.Duration

	for _, match := range matches {
		value, err := strconv.ParseFloat(match[1], 64)
		if err != nil {
			return 0, fmt.Errorf("invalid number in duration: %s", match[1])
		}

		unit := match[2]
		var unitDuration time.Duration

		switch unit {
		case "month", "months":
			// Approximate month as 30 days
			unitDuration = time.Duration(value * 30 * 24 * float64(time.Hour))
		case "w":
			unitDuration = time.Duration(value * 7 * 24 * float64(time.Hour))
		case "d":
			unitDuration = time.Duration(value * 24 * float64(time.Hour))
		case "h":
			unitDuration = time.Duration(value * float64(time.Hour))
		case "m":
			unitDuration = time.Duration(value * float64(time.Minute))
		case "s":
			unitDuration = time.Duration(value * float64(time.Second))
		case "ms":
			unitDuration = time.Duration(value * float64(time.Millisecond))
		case "us", "µs":
			unitDuration = time.Duration(value * float64(time.Microsecond))
		case "ns":
			unitDuration = time.Duration(value * float64(time.Nanosecond))
		default:
			return 0, fmt.Errorf("unknown time unit: %s", unit)
		}

		totalDuration += unitDuration
	}

	// Verify we consumed the entire string (ignoring whitespace)
	consumed := ""
	for _, match := range matches {
		consumed += match[0]
	}
	if strings.ReplaceAll(s, " ", "") != strings.ReplaceAll(consumed, " ", "") {
		return 0, fmt.Errorf("invalid duration format: %s", s)
	}

	return totalDuration, nil
}
