package localclone

import "testing"

func TestRestoreRejectsNonLocalOrExistingBusinessNames(t *testing.T) {
	for _, target := range []string{"postgres://u:p@db.example.com/hoanxu_local", "postgres://u:p@127.0.0.1/hoanxu", "postgres://u:p@127.0.0.1/hoanxu_test", "postgres://u:p@127.0.0.1/hoanxu_local_x;drop", "http://localhost/hoanxu_local"} {
		if ValidateTarget(target) == nil {
			t.Errorf("accepted unsafe target %q", target)
		}
	}
	for _, target := range []string{"postgres://u:p@127.0.0.1/hoanxu_local", "postgres://u:p@localhost/hoanxu_local_20261007", "postgres://u:p@[::1]/hoanxu_local"} {
		if err := ValidateTarget(target); err != nil {
			t.Errorf("valid target rejected: %v", err)
		}
	}
}
