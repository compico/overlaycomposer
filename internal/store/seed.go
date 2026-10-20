package store

import "github.com/compico/overlaycomposer/internal/model"

// Seed при первом запуске (пустая база) создаёт пустой layout «Default» и делает его layout по умолчанию:
// сцены без своего layout показывают его, а в редакторе сразу есть куда добавлять виджеты.
func (s *Store) Seed() error {
	ls, err := s.ListLayouts()
	if err != nil {
		return err
	}
	if len(ls) > 0 {
		return nil
	}
	l := model.Layout{ID: "default", Name: "Default", Scenes: []string{}, Canvas: model.Canvas{Width: 1920, Height: 1080}, Objects: []model.Object{}}
	if _, err := s.SaveLayout(l); err != nil {
		return err
	}
	return s.SetDefaultLayoutID(l.ID)
}
