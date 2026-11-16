package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/lan-dot-party/lanventory/internal/store"
)

func init() {
	dataExportCmd.Flags().StringP("output", "o", "", "write to this file instead of stdout")
	dataImportCmd.Flags().Bool("dry-run", false, "check the file and print what would happen, write nothing")
	dataImportCmd.Flags().String("on-conflict", store.OnConflictSkip, "existing inventory numbers: skip (report) or abort (fail)")
	dataImportCmd.Flags().Bool("update-categories", true, "adopt the settings of categories that already exist (prefix, DGUV, field labels/units/required) from the file; --update-categories=false keeps them")
	dataCmd.AddCommand(dataExportCmd, dataImportCmd)
	rootCmd.AddCommand(dataCmd)
}

var dataCmd = &cobra.Command{Use: "data", Short: "Export and import the whole inventory including inspections (JSON)"}

var dataExportCmd = &cobra.Command{
	Use:   "export [-o file]",
	Short: "Write the inventory, including inspections, as JSON (stdout by default)",
	Long: "The export contains locations, categories with fields, assets with field values and inspections.\n" +
		"Users, sessions, tokens, settings and the audit log are not exported.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		out, _ := cmd.Flags().GetString("output")
		e, err := open(cmd.Context())
		if err != nil {
			return err
		}
		defer e.close()
		exp, err := e.db.ExportData(cmd.Context())
		if err != nil {
			return err
		}
		b, err := json.MarshalIndent(exp, "", "  ")
		if err != nil {
			return err
		}
		b = append(b, '\n')
		w := cmd.OutOrStdout()
		if out != "" {
			// The file holds the whole inventory: owner only.
			f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
			if err != nil {
				return err
			}
			defer f.Close()
			w = f
		}
		if _, err := w.Write(b); err != nil {
			return err
		}
		loc, cats, assets, insp := exp.Summary()
		e.audit(cmd.Context(), "data.export", "data", "", map[string]any{"locations": loc, "categories": cats, "assets": assets, "inspections": insp})
		fmt.Fprintf(cmd.ErrOrStderr(), "exported %d locations, %d categories, %d assets, %d inspections\n", loc, cats, assets, insp)
		return nil
	},
}

var dataImportCmd = &cobra.Command{
	Use:   "import [--dry-run] [--on-conflict skip|abort] <file>",
	Short: "Import a JSON export (all or nothing); users are not transferred",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dry, _ := cmd.Flags().GetBool("dry-run")
		oc, _ := cmd.Flags().GetString("on-conflict")
		upd, _ := cmd.Flags().GetBool("update-categories")
		if oc != store.OnConflictSkip && oc != store.OnConflictAbort {
			return errors.New("--on-conflict must be skip or abort")
		}
		raw, err := os.ReadFile(args[0])
		if err != nil {
			return err
		}
		data, err := store.ParseDataExport(raw)
		if err != nil {
			return err
		}
		e, err := open(cmd.Context())
		if err != nil {
			return err
		}
		defer e.close()
		res, err := e.db.ImportData(cmd.Context(), data, store.DataImportOptions{DryRun: dry, OnConflict: oc, UpdateCategories: upd})
		out := cmd.OutOrStdout()
		if res != nil {
			printDataResult(out, res)
		}
		if err != nil {
			return err
		}
		if res.Committed {
			for _, a := range res.CreatedAssets {
				e.audit(cmd.Context(), "asset.create", "asset", strconv.FormatInt(a.ID, 10),
					map[string]any{"inventory_no": a.InventoryNo, "name": a.Name, "changes": a.Changes, "via": "data_import"})
			}
			e.audit(cmd.Context(), "data.import", "data", "", map[string]any{"locations": res.LocationsCreated,
				"categories": res.CategoriesCreated, "categories_updated": res.CategoriesUpdated,
				"fields": res.FieldsCreated, "fields_updated": res.FieldsUpdated, "options": res.OptionsAdded, "assets": res.AssetsCreated,
				"inspections": res.InspectionsCreated, "skipped": len(res.SkippedAssets), "warnings": len(res.Warnings), "on_conflict": oc})
		}
		return nil
	},
}

func printDataResult(w io.Writer, r *store.DataImportResult) {
	verb := "created"
	switch {
	case r.ErrorCount > 0:
		verb = "would create (nothing written, problems found)"
	case r.DryRun:
		verb = "would create (dry run, nothing written)"
	}
	fmt.Fprintf(w, "%s: %d locations, %d categories, %d fields, %d select options added, %d assets, %d inspections\n", verb,
		r.LocationsCreated, r.CategoriesCreated, r.FieldsCreated, r.OptionsAdded, r.AssetsCreated, r.InspectionsCreated)
	if r.CategoriesUpdated+r.FieldsUpdated > 0 {
		fmt.Fprintf(w, "settings adopted from the file: %d categories, %d fields\n", r.CategoriesUpdated, r.FieldsUpdated)
	}
	if len(r.SkippedAssets) > 0 {
		fmt.Fprintf(w, "skipped %d existing inventory numbers:", len(r.SkippedAssets))
		for _, n := range r.SkippedAssets {
			fmt.Fprint(w, " ", n)
		}
		fmt.Fprintln(w)
	}
	for _, i := range r.Warnings {
		fmt.Fprintln(w, "warning:", i)
	}
	for _, i := range r.Errors {
		fmt.Fprintln(w, "error:", i)
	}
	if r.ErrorCount > len(r.Errors) {
		fmt.Fprintf(w, "… %d problems in total\n", r.ErrorCount)
	}
}
