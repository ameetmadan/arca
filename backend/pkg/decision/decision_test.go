package decision

import "testing"

func TestAction(t *testing.T) {
	tests := []struct {
		action Action
		str    string
		valid  bool
	}{
		{Move, "move", true},
		{Tag, "tag", true},
		{Index, "index", true},
		{Ignore, "ignore", true},
		{AskUser, "ask_user", true},
		{Action(""), "", false},
		{Action("delete"), "delete", false},
	}
	for _, tt := range tests {
		t.Run(tt.str, func(t *testing.T) {
			if got := tt.action.String(); got != tt.str {
				t.Errorf("String() = %q, want %q", got, tt.str)
			}
			if got := tt.action.Valid(); got != tt.valid {
				t.Errorf("Valid() = %v, want %v", got, tt.valid)
			}
		})
	}
}
