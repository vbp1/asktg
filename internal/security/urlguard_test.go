package security

import "testing"

func TestValidateFetchURLRejectsUnsafeTargets(t *testing.T) {
	testCases := []string{
		"ftp://example.com/file",
		"http://localhost/admin",
		"http://127.0.0.1:8080",
		"http://10.0.0.1",
		"http://192.168.1.1",
		"http://169.254.10.20",
		"http://100.64.0.10",
	}
	for _, candidate := range testCases {
		if err := ValidateFetchURL(candidate); err == nil {
			t.Fatalf("expected blocked URL: %s", candidate)
		}
	}
}

func TestValidateFetchURLAllowsPublicIP(t *testing.T) {
	if err := ValidateFetchURL("https://1.1.1.1/dns-query"); err != nil {
		t.Fatalf("expected URL to pass baseline guard, got: %v", err)
	}
}
