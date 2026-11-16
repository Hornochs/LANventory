package store

import (
	"context"
)

// SettingSeededCategories marks that the category templates were created once.
const SettingSeededCategories = "seeded_categories"

type seedField struct {
	key, label, typ, unit string
	options               []string
	required              bool
}

type seedCategory struct {
	name, prefix string
	dguv         bool
	fields       []seedField
}

// The templates are ordinary, editable data in German (the product targets
// German LAN parties); field keys are English.
var seedCategories = []seedCategory{
	{"Steckerleiste", "SL", true, []seedField{
		{"length_m", "Länge", FieldNumber, "m", nil, true},
		{"sockets", "Anzahl Steckplätze", FieldNumber, "", nil, false},
		{"switch", "Schalter", FieldBool, "", nil, false},
		{"surge_protection", "Überspannungsschutz", FieldBool, "", nil, false},
	}},
	{"Kabeltrommel", "KT", true, []seedField{
		{"length_m", "Länge", FieldNumber, "m", nil, true},
		{"cross_section", "Querschnitt", FieldSelect, "", []string{"1,5 mm²", "2,5 mm²"}, false},
		{"connector", "Anschluss", FieldSelect, "", []string{"Schuko", "CEE 16 A blau", "CEE 16 A rot", "CEE 32 A rot"}, false},
		{"sockets", "Anzahl Steckdosen", FieldNumber, "", nil, false},
	}},
	{"Kaltgerätekabel", "KG", true, []seedField{
		{"length_m", "Länge", FieldNumber, "m", nil, true},
	}},
	{"Netzwerkkabel", "NK", false, []seedField{
		{"length_m", "Länge", FieldNumber, "m", nil, true},
		{"cable_category", "Kategorie", FieldSelect, "", []string{"Cat 5e", "Cat 6", "Cat 6a", "Cat 7", "LWL"}, false},
		{"color", "Farbe", FieldText, "", nil, false},
	}},
	{"Switch", "SW", true, []seedField{
		{"ports", "Ports", FieldNumber, "", nil, false},
		{"uplinks", "Uplink-Ports", FieldNumber, "", nil, false},
		{"managed", "Managed", FieldBool, "", nil, false},
		{"poe", "PoE", FieldBool, "", nil, false},
	}},
	{"Monitor", "MO", true, []seedField{
		{"size_in", "Diagonale", FieldNumber, "Zoll", nil, false},
		{"resolution", "Auflösung", FieldText, "", nil, false},
	}},
	{"PC / Server", "PC", true, []seedField{
		{"cpu", "CPU", FieldText, "", nil, false},
		{"ram_gb", "RAM", FieldNumber, "GB", nil, false},
		{"os", "Betriebssystem", FieldText, "", nil, false},
	}},
}

// SeedCategories creates the category templates once. A row in settings
// records that it ran, so categories an admin deleted do not come back. It
// reports whether it seeded.
func (db *DB) SeedCategories(ctx context.Context) (bool, error) {
	seeded := false
	err := db.InTx(ctx, func(tx *Tx) error {
		var n int
		if err := tx.tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM settings WHERE key = ?`, SettingSeededCategories).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return nil
		}
		for _, sc := range seedCategories {
			var exists int
			if err := tx.tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM categories WHERE name = ? OR prefix = ?`,
				sc.name, sc.prefix).Scan(&exists); err != nil {
				return err
			}
			if exists > 0 {
				continue
			}
			c := &Category{Name: sc.name, Prefix: sc.prefix, DGUVRequired: sc.dguv, DGUVIntervalMonths: 12}
			if err := insertCategory(ctx, tx.tx, c); err != nil {
				return err
			}
			for _, sf := range sc.fields {
				f := &Field{CategoryID: c.ID, Key: sf.key, Label: sf.label, Type: sf.typ, Unit: sf.unit, Options: sf.options, Required: sf.required}
				if err := insertField(ctx, tx.tx, f); err != nil {
					return err
				}
			}
		}
		if _, err := tx.tx.ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?, '1', ?)`,
			SettingSeededCategories, unix(Now())); err != nil {
			return err
		}
		seeded = true
		return nil
	})
	return seeded, err
}
