package cloud

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Folder — провайдер «папка»: файлы бандлов лежат в обычном каталоге. Если это папка, которую синхронизирует Dropbox,
// OneDrive, Google Drive для компьютера, Syncthing или сетевой диск, получается облако без OAuth и API.
type Folder struct {
	mu  sync.Mutex
	dir string
	ok  bool
}

func NewFolder(dir string) *Folder { return &Folder{dir: dir} }

func (f *Folder) Name() string { return "folder" }

func (f *Folder) Connected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ok
}

// Connect проверяет, что каталог есть (создаёт, если его нет) и в него можно писать.
func (f *Folder) Connect(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dir == "" || !filepath.IsAbs(f.dir) {
		return errors.New("the folder must be an absolute path")
	}
	if err := os.MkdirAll(f.dir, 0o700); err != nil {
		return err
	}
	probe, err := os.CreateTemp(f.dir, ".oc-probe-*")
	if err != nil {
		return fmt.Errorf("cannot write to the folder: %w", err)
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	f.ok = true
	return nil
}

func (f *Folder) Disconnect() error {
	f.mu.Lock()
	f.ok = false
	f.mu.Unlock()
	return nil
}

func (f *Folder) path(id string) (string, error) {
	if !f.Connected() {
		return "", ErrNotConnected
	}
	if err := ValidName(id); err != nil {
		return "", err
	}
	return filepath.Join(f.dir, id), nil
}

func (f *Folder) List(context.Context) ([]Remote, error) {
	if !f.Connected() {
		return nil, ErrNotConnected
	}
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		return nil, err
	}
	out := []Remote{}
	for _, e := range entries {
		if !e.Type().IsRegular() || ValidName(e.Name()) != nil {
			continue
		}
		info, err := e.Info()
		if err != nil || info.Size() > MaxFileSize {
			continue
		}
		data, err := os.ReadFile(filepath.Join(f.dir, e.Name()))
		if err != nil {
			continue
		}
		out = append(out, Remote{ID: e.Name(), Name: e.Name(), Size: info.Size(), ModTime: info.ModTime(), Fingerprint: Fingerprint(data)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ModTime.After(out[j].ModTime) })
	return out, nil
}

func (f *Folder) Get(_ context.Context, id string) ([]byte, error) {
	p, err := f.path(id)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxFileSize {
		return nil, fmt.Errorf("%s is not a bundle file", id)
	}
	return os.ReadFile(p)
}

func (f *Folder) Put(_ context.Context, name string, data []byte, expect string) (Remote, error) {
	p, err := f.path(name)
	if err != nil {
		return Remote{}, err
	}
	if len(data) > MaxFileSize {
		return Remote{}, fmt.Errorf("the file is larger than %d MB", MaxFileSize>>20)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, err := os.ReadFile(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if expect != "" {
			return Remote{}, ErrConflict
		}
	case err != nil:
		return Remote{}, err
	case expect == "" || Fingerprint(cur) != expect:
		return Remote{}, ErrConflict
	}
	tmp, err := os.CreateTemp(f.dir, ".oc-put-*")
	if err != nil {
		return Remote{}, err
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmpName)
		return Remote{}, errors.Join(werr, cerr)
	}
	if err := os.Rename(tmpName, p); err != nil {
		_ = os.Remove(tmpName)
		return Remote{}, err
	}
	info, err := os.Stat(p)
	if err != nil {
		return Remote{}, err
	}
	return Remote{ID: name, Name: name, Size: info.Size(), ModTime: info.ModTime(), Fingerprint: Fingerprint(data)}, nil
}

func (f *Folder) Delete(_ context.Context, id string) error {
	p, err := f.path(id)
	if err != nil {
		return err
	}
	if err := os.Remove(p); errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	} else {
		return err
	}
}
