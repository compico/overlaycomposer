package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseDotEnv(t *testing.T) {
	got := parseDotEnv("# comment\n\nGOOGLE_CLIENT_ID=abc\nexport GOOGLE_CLIENT_SECRET = \"s e c\"\nQ='x'\nbroken\n=novalue\nEMPTY=\n")
	want := map[string]string{"GOOGLE_CLIENT_ID": "abc", "GOOGLE_CLIENT_SECRET": "s e c", "Q": "x", "EMPTY": ""}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestLoadDotEnvDoesNotOverrideEnvironment(t *testing.T) {
	file := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(file, []byte("OC_TEST_A=from-file\nOC_TEST_B=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OC_TEST_A", "from-env")
	os.Unsetenv("OC_TEST_B")
	t.Cleanup(func() { os.Unsetenv("OC_TEST_B") })
	loadDotEnv(file)
	if os.Getenv("OC_TEST_A") != "from-env" || os.Getenv("OC_TEST_B") != "from-file" {
		t.Fatalf("A=%q B=%q", os.Getenv("OC_TEST_A"), os.Getenv("OC_TEST_B"))
	}
	loadDotEnv(filepath.Join(t.TempDir(), "missing")) // нет файла — не ошибка
}
