// Package config — config.json: настройки, которые нужны до открытия базы (адрес сервера, OBS).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"regexp"
	"strings"
	"sync"
)

type Obs struct {
	// Enabled — подключаться к OBS по obs-websocket 5.x (список сцен и текущая сцена).
	Enabled  bool   `json:"enabled"`
	URL      string `json:"url"`
	Password string `json:"password"`
}

type Config struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	DebugProxy bool   `json:"debugProxy"`
	Obs        Obs    `json:"obs"`
	// NoUpdateCheck — не проверять новые версии на GitHub (только вручную в config.json).
	NoUpdateCheck bool `json:"noUpdateCheck"`
}

func Default() Config {
	return Config{Host: "127.0.0.1", Port: 8000, Obs: Obs{Enabled: true, URL: "ws://127.0.0.1:4455"}}
}

// Store — конфиг с блокировкой и записью на диск.
type Store struct {
	mu   sync.RWMutex
	path string
	cfg  Config
}

// Load читает файл; недостающее/битое → значения по умолчанию. Если файла нет — создаёт.
func Load(path string) (*Store, error) {
	cfg := Default()
	raw, err := os.ReadFile(path)
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		slog.Warn("config не прочитан, использую значения по умолчанию", "path", path, "err", err)
	}
	if err == nil {
		// Накладываем файл поверх значений по умолчанию (отсутствующие поля остаются дефолтными).
		if jerr := json.Unmarshal(raw, &cfg); jerr != nil {
			slog.Warn("config битый, использую значения по умолчанию", "path", path, "err", jerr)
			cfg = Default()
		}
	}
	cfg = sanitize(cfg)
	s := &Store{path: path, cfg: cfg}
	if missing {
		if err := s.save(); err != nil {
			return s, err
		}
	}
	return s, nil
}

func sanitize(c Config) Config {
	d := Default()
	if strings.TrimSpace(c.Host) == "" {
		c.Host = d.Host
	}
	if c.Port < 1 || c.Port > 65535 {
		c.Port = d.Port
	}
	if strings.TrimSpace(c.Obs.URL) == "" {
		c.Obs.URL = d.Obs.URL
	}
	return c
}

func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

func (s *Store) save() error {
	b, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil { // в файле может быть пароль OBS
		return err
	}
	return os.Rename(tmp, s.path)
}

// Public — вид для UI: без пароля.
type Public struct {
	Host           string `json:"host"`
	Port           int    `json:"port"`
	DebugProxy     bool   `json:"debugProxy"`
	ObsEnabled     bool   `json:"obsEnabled"`
	ObsURL         string `json:"obsUrl"`
	HasObsPassword bool   `json:"hasObsPassword"`
}

func (c Config) Public() Public {
	return Public{c.Host, c.Port, c.DebugProxy, c.Obs.Enabled, c.Obs.URL, c.Obs.Password != ""}
}

// Patch — частичное обновление из UI. nil = «не менять»; пустой пароль = «не менять».
type Patch struct {
	Host       *string   `json:"host"`
	Port       *float64  `json:"port"`
	DebugProxy *bool     `json:"debugProxy"`
	Obs        *ObsPatch `json:"obs"`
}

type ObsPatch struct {
	Enabled       *bool   `json:"enabled"`
	URL           *string `json:"url"`
	Password      *string `json:"password"`
	ClearPassword *bool   `json:"clearPassword"`
}

var wsURL = regexp.MustCompile(`^wss?://\S+$`)

// Apply проверяет и применяет патч, сохраняет файл. Возвращает старый и новый конфиг.
func (s *Store) Apply(p Patch) (prev, next Config, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev = s.cfg
	n := s.cfg
	if p.Host != nil {
		h := strings.TrimSpace(*p.Host)
		if h == "" {
			return prev, prev, errors.New("invalid host")
		}
		n.Host = h
	}
	if p.Port != nil {
		f := *p.Port
		if f != math.Trunc(f) || f < 1 || f > 65535 {
			return prev, prev, errors.New("invalid port (1–65535)")
		}
		n.Port = int(f)
	}
	if p.DebugProxy != nil {
		n.DebugProxy = *p.DebugProxy
	}
	if p.Obs != nil {
		if p.Obs.Enabled != nil {
			n.Obs.Enabled = *p.Obs.Enabled
		}
		if p.Obs.URL != nil {
			u := strings.TrimSpace(*p.Obs.URL)
			if !wsURL.MatchString(u) {
				return prev, prev, errors.New("OBS URL must start with ws:// or wss://")
			}
			n.Obs.URL = u
		}
		if p.Obs.ClearPassword != nil && *p.Obs.ClearPassword {
			n.Obs.Password = ""
		}
		if p.Obs.Password != nil && *p.Obs.Password != "" {
			n.Obs.Password = *p.Obs.Password
		}
	}
	s.cfg = n
	if err := s.save(); err != nil {
		s.cfg = prev
		return prev, prev, fmt.Errorf("save config: %w", err)
	}
	return prev, n, nil
}
