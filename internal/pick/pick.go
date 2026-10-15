package pick

import (
	"errors"
	"strings"
)

var (
	ErrEmpty     = errors.New("no matches")
	ErrChoose    = errors.New("choose one")
	ErrNotFound  = errors.New("not in account")
	ErrAmbiguous = errors.New("ambiguous")
)

type Item struct {
	ID   string
	Slug string
	Name string
}

// Pick selects one account row. An empty query selects the only row.
// A query must match an id, slug, or name from items.
func Pick(items []Item, query string) (Item, error) {
	if len(items) == 0 {
		return Item{}, ErrEmpty
	}
	query = strings.TrimSpace(query)
	if query == "" {
		if len(items) == 1 {
			return items[0], nil
		}
		return Item{}, ErrChoose
	}

	var matches []Item
	for _, item := range items {
		if item.ID == query || equalFold(item.Slug, query) || equalFold(item.Name, query) {
			matches = append(matches, item)
		}
	}
	switch len(matches) {
	case 0:
		return Item{}, ErrNotFound
	case 1:
		return matches[0], nil
	default:
		return Item{}, ErrAmbiguous
	}
}

func equalFold(value, query string) bool {
	return value != "" && strings.EqualFold(value, query)
}
