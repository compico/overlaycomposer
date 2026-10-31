// Package store — SQLite через GORM (pure-Go драйвер github.com/glebarez/sqlite, без CGO).
//
// Таблицы: sources, layouts (объекты layout лежат JSON-колонкой), layout_scenes (сцена → layout; первичный
// ключ по имени сцены гарантирует «сцена принадлежит одному layout»), settings (key/value: default_layout).
package store

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"

	"github.com/compico/overlaycomposer/internal/model"
)

type sourceRow struct {
	ID    string `gorm:"primaryKey;size:64"`
	Name  string
	Type  string
	URL   string
	Proxy bool
	// Router — режим «SPA-роутер» (см. model.SourceData.Router); пусто — авто
	Router string
	// плагин-виджет (type "plugin")
	Plugin   string
	Settings map[string]any `gorm:"serializer:json"`
	Granted  []string       `gorm:"serializer:json"`
	Pos      int
}

func (sourceRow) TableName() string { return "sources" }

type layoutRow struct {
	ID        string `gorm:"primaryKey;size:64"`
	Name      string
	CanvasW   int
	CanvasH   int
	Objects   []model.Object `gorm:"serializer:json"`
	UpdatedAt time.Time
	Rev       int64 `gorm:"not null;default:0"` // растёт при каждом сохранении (оптимистичная блокировка)
}

func (layoutRow) TableName() string { return "layouts" }

type sceneRow struct {
	Scene    string `gorm:"primaryKey"`
	LayoutID string `gorm:"index;not null;size:64"`
	Pos      int
}

func (sceneRow) TableName() string { return "layout_scenes" }

type settingRow struct {
	Key   string `gorm:"primaryKey"`
	Value string
}

func (settingRow) TableName() string { return "settings" }

const keyDefaultLayout = "default_layout"

type Store struct{ db *gorm.DB }

// Open открывает (создаёт) базу и приводит схему к актуальной версии (см. migrate.go).
func Open(path string) (*Store, error) {
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, err
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1) // один писатель: без «database is locked»
	}
	if err := migrate(db, path, migrations); err != nil {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// ---- sources ----

func (r sourceRow) model() model.Source {
	return model.Source{ID: r.ID, Name: r.Name, Type: r.Type, Data: model.SourceData{URL: r.URL, Proxy: r.Proxy, Router: r.Router, Plugin: r.Plugin, Settings: r.Settings, Granted: r.Granted}}
}

func (s *Store) ListSources() ([]model.Source, error) {
	var rows []sourceRow
	if err := s.db.Order("pos, id").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]model.Source, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.model())
	}
	return out, nil
}

func (s *Store) GetSource(id string) (*model.Source, error) {
	var r sourceRow
	err := s.db.First(&r, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	m := r.model()
	return &m, nil
}

// SaveSource — создание или обновление (upsert по id).
func (s *Store) SaveSource(src model.Source) error {
	var count int64
	if err := s.db.Model(&sourceRow{}).Count(&count).Error; err != nil {
		return err
	}
	row := sourceRow{ID: src.ID, Name: src.Name, Type: src.Type, URL: src.Data.URL, Proxy: src.Data.Proxy, Router: src.Data.Router, Plugin: src.Data.Plugin, Settings: src.Data.Settings, Granted: src.Data.Granted, Pos: int(count)}
	var old sourceRow
	if err := s.db.First(&old, "id = ?", src.ID).Error; err == nil {
		row.Pos = old.Pos // порядок не меняем при обновлении
	}
	return s.db.Save(&row).Error
}

// DeleteSource удаляет источник и ссылки на него из всех layout. false — источника не было.
func (s *Store) DeleteSource(id string) (bool, error) {
	found := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		res := tx.Delete(&sourceRow{}, "id = ?", id)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}
		found = true
		var rows []layoutRow
		if err := tx.Find(&rows).Error; err != nil {
			return err
		}
		for _, r := range rows {
			kept := make([]model.Object, 0, len(r.Objects))
			for _, o := range r.Objects {
				if o.SourceID != id {
					kept = append(kept, o)
				}
			}
			if len(kept) != len(r.Objects) {
				r.Objects = kept
				r.Rev++ // серверная правка: открытые редакторы должны увидеть, что их версия устарела
				if err := tx.Save(&r).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
	return found, err
}

// ---- layouts ----

func (r layoutRow) model(scenes []string) model.Layout {
	objs := r.Objects
	if objs == nil {
		objs = []model.Object{}
	}
	if scenes == nil {
		scenes = []string{}
	}
	return model.Layout{ID: r.ID, Name: r.Name, Scenes: scenes, Canvas: model.Canvas{Width: r.CanvasW, Height: r.CanvasH}, Objects: objs, Rev: r.Rev}
}

func (s *Store) scenesByLayout(db *gorm.DB) (map[string][]string, error) {
	var rows []sceneRow
	if err := db.Order("pos, scene").Find(&rows).Error; err != nil {
		return nil, err
	}
	m := map[string][]string{}
	for _, r := range rows {
		m[r.LayoutID] = append(m[r.LayoutID], r.Scene)
	}
	return m, nil
}

func (s *Store) ListLayouts() ([]model.Layout, error) {
	var rows []layoutRow
	if err := s.db.Find(&rows).Error; err != nil {
		return nil, err
	}
	scenes, err := s.scenesByLayout(s.db)
	if err != nil {
		return nil, err
	}
	out := make([]model.Layout, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.model(scenes[r.ID]))
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// GetLayout возвращает nil, nil, если layout нет.
func (s *Store) GetLayout(id string) (*model.Layout, error) {
	if !model.IsValidID(id) {
		return nil, nil
	}
	var r layoutRow
	err := s.db.First(&r, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var sc []sceneRow
	if err := s.db.Where("layout_id = ?", id).Order("pos, scene").Find(&sc).Error; err != nil {
		return nil, err
	}
	names := make([]string, 0, len(sc))
	for _, x := range sc {
		names = append(names, x.Scene)
	}
	m := r.model(names)
	return &m, nil
}

// ErrRevConflict — layout уже изменили после того, как клиент его прочитал (не совпал Rev).
var ErrRevConflict = errors.New("layout was changed by someone else")

// SaveLayout сохраняет layout и его привязки к сценам одной транзакцией без проверки версии (серверные правки,
// создание). Сцены, которые принадлежали другим layout, переходят к этому (stolen=true). Layout должен быть уже
// провалидирован (model.Layout.Validate). Rev растёт в любом случае.
func (s *Store) SaveLayout(l model.Layout) (stolen bool, err error) {
	_, stolen, err = s.SaveLayoutRev(l, nil)
	return stolen, err
}

// SaveLayoutRev — то же, но при expect != nil сохраняет, только если Rev в базе равен *expect, иначе ErrRevConflict
// (ничего не меняется). Возвращает новый Rev.
func (s *Store) SaveLayoutRev(l model.Layout, expect *int64) (rev int64, stolen bool, err error) {
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var cur layoutRow
		switch e := tx.First(&cur, "id = ?", l.ID).Error; {
		case e == nil:
			if expect != nil && cur.Rev != *expect {
				return ErrRevConflict
			}
		case errors.Is(e, gorm.ErrRecordNotFound):
			cur = layoutRow{}
		default:
			return e
		}
		rev = cur.Rev + 1
		row := layoutRow{ID: l.ID, Name: l.Name, CanvasW: l.Canvas.Width, CanvasH: l.Canvas.Height, Objects: l.Objects, Rev: rev}
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		if len(l.Scenes) > 0 {
			var n int64
			if err := tx.Model(&sceneRow{}).Where("scene IN ? AND layout_id <> ?", l.Scenes, l.ID).Count(&n).Error; err != nil {
				return err
			}
			stolen = n > 0
		}
		if err := tx.Delete(&sceneRow{}, "layout_id = ?", l.ID).Error; err != nil {
			return err
		}
		for i, sc := range l.Scenes {
			err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "scene"}},
				DoUpdates: clause.AssignmentColumns([]string{"layout_id", "pos"}),
			}).Create(&sceneRow{Scene: sc, LayoutID: l.ID, Pos: i}).Error
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, false, err
	}
	return rev, stolen, nil
}

