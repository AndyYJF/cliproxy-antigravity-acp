// Code generated from CPA v8.0.72; DO NOT EDIT.
package config

type SensitivePatternRule struct {
	// Pattern is the Go RE2 regular expression to match.
	Pattern string `yaml:"pattern" json:"pattern"`

	// Replacement is the substitution string (empty string deletes matches).
	Replacement string `yaml:"replacement" json:"replacement"`
}
