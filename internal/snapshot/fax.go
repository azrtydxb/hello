package snapshot

import (
	"context"
	"fmt"
)

// FaxDestination is a named fax destination (migration 00073): the extension
// whose faxes arrive there and an address that replaces the owner's for
// delivery.
type FaxDestination struct {
	ID        int64
	Name      string
	Extension string
	Email     string
}

// WithFaxDestinations installs the fax destinations (tests and Load).
func (s *Snapshot) WithFaxDestinations(ds []FaxDestination) *Snapshot {
	if s.faxDests == nil {
		s.faxDests = map[string]FaxDestination{}
	}
	for _, d := range ds {
		s.faxDests[d.Name] = d
	}
	return s
}

// FaxDestination returns a fax destination by name.
func (s *Snapshot) FaxDestination(name string) (FaxDestination, bool) {
	d, ok := s.faxDests[name]
	return d, ok
}

func loadFaxDestinations(ctx context.Context, db Querier) ([]FaxDestination, error) {
	rows, err := db.Query(ctx, `SELECT d.id, d.name, e.number, d.email FROM fax_destinations d
		JOIN extensions e ON e.id = d.extension_id ORDER BY d.name`)
	if err != nil {
		return nil, fmt.Errorf("snapshot: fax destinations: %w", err)
	}
	defer rows.Close()
	var out []FaxDestination
	for rows.Next() {
		var d FaxDestination
		if err := rows.Scan(&d.ID, &d.Name, &d.Extension, &d.Email); err != nil {
			return nil, fmt.Errorf("snapshot: scan fax destination: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
