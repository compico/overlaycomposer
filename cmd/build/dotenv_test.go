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

func TestGoogleCredsTakesOnlyNeededKeysAndKeepsEnvironmentClean(t *testing.T) {
	file := filepath.Join(t.TempDir(), ".env")
	body := "GOOGLE_CLIENT_ID=from-file\nGOOGLE_CLIENT_SECRET=file-secret\nOC_TEST_EXTRA_KEY=must-not-be-read\n"
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_CLIENT_ID", "from-env") // окружение важнее файла
	os.Unsetenv("GOOGLE_CLIENT_SECRET")
	t.Cleanup(func() { os.Unsetenv("GOOGLE_CLIENT_SECRET"); os.Unsetenv("OC_TEST_EXTRA_KEY") })

	got := googleCreds(file)
	if len(got) != 2 || got["GOOGLE_CLIENT_ID"] != "from-env" || got["GOOGLE_CLIENT_SECRET"] != "file-secret" {
		t.Fatalf("%v", got)
	}
	// ничего из файла не попало в окружение процесса
	if _, ok := os.LookupEnv("GOOGLE_CLIENT_SECRET"); ok {
		t.Fatal("секрет из .env попал в окружение")
	}
	if _, ok := os.LookupEnv("OC_TEST_EXTRA_KEY"); ok {
		t.Fatal("лишняя переменная из .env попала в окружение")
	}
	if len(googleCreds(filepath.Join(t.TempDir(), "missing"))) != 1 { // нет файла: только окружение
		t.Fatal("без .env ожидался один ключ из окружения")
	}
}

func TestRedactHidesSecrets(t *testing.T) {
	hidden = []string{"id-123", "sec-456", ""}
	t.Cleanup(func() { hidden = nil })
	got := redact("-ldflags -X x.GoogleClientID=id-123 -X x.GoogleClientSecret=sec-456 -o out")
	if got != "-ldflags -X x.GoogleClientID=*** -X x.GoogleClientSecret=*** -o out" {
		t.Fatal(got)
	}
}
