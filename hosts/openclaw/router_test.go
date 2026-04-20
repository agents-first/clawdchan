package openclaw

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseActions(t *testing.T) {
	validWords := "elder thunder high travel smoke orbit rail crane mint frost echo dawn"

	tests := []struct {
		name string
		text string
		want []Action
	}{
		{
			name: "plain action lines",
			text: strings.Join([]string{
				`{"cc":"pair"}`,
				`{"cc":"peers"}`,
				`{"cc":"inbox"}`,
			}, "\n"),
			want: []Action{
				{Kind: ActionPair},
				{Kind: ActionPeers},
				{Kind: ActionInbox},
			},
		},
		{
			name: "embedded action in text",
			text: `I'll pair you now. {"cc":"pair"}`,
			want: []Action{
				{Kind: ActionPair},
			},
		},
		{
			name: "unknown cc skipped",
			text: strings.Join([]string{
				`{"cc":"not-real"}`,
				`{"cc":"pair"}`,
			}, "\n"),
			want: []Action{
				{Kind: ActionPair},
			},
		},
		{
			name: "consume valid and invalid word counts",
			text: strings.Join([]string{
				`{"cc":"consume","words":"too few words"}`,
				`{"cc":"consume","words":"` + validWords + `"}`,
				`{"cc":"consume","words":"one two three four five six seven eight nine ten eleven"}`,
			}, "\n"),
			want: []Action{
				{Kind: ActionConsume, Words: validWords},
			},
		},
		{
			name: "multiple actions in one turn",
			text: strings.Join([]string{
				`First {"cc":"pair"} and then {"cc":"peers"}`,
				`Also {"cc":"inbox"}`,
			}, "\n"),
			want: []Action{
				{Kind: ActionPair},
				{Kind: ActionPeers},
				{Kind: ActionInbox},
			},
		},
		{
			name: "empty input",
			text: "",
			want: []Action{},
		},
		{
			name: "whitespace only input",
			text: "   \n\t  ",
			want: []Action{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseActions(tc.text)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseActions mismatch:\n got: %#v\nwant: %#v", got, tc.want)
			}
		})
	}
}

func TestIsClawdChanTurn(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{
			name: "recognized action line",
			text: `{"cc":"pair"}`,
			want: true,
		},
		{
			name: "recognized embedded action",
			text: `Sure. {"cc":"inbox"}`,
			want: true,
		},
		{
			name: "unknown action",
			text: `{"cc":"not-real"}`,
			want: false,
		},
		{
			name: "invalid consume words",
			text: `{"cc":"consume","words":"one two three"}`,
			want: false,
		},
		{
			name: "whitespace only",
			text: " \n\t ",
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsClawdChanTurn(tc.text); got != tc.want {
				t.Fatalf("IsClawdChanTurn() = %v, want %v", got, tc.want)
			}
		})
	}
}