// DeleteLayout удаляет layout и его привязки; если он был default — сбрасывает default.
func (s *Store) DeleteLayout(id string) (bool, error) {
	found := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		res := tx.Delete(&layoutRow{}, "id = ?", id)
		if res.Error != nil {
			return res.Error
		}
		found = res.RowsAffected > 0
		if !found {
			return nil
		}
		if err := tx.Delete(&sceneRow{}, "layout_id = ?", id).Error; err != nil {
			return err
		}
		return tx.Delete(&settingRow{}, "key = ? AND value = ?", keyDefaultLayout, id).Error
	})
	return found, err
}

// RenameScene переносит привязку на новое имя сцены (сцену переименовали в OBS). true — что-то изменилось.
func (s *Store) RenameScene(from, to string) (bool, error) {
	if from == to {
		return false, nil
	}
	changed := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var old sceneRow
		if err := tx.First(&old, "scene = ?", from).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if err := tx.Delete(&sceneRow{}, "scene = ?", to).Error; err != nil { // новое имя было занято — привязка переименованной сцены главнее
			return err
		}
		if err := tx.Delete(&sceneRow{}, "scene = ?", from).Error; err != nil {
			return err
		}
		changed = true
		return tx.Create(&sceneRow{Scene: to, LayoutID: old.LayoutID, Pos: old.Pos}).Error
	})
	return changed, err
}

// ---- default layout и выбор layout для сцены ----

// DefaultLayoutID — "" если не задан или layout уже удалён.
func (s *Store) DefaultLayoutID() (string, error) {
	var st settingRow
	err := s.db.First(&st, "key = ?", keyDefaultLayout).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var n int64
	if err := s.db.Model(&layoutRow{}).Where("id = ?", st.Value).Count(&n).Error; err != nil || n == 0 {
		return "", err
	}
	return st.Value, nil
}

// SetDefaultLayoutID("") снимает default.
func (s *Store) SetDefaultLayoutID(id string) error {
	if id == "" {
		return s.db.Delete(&settingRow{}, "key = ?", keyDefaultLayout).Error
	}
	return s.db.Save(&settingRow{Key: keyDefaultLayout, Value: id}).Error
}

// ResolveLayoutForScene: layout, привязанный к сцене → иначе default → иначе nil.
func (s *Store) ResolveLayoutForScene(scene string) (*model.Layout, error) {
	var sc sceneRow
	err := s.db.First(&sc, "scene = ?", scene).Error
	if err == nil {
		return s.GetLayout(sc.LayoutID)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	def, err := s.DefaultLayoutID()
	if err != nil || def == "" {
		return nil, err
	}
	return s.GetLayout(def)
}

// SceneOwner — id layout, которому принадлежит сцена ("" если никому).
func (s *Store) SceneOwner(scene string) (string, error) {
	var sc sceneRow
	err := s.db.First(&sc, "scene = ?", scene).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	return sc.LayoutID, err
}

// FirstLayoutID — запасной «активный» layout при старте (первый по имени).
func (s *Store) FirstLayoutID() (string, error) {
	ls, err := s.ListLayouts()
	if err != nil || len(ls) == 0 {
		return "", err
	}
	return ls[0].ID, nil
}
