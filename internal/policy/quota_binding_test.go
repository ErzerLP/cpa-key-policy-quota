package policy

import (
	"errors"
	"testing"
)

func TestQuotaBindingGroup(t *testing.T) {
	key := KeyConfig{Models: []ModelRule{
		{Alias: "fast", Provider: "codex", TargetModel: "gpt-5-codex", Group: "classify:user-a"},
		{Alias: "deep", Provider: "codex", TargetModel: "gpt-5.4", Group: "CLASSIFY:USER-A"},
		{Alias: "sonnet", Provider: "claude", TargetModel: "claude-sonnet"},
	}}
	group, err := QuotaBindingGroup(key)
	if err != nil {
		t.Fatal(err)
	}
	if group != "classify:user-a" {
		t.Fatalf("group = %q, want classify:user-a", group)
	}
}

func TestQuotaBindingGroupRejectsUnsafeBindings(t *testing.T) {
	tests := []struct {
		name string
		key  KeyConfig
		want error
	}{
		{name: "no codex", key: KeyConfig{Models: []ModelRule{{Provider: "claude"}}}, want: ErrQuotaBindingMissing},
		{name: "ungrouped", key: KeyConfig{Models: []ModelRule{{Provider: "codex"}}}, want: ErrQuotaBindingUnscoped},
		{name: "built in tier", key: KeyConfig{Models: []ModelRule{{Provider: "codex", Group: "team"}}}, want: ErrQuotaBindingNotClassified},
		{name: "multiple groups", key: KeyConfig{Models: []ModelRule{{Provider: "codex", Group: "classify:a"}, {Provider: "codex", Group: "classify:b"}}}, want: ErrQuotaBindingAmbiguous},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := QuotaBindingGroup(test.key)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}
