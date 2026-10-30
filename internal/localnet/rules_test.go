package localnet

import (
	"errors"
	"testing"
	"time"
)

func TestRuleValidate(t *testing.T) {
	ok := []Rule{
		{Target: "127.0.0.1:24050", Paths: []string{"/json/"}},
		{Setting: "tosuHost", Paths: []string{"/json/v2", "/v2/"}},
	}
	for _, r := range ok {
		if err := r.Validate(); err != nil {
			t.Errorf("%+v: %v", r, err)
		}
	}
	bad := []Rule{
		{Paths: []string{"/"}}, // ни target, ни setting
		{Target: "127.0.0.1:1", Setting: "x", Paths: []string{"/"}},     // и то и другое
		{Target: "example.com:80", Paths: []string{"/"}},                // не loopback
		{Target: "127.0.0.1:80"},                                        // нет путей
		{Target: "127.0.0.1:80", Paths: []string{"json"}},               // без «/»
		{Target: "127.0.0.1:80", Paths: []string{"//evil/"}},            // схема-относительный
		{Target: "127.0.0.1:80", Paths: []string{"/a/../b"}},            // выход вверх
		{Target: "127.0.0.1:80", Paths: []string{"/a/%2e%2e/b"}},        // то же в %-кодировке
		{Target: "127.0.0.1:80", Paths: []string{"/a b"}},               // пробел
		{Target: "127.0.0.1:80", Paths: make([]string, MaxRulePaths+1)}, // слишком много
	}
	for _, r := range bad {
		if r.Validate() == nil {
			t.Errorf("%+v must be rejected", r)
		}
	}
}

func TestAllowed(t *testing.T) {
	settings := map[string]string{"host": "localhost:24050"}
	get := func(k string) string { return settings[k] }
	rules := []Rule{
		{Setting: "host", Paths: []string{"/json/"}},
		{Target: "127.0.0.1:9000", Paths: []string{"/status"}},
	}
	allow := []struct{ target, path string }{
		{"127.0.0.1:24050", "/json/v2"},     // localhost из настройки нормализуется к 127.0.0.1
		{"localhost:24050", "/json/v2?a=1"}, // query не участвует в проверке пути
		{"127.0.0.1:9000", "/status"},
		{"127.0.0.1:9000", "/status/x"},
	}
	for _, c := range allow {
		if _, err := Allowed(rules, get, c.target, c.path); err != nil {
			t.Errorf("%v: %v", c, err)
		}
	}
	deny := []struct{ target, path string }{
		{"127.0.0.1:24051", "/json/v2"},       // другой порт
		{"127.0.0.1:24050", "/admin"},         // другой путь
		{"127.0.0.1:9000", "/json/v2"},        // путь чужого правила
		{"127.0.0.1:24050", "/json/../admin"}, // выход из префикса
		{"127.0.0.1:24050", "/json/%2e%2e/admin"},
		{"127.0.0.1:24050", "/json/..%2fadmin"}, // %2f не разворачиваем в сегмент, но «..» внутри сегмента не должен проходить как префикс
		{"example.com:24050", "/json/v2"},
		{"127.0.0.1:24050", "//evil/json/"},
	}
	for _, c := range deny {
		if _, err := Allowed(rules, get, c.target, c.path); err == nil {
			t.Errorf("%v must be denied", c)
		}
	}
	// sad path: правил нет вообще
	if _, err := Allowed(nil, get, "127.0.0.1:24050", "/json/"); !errors.Is(err, ErrNoRules) {
		t.Errorf("no rules: %v", err)
	}
	// sad path: пользователь стёр настройку — адрес из неё не угадать
	settings["host"] = ""
	if _, err := Allowed(rules[:1], get, "127.0.0.1:24050", "/json/"); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("empty setting: %v", err)
	}
}

func TestPortOf(t *testing.T) {
	if PortOf("127.0.0.1:8000") != "8000" || PortOf("[::1]:80") != "80" || PortOf("x") != "" {
		t.Error("PortOf")
	}
}

func TestLimiter(t *testing.T) {
	now := time.Now()
	l := NewLimiter(10*time.Second, 3, 2, 3)
	l.now = func() time.Time { return now }

	d1, err := l.Acquire("a")
	if err != nil {
		t.Fatal(err)
	}
	d2, _ := l.Acquire("a")
	if _, err := l.Acquire("a"); !errors.Is(err, ErrRate) {
		t.Fatalf("третий одновременный запрос ключа должен быть отклонён: %v", err)
	}
	d1()
	d1() // повторный вызов не освобождает чужое место
	if _, err := l.Acquire("a"); err != nil {
		t.Fatalf("после освобождения можно: %v", err)
	}
	// частота: 3 запроса в окне уже были
	d2()
	if _, err := l.Acquire("a"); !errors.Is(err, ErrRate) {
		t.Fatalf("лимит окна: %v", err)
	}
	now = now.Add(11 * time.Second)
	if _, err := l.Acquire("a"); err != nil {
		t.Fatalf("новое окно: %v", err)
	}
	// общий предел одновременных запросов (3): у «a» уже заняты 2 (3-й и 4-й выше не освобождались)
	if _, err := l.Acquire("b"); err != nil {
		t.Fatalf("другой ключ: %v", err)
	}
	if _, err := l.Acquire("c"); !errors.Is(err, ErrRate) {
		t.Fatalf("общий предел: %v", err)
	}
}
