package policy

import (
	"errors"
	"strings"
)

var (
	ErrQuotaBindingMissing       = errors.New("key has no Codex credential binding")
	ErrQuotaBindingUnscoped      = errors.New("Codex model rules must all use one credential group")
	ErrQuotaBindingNotClassified = errors.New("Codex credential binding must use a classify group")
	ErrQuotaBindingAmbiguous     = errors.New("key maps to more than one Codex credential group")
)

// QuotaBindingGroup returns the one custom classification group shared by all
// Codex routes available to a key. The self-service quota page deliberately
// rejects ungrouped and built-in tier routes because neither identifies one
// concrete upstream credential.
func QuotaBindingGroup(key KeyConfig) (string, error) {
	groups := make(map[string]struct{})
	foundCodex := false
	for _, rule := range key.Models {
		if !strings.EqualFold(strings.TrimSpace(rule.Provider), "codex") {
			continue
		}
		foundCodex = true
		group := strings.ToLower(strings.TrimSpace(rule.Group))
		if group == "" {
			return "", ErrQuotaBindingUnscoped
		}
		if !IsClassifyGroup(group) {
			return "", ErrQuotaBindingNotClassified
		}
		groups[group] = struct{}{}
	}
	if !foundCodex {
		return "", ErrQuotaBindingMissing
	}
	if len(groups) != 1 {
		return "", ErrQuotaBindingAmbiguous
	}
	for group := range groups {
		return group, nil
	}
	return "", ErrQuotaBindingMissing
}
