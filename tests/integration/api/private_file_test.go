package api

import (
	"bytes"
	"context"
	"hoanxu/internal/affiliate"
	"hoanxu/internal/auth"
	"hoanxu/internal/privatefiles"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCSVFailureCleansFilesAndSuccessfulPreviewAttachesBeforeRetention(t *testing.T) {
	store, _, admin := testStore(t)
	ctx := context.Background()
	root := t.TempDir()
	a := &auth.Service{Store: store}
	if _, err := store.Pool.Exec(ctx, `UPDATE internal_credentials SET must_change=false WHERE user_id=$1`, admin); err != nil {
		t.Fatal(err)
	}
	tx, err := store.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	token, err := a.NewSession(ctx, tx, admin)
	if err == nil {
		err = tx.Commit(ctx)
	} else {
		_ = tx.Rollback(ctx)
	}
	if err != nil {
		t.Fatal(err)
	}
	principal, err := a.Session(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Pool.Exec(ctx, `UPDATE sessions SET reauthenticated_at=now() WHERE id=$1`, principal.SessionID); err != nil {
		t.Fatal(err)
	}
	server := New(&Server{Store: store, Auth: a, Affiliate: &affiliate.Service{Store: store}, Origin: "http://localhost:3000", PrivateDir: root})
	csv := "channel,publisher,order_id,line_id,tracking_code,date,product_name,value,commission,status\nshopee,publisher,order,line,unknown,2026-10-05,Test,100000,5000,approved\n"
	upload := func(mapping string) int {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		file, err := form.CreateFormFile("file", "preview.csv")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = file.Write([]byte(csv))
		_ = form.WriteField("mapping", mapping)
		_ = form.Close()
		r := httptest.NewRequest("POST", "/api/v1/admin/order-imports", &body)
		r.Header.Set("Content-Type", form.FormDataContentType())
		r.Header.Set("Origin", "http://localhost:3000")
		r.Header.Set("X-CSRF-Token", principal.CSRF)
		r.AddCookie(&http.Cookie{Name: "hx_session", Value: token})
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		return w.Code
	}
	if status := upload("{"); status != 422 {
		t.Fatal(status)
	}
	files, err := os.ReadDir(root)
	if err != nil || len(files) != 0 {
		t.Fatal("failed preview orphan", files, err)
	}
	var count int
	if err = store.Pool.QueryRow(ctx, `SELECT count(*) FROM private_files`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	if status := upload("{}"); status != 201 {
		t.Fatal("preview", status)
	}
	if err = store.Pool.QueryRow(ctx, `SELECT count(*) FROM import_batches b JOIN private_files f ON f.id=b.file_id WHERE f.lifecycle='attached'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("unattached preview", count, err)
	}
	for _, purpose := range []string{"legacy", "evidence"} {
		if err = os.WriteFile(filepath.Join(root, purpose), []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = store.Pool.Exec(ctx, `INSERT INTO private_files(owner_id,purpose,name,path,content_type,lifecycle,created_at) VALUES($1,$2,$3,$3,'text/plain',$4,now()-interval '60 days')`, admin, map[string]string{"legacy": "csv", "evidence": "evidence"}[purpose], purpose, map[string]string{"legacy": "legacy", "evidence": "attached"}[purpose]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = store.Pool.Exec(ctx, `UPDATE private_files SET created_at=now()-interval '60 days' WHERE lifecycle='attached' AND purpose='csv';UPDATE import_batches SET status='completed'`); err != nil {
		t.Fatal(err)
	}
	if err = privatefiles.Cleanup(ctx, store, root); err != nil {
		t.Fatal(err)
	}
	files, err = os.ReadDir(root)
	if err != nil || len(files) != 2 {
		t.Fatal("retention deleted protected data", files, err)
	}
	if err = store.Pool.QueryRow(ctx, `SELECT count(*) FROM import_batches WHERE file_id IS NULL`).Scan(&count); err != nil || count != 1 {
		t.Fatal("batch metadata lost", count, err)
	}
}
