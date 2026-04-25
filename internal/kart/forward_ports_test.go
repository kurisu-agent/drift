package kart

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestParseForwardPortsList(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		json string
		want []int
	}{
		{
			name: "numeric",
			json: `[3000, 8080]`,
			want: []int{3000, 8080},
		},
		{
			name: "strings bare",
			json: `["3000", "8080"]`,
			want: []int{3000, 8080},
		},
		{
			name: "string with label",
			json: `["3000:web", "8080:api"]`,
			want: []int{3000, 8080},
		},
		{
			name: "string with host",
			json: `["localhost:3000"]`,
			want: []int{3000},
		},
		{
			name: "out of range numeric drops",
			json: `[0, 70000, 3000]`,
			want: []int{3000},
		},
		{
			name: "garbage entry skipped",
			json: `["nope", 3000]`,
			want: []int{3000},
		},
		{
			name: "duplicate collapses",
			json: `[3000, 3000, "3000"]`,
			want: []int{3000},
		},
		{
			name: "empty",
			json: `[]`,
			want: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var raws []json.RawMessage
			if err := json.Unmarshal([]byte(c.json), &raws); err != nil {
				t.Fatalf("seed: %v", err)
			}
			got := parseForwardPortsList(raws)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}
