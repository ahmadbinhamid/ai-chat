package evals

import "testing"

func TestSelect(t *testing.T) {
	tests := []struct {
		ids  string
		want []string
	}{
		{"", nil},
		{"homepage_redesign", []string{"homepage_redesign"}},
		{" small_copy_tweak , homepage_redesign ", []string{"homepage_redesign", "small_copy_tweak"}},
		{"nope", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.ids, func(t *testing.T) {
			got := Select(tt.ids)
			if tt.want == nil {
				if len(got) != len(Tasks) {
					t.Errorf("Select(%q) = %d tasks, want all %d", tt.ids, len(got), len(Tasks))
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("Select(%q) = %d tasks, want %v", tt.ids, len(got), tt.want)
			}
			for i, id := range tt.want {
				if got[i].ID != id {
					t.Errorf("Select(%q)[%d] = %q, want %q", tt.ids, i, got[i].ID, id)
				}
			}
		})
	}
}
