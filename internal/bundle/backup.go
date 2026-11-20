package bundle

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const backupPrefix = "oc-settings-"

// WriteBackup пишет файл в dir (имя по времени) и оставляет не больше keep последних бэкапов. Возвращает путь.
func WriteBackup(dir string, f File, now time.Time, keep int) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s%s.json", backupPrefix, now.UTC().Format("20060102-150405.000"))
	path := filepath.Join(dir, name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if keep > 0 {
		entries, _ := os.ReadDir(dir)
		var names []string
		for _, e := range entries {
			if !e.IsDir() && strings.HasPrefix(e.Name(), backupPrefix) && strings.HasSuffix(e.Name(), ".json") {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names) // имя содержит время: старые — раньше
		for len(names) > keep {
			_ = os.Remove(filepath.Join(dir, names[0]))
			names = names[1:]
		}
	}
	return path, nil
}
