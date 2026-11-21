package content

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/compico/overlaycomposer/internal/bundle"
	"github.com/compico/overlaycomposer/internal/market"
)

// ---- store: источники магазина ----

// SourceList — список источников магазина (репозитории с плагинами).
type SourceList interface {
	Sources() []string
	// AddSource проверяет, что репозиторий скачивается и в нём есть плагины, и добавляет его.
	AddSource(ctx context.Context, repo string) (string, error)
}

type storeData struct {
	Sources []string `json:"sources"`
}

// Store — секция «store»: какие репозитории добавлены в магазин. Токен GitHub не экспортируется. Добавить источник — значит довериться
// коду из этого репозитория, поэтому все элементы не отмечены по умолчанию (Optional) и источники добавляются только по явному выбору.
type Store struct{ List SourceList }

func (*Store) Name() string { return "store" }

func (s *Store) Export(context.Context) (json.RawMessage, error) {
	src := s.List.Sources()
	if src == nil {
		src = []string{}
	}
	return json.Marshal(storeData{Sources: src})
}

func (s *Store) parse(raw json.RawMessage) ([]string, error) {
	var in storeData
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, errors.New("the store section is unreadable")
	}
	if len(in.Sources) > 100 {
		return nil, errors.New("too many sources")
	}
	return in.Sources, nil
}

func (s *Store) Plan(_ context.Context, raw json.RawMessage) ([]bundle.Item, error) {
	in, err := s.parse(raw)
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, h := range s.List.Sources() {
		have[strings.ToLower(h)] = true
	}
	items := make([]bundle.Item, 0, len(in))
	seen := map[string]bool{}
	for _, repo := range in {
		it := bundle.Item{ID: repo, Name: repo, Optional: true}
		r, err := market.ParseRepo(repo)
		switch {
		case err != nil:
			it.Status, it.Reason = bundle.StatusProblem, "Invalid repository: "+err.Error()
		case seen[strings.ToLower(r.String())]:
			it.Status, it.Reason = bundle.StatusProblem, "The file lists this repository twice."
		case have[strings.ToLower(r.String())]:
			it.Status = bundle.StatusSame
		default:
			it.Status, it.Warning = bundle.StatusNew, true
			it.Reason = "Adding a source means trusting the plugins published in this repository."
		}
		if err == nil {
			it.ID = r.String()
			seen[strings.ToLower(r.String())] = true
		}
		items = append(items, it)
	}
	return items, nil
}

func (s *Store) Apply(ctx context.Context, raw json.RawMessage, pick map[string]string) ([]bundle.Outcome, error) {
	in, err := s.parse(raw)
	if err != nil {
		return nil, err
	}
	var out []bundle.Outcome
	done := map[string]bool{}
	for _, repo := range in {
		r, perr := market.ParseRepo(repo)
		if perr != nil {
			continue
		}
		id := r.String()
		mode, ok := pick[id]
		if !ok || done[id] {
			continue
		}
		done[id] = true
		o := bundle.Outcome{ID: id}
		if mode != bundle.ModeReplace {
			o.Error = "sources are added, not copied"
		} else if _, err := s.List.AddSource(ctx, id); err != nil {
			o.Error = err.Error()
		} else {
			o.OK = true
		}
		out = append(out, o)
	}
	return out, nil
}

// ---- app: подключение к OBS ----

// ObsSettings — несекретные настройки подключения к OBS.
type ObsSettings struct {
	Enabled bool   `json:"enabled"`
	URL     string `json:"url"`
}

// ObsConfig — чтение и применение настроек OBS (пароль не затрагивается).
type ObsConfig interface {
	Get() ObsSettings
	Apply(ObsSettings) error
}

type appData struct {
	Obs *ObsSettings `json:"obs,omitempty"`
}

// ObsItemID — единственный элемент секции «app».
const ObsItemID = "obs"

var wsURLRe = regexp.MustCompile(`^wss?://\S+$`)

// App — секция «app»: несекретные настройки самого приложения, которые имеет смысл переносить (подключение к OBS без пароля).
// Адрес и порты сервера, порт прокси, пароли и ключи сюда не входят: они привязаны к компьютеру или секретны.
// Элемент не отмечен по умолчанию: адрес OBS зависит от машины.
type App struct{ Obs ObsConfig }

func (*App) Name() string { return "app" }

func (a *App) Export(context.Context) (json.RawMessage, error) {
	o := a.Obs.Get()
	return json.Marshal(appData{Obs: &o})
}

func (a *App) parse(raw json.RawMessage) (*ObsSettings, error) {
	var in appData
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, errors.New("the app section is unreadable")
	}
	return in.Obs, nil
}

func (a *App) Plan(_ context.Context, raw json.RawMessage) ([]bundle.Item, error) {
	o, err := a.parse(raw)
	if err != nil {
		return nil, err
	}
	if o == nil {
		return []bundle.Item{}, nil
	}
	it := bundle.Item{ID: ObsItemID, Name: "OBS connection (" + o.URL + ")", Optional: true}
	switch cur := a.Obs.Get(); {
	case !wsURLRe.MatchString(strings.TrimSpace(o.URL)):
		it.Status, it.Reason = bundle.StatusProblem, "OBS URL must start with ws:// or wss://."
	case cur == *o:
		it.Status = bundle.StatusSame
	default:
		it.Status, it.Warning = bundle.StatusReplace, true
		it.Reason = "Changes the OBS address and reconnects. The OBS password is not part of the file: enter it in Settings if needed."
	}
	return []bundle.Item{it}, nil
}

func (a *App) Apply(_ context.Context, raw json.RawMessage, pick map[string]string) ([]bundle.Outcome, error) {
	if _, ok := pick[ObsItemID]; !ok {
		return nil, nil
	}
	o, err := a.parse(raw)
	out := bundle.Outcome{ID: ObsItemID}
	switch {
	case pick[ObsItemID] != bundle.ModeReplace:
		out.Error = "settings are imported by replacing only"
	case err != nil:
		out.Error = err.Error()
	case o == nil:
		out.Error = "the file has no OBS settings"
	case !wsURLRe.MatchString(strings.TrimSpace(o.URL)):
		out.Error = "OBS URL must start with ws:// or wss://"
	default:
		o.URL = strings.TrimSpace(o.URL)
		if err := a.Obs.Apply(*o); err != nil {
			out.Error = err.Error()
		} else {
			out.OK = true
		}
	}
	return []bundle.Outcome{out}, nil
}
