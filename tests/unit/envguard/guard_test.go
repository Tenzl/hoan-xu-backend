package envguard

import "testing"

func TestDevelopmentAndSeedCannotReachBusinessDatabase(t *testing.T) {
	if Validate("postgres://u:p@db.example.com/hoanxu", "development") == nil {
		t.Fatal("development accepted remote database")
	}
	if Validate("postgres://u:p@127.0.0.1/hoanxu_local", "development") != nil {
		t.Fatal("local development rejected")
	}
	for _, target := range []struct{ url, mode string }{{"postgres://u:p@127.0.0.1/hoanxu_local", "development"}, {"postgres://u:p@db.example.com/hoanxu_test", "test"}, {"postgres://u:p@127.0.0.1/hoanxu", "test"}} {
		if SeedAllowed(target.url, target.mode) == nil {
			t.Fatal("generated seed accepted business database")
		}
	}
	if SeedAllowed("postgres://u:p@127.0.0.1/hoanxu_test", "test") != nil {
		t.Fatal("test seed rejected")
	}
}
