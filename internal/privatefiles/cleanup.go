package privatefiles

import (
	"context"
	"errors"
	"fmt"
	"hoanxu/internal/platform"
	"os"
	"path/filepath"
)

// Cleanup only removes explicitly classified CSVs, never evidence or legacy files.
func Cleanup(ctx context.Context, s *platform.Store, root string) error {
	rows, err := s.Pool.Query(ctx, `WITH candidates AS (
 SELECT f.id FROM private_files f WHERE f.purpose='csv' AND (
 f.lifecycle='deleting' OR (f.lifecycle='staged' AND f.created_at<now()-interval '24 hours') OR
 (f.lifecycle='attached' AND f.created_at<now()-interval '30 days' AND EXISTS
 (SELECT 1 FROM import_batches b WHERE b.file_id=f.id AND b.status IN ('preview','completed','failed'))))
 ORDER BY f.created_at LIMIT 100 FOR UPDATE SKIP LOCKED)
 UPDATE private_files f SET lifecycle='deleting' FROM candidates c WHERE f.id=c.id RETURNING f.id::text,f.path`)
	if err != nil {
		return err
	}
	type file struct{ id, path string }
	var files []file
	for rows.Next() {
		var f file
		if err = rows.Scan(&f.id, &f.path); err != nil {
			rows.Close()
			return err
		}
		files = append(files, f)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, f := range files {
		if f.path == "" || filepath.Base(f.path) != f.path || f.path == "." || f.path == ".." {
			return fmt.Errorf("invalid private file path")
		}
		err = os.Remove(filepath.Join(root, f.path))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if _, err = s.Pool.Exec(ctx, `DELETE FROM private_files WHERE id=$1 AND lifecycle='deleting' AND purpose='csv'`, f.id); err != nil {
			return err
		}
	}
	return nil
}
