package pick

import (
	"errors"
	"testing"
)

func TestPick(t *testing.T) {
	items := []Item{
		{ID: "org-1", Slug: "acme-corp", Name: "Acme"},
		{ID: "org-2", Slug: "other", Name: "Other"},
	}

	tests := []struct {
		name    string
		items   []Item
		query   string
		wantID  string
		wantErr error
	}{
		{name: "only row", items: items[:1], wantID: "org-1"},
		{name: "many need a choice", items: items, wantErr: ErrChoose},
		{name: "slug", items: items, query: "ACME-CORP", wantID: "org-1"},
		{name: "name", items: items, query: "other", wantID: "org-2"},
		{name: "id", items: items, query: "org-2", wantID: "org-2"},
		{name: "unknown", items: items, query: "missing", wantErr: ErrNotFound},
		{name: "empty account", wantErr: ErrEmpty},
		{
			name: "ambiguous name",
			items: []Item{
				{ID: "a", Name: "Same"},
				{ID: "b", Name: "same"},
			},
			query:   "same",
			wantErr: ErrAmbiguous,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Pick(tt.items, tt.query)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.ID != tt.wantID {
				t.Fatalf("id = %s, want %s", got.ID, tt.wantID)
			}
		})
	}
}
