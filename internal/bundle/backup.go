package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

const backupPrefix = "oc-settings-"

// Виды локальных снимков (в имени файла после времени).
const (
	KindAuto   = "auto"   // при закрытии приложения, если настройки изменились
	KindManual = "manual" // кнопка «Save snapshot now»
	KindImport = "import" // перед применением импорта
)

// nameRe: oc-settings-20261121-190000.123[-вид].json; файл без вида — бэкап перед импортом из ранних версий.
var nameRe = regexp.MustCompile(`^oc-settings-(\d{8}-\d{6}\.\d{3})(?:-(auto|manual|import))?\.json$`)

// ValidBackupName — имя файла снимка (без путей и посторонних символов).
func ValidBackupName(name string) bool { return nameRe.MatchString(name) }

// Backup — снимок настроек на диске.
type Backup struct {
	Name    string    `json:"name"`
	Kind    string    `json:"kind"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"` // время создания из имени файла (UTC)
}

// Signature — отпечаток содержимого настроек: по контрольным суммам секций, без времени создания. Одинаковые настройки — одинаковый отпечаток.
func Signature(f File) string {
	names := make([]string, 0, len(f.Checksums))
	for n := range f.Checksums {
		names = append(names, n)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		fmt.Fprintf(h, "%s=%s\n", n, f.Checksums[n])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// WriteBackup пишет снимок вида kind в dir (имя по времени) и оставляет не больше keep последних файлов (keep <= 0 — без удаления).
// Возвращает путь.
func WriteBackup(dir string, f File, now time.Time, keep int, kind string) (string, error) {
	if kind != KindAuto && kind != KindManual && kind != KindImport {
		return "", fmt.Errorf("неизвестный вид снимка %q", kind)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s%s-%s.json", backupPrefix, now.UTC().Format("20060102-150405.000"), kind)
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
		list, _ := ListBackups(dir)
		for i := len(list) - 1; i >= keep; i-- { // list — новые первыми
			_ = os.Remove(filepath.Join(dir, list[i].Name))
		}
	}
	return path, nil
}

// ListBackups — снимки в dir, новые первыми. Нет каталога — пустой список.
func ListBackups(dir string) ([]Backup, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return []Backup{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Backup{}
	for _, e := range entries {
		m := nameRe.FindStringSubmatch(e.Name())
		if e.IsDir() || m == nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		at, _ := time.ParseInLocation("20060102-150405.000", m[1], time.UTC)
		kind := m[2]
		if kind == "" {
			kind = KindImport
		}
		out = append(out, Backup{Name: e.Name(), Kind: kind, Size: info.Size(), ModTime: at})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out, nil
}

// ReadBackup — содержимое снимка как есть (для импорта: контрольные суммы считаются по исходному тексту).
func ReadBackup(dir, name string) ([]byte, error) {
	if !ValidBackupName(name) {
		return nil, fs.ErrNotExist
	}
	return os.ReadFile(filepath.Join(dir, name))
}

// DeleteBackup удаляет снимок.
func DeleteBackup(dir, name string) error {
	if !ValidBackupName(name) {
		return fs.ErrNotExist
	}
	return os.Remove(filepath.Join(dir, name))
}

// Autosave пишет автоснимок, только если настройки изменились с прошлого снимка (lastSig — отпечаток прошлого, пусто — снимков не было).
// Возвращает отпечаток текущих настроек и записан ли файл.
func Autosave(dir string, f File, lastSig string, now time.Time, keep int) (sig string, wrote bool, err error) {
	sig = Signature(f)
	if sig == lastSig {
		return sig, false, nil
	}
	if _, err := WriteBackup(dir, f, now, keep, KindAuto); err != nil {
		return sig, false, err
	}
	return sig, true, nil
}
