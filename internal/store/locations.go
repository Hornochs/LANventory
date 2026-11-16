package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Location is a place where assets are kept. Locations are a flat list.
type Location struct {
	ID          int64
	Name        string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	// AssetCount is filled by ListLocations.
	AssetCount int
}

func (l *Location) validate() *ValidationError {
	ve := newVE()
	var code string
	if l.Name, code = cleanLine(l.Name, 100); code != "" {
		ve.add("name", code)
	} else if l.Name == "" {
		ve.add("name", "required")
	}
	if l.Description, code = cleanText(l.Description, 500); code != "" {
		ve.add("description", code)
	}
	return ve
}

// CreateLocation inserts a location.
func (db *DB) CreateLocation(ctx context.Context, l *Location) error {
	if ve := l.validate(); ve.err() != nil {
		return ve
	}
	now := Now()
	res, err := db.sql.ExecContext(ctx, `INSERT INTO locations (name, description, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		l.Name, l.Description, unix(now), unix(now))
	if isUnique(err) {
		ve := newVE()
		ve.add("name", "taken")
		return ve
	}
	if err != nil {
		return err
	}
	l.ID, err = res.LastInsertId()
	l.CreatedAt, l.UpdatedAt = now, now
	return err
}

// UpdateLocation renames a location and changes its description.
func (db *DB) UpdateLocation(ctx context.Context, l *Location) error {
	if ve := l.validate(); ve.err() != nil {
		return ve
	}
	res, err := db.sql.ExecContext(ctx, `UPDATE locations SET name = ?, description = ?, updated_at = ? WHERE id = ?`,
		l.Name, l.Description, unix(Now()), l.ID)
	if isUnique(err) {
		ve := newVE()
		ve.add("name", "taken")
		return ve
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteLocation removes a location; ErrInUse while assets are stored there.
func (db *DB) DeleteLocation(ctx context.Context, id int64) error {
	var n int
	if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM assets WHERE location_id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrInUse
	}
	res, err := db.sql.ExecContext(ctx, `DELETE FROM locations WHERE id = ?`, id)
	if err != nil {
		if strings.Contains(err.Error(), "FOREIGN KEY") {
			return ErrInUse
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// LocationByID returns a location.
func (db *DB) LocationByID(ctx context.Context, id int64) (*Location, error) {
	var l Location
	var c, u int64
	err := db.sql.QueryRowContext(ctx, `SELECT id, name, description, created_at, updated_at,
		(SELECT COUNT(*) FROM assets a WHERE a.location_id = locations.id) FROM locations WHERE id = ?`, id).
		Scan(&l.ID, &l.Name, &l.Description, &c, &u, &l.AssetCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	l.CreatedAt, l.UpdatedAt = fromUnix(c), fromUnix(u)
	return &l, err
}

// ListLocations returns all locations by name with their asset counts.
func (db *DB) ListLocations(ctx context.Context) ([]*Location, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT id, name, description, created_at, updated_at,
		(SELECT COUNT(*) FROM assets a WHERE a.location_id = locations.id) FROM locations ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Location
	for rows.Next() {
		var l Location
		var c, u int64
		if err := rows.Scan(&l.ID, &l.Name, &l.Description, &c, &u, &l.AssetCount); err != nil {
			return nil, err
		}
		l.CreatedAt, l.UpdatedAt = fromUnix(c), fromUnix(u)
		out = append(out, &l)
	}
	return out, rows.Err()
}
