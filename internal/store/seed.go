package store

import "github.com/compico/overlaycomposer/internal/model"

// Seed создаёт демо-источники и стартовый layout при первом запуске (пустая база).
func (s *Store) Seed() error {
	srcs, err := s.ListSources()
	if err != nil {
		return err
	}
	if len(srcs) == 0 {
		for _, src := range []model.Source{
			{ID: "clock", Name: "Clock (demo)", Type: "iframe", Data: model.SourceData{URL: "/widgets/clock"}},
			{ID: "box", Name: "Box (demo)", Type: "iframe", Data: model.SourceData{URL: "/widgets/box"}},
		} {
			if err := s.SaveSource(src); err != nil {
				return err
			}
		}
	}
	ls, err := s.ListLayouts()
	if err != nil {
		return err
	}
	if len(ls) > 0 {
		return nil
	}
	t := func(x, y, w, h, rz float64) model.Transform {
		return model.Transform{X: x, Y: y, Width: w, Height: h, RotationZ: rz, ScaleX: 1, ScaleY: 1, ScaleZ: 1}
	}
	l := model.Layout{
		ID: "gaming", Name: "Gaming", Scenes: []string{"Gaming"}, Canvas: model.Canvas{Width: 1920, Height: 1080},
		Objects: []model.Object{
			{ID: "obj_clock", SourceID: "clock", Transform: t(1400, 60, 440, 160, 0), Opacity: 1, Visible: true, ZIndex: 10},
			{ID: "obj_box", SourceID: "box", Transform: t(80, 700, 480, 300, -4), Opacity: 1, Visible: true, ZIndex: 5},
		},
	}
	if _, err := s.SaveLayout(l); err != nil {
		return err
	}
	return s.SetDefaultLayoutID("gaming") // первый запуск: единственный layout — он же по умолчанию
}
