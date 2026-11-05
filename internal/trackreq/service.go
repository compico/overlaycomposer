// Package trackreq — заявки зрителей на треки: награда за баллы канала Twitch → ссылка на Яндекс Музыку → очередь плеера (PulseSync).
//
// Границы ответственности:
//   - Twitch (авторизация, EventSub, награды) — internal/twitch; здесь только потребление погашений;
//   - разбор ссылки — ParseYandexTrack (строго, ввод зрителя недоверенный);
//   - очередь заявок хранится в базе (переживает перезапуск), дедупликация по ID погашения;
//   - плеер — за интерфейсом Player: он вставляет трек и сообщает, что трек начал играть (подтверждение старта,
//     а не «команда отправлена»). Заявки отдаются плееру по одной (FIFO), следующая — после старта предыдущей.
package trackreq

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/compico/overlaycomposer/internal/twitch"
)

// Статусы заявки.
const (
	StatusQueued   = "queued"   // принята, ждёт своей очереди
	StatusSent     = "sent"     // отдана плееру, старт не подтверждён
	StatusPlayed   = "played"   // плеер подтвердил, что трек начал играть
	StatusFailed   = "failed"   // плеер не смог добавить трек
	StatusRejected = "rejected" // ссылка не подошла: заявка не принята, баллы возвращены
	StatusCanceled = "canceled" // снята вручную
)

// Request — заявка зрителя.
type Request struct {
	ID           int64     `json:"id"`
	RedemptionID string    `json:"redemptionId"`
	RewardID     string    `json:"rewardId"`
	UserLogin    string    `json:"userLogin"`
	UserName     string    `json:"userName"`
	Input        string    `json:"input"` // что ввёл зритель (недоверенно, показывать только как текст)
	TrackID      string    `json:"trackId,omitempty"`
	AlbumID      string    `json:"albumId,omitempty"`
	Status       string    `json:"status"`
	Error        string    `json:"error,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// Config — настройки функции.
type Config struct {
	Enabled  bool   `json:"enabled"`
	RewardID string `json:"rewardId"` // награда, погашения которой считаются заявками
}

// Store — хранилище заявок и настроек.
type Store interface {
	// Add сохраняет заявку; created=false — заявка с таким RedemptionID уже есть (повторная доставка).
	Add(Request) (stored Request, created bool, err error)
	Get(id int64) (*Request, error)
	List(limit int) ([]Request, error) // новые первыми
	NextQueued() (*Request, error)     // самая ранняя queued
	Active() (*Request, error)         // заявка в статусе sent, если есть
	SetStatus(id int64, status, errText string) error
	LoadConfig() (Config, error)
	SaveConfig(Config) error
}

// Twitch — то, что нужно от слоя Twitch.
type Twitch interface {
	UpdateRedemption(ctx context.Context, rewardID, redemptionID, status string) error
}

// Command — что нужно сделать плееру.
type Command struct {
	ID      int64  `json:"id"`
	TrackID string `json:"trackId"`
	AlbumID string `json:"albumId"`
	// Resend — команда повторная (после перезапуска OC или переподключения плеера): плеер должен сначала проверить,
	// нет ли трека уже в очереди, чтобы не вставить дубликат.
	Resend bool `json:"resend,omitempty"`
}

// Player — мост к плееру. Send возвращает ошибку, если плеер не подключён.
type Player interface {
	Connected() bool
	Send(Command) error
}

// EventSource — источник погашений (twitch.EventSub). Run блокируется до отмены контекста.
type EventSource interface{ Run(ctx context.Context) }

// NewEventSource создаёт источник погашений с обработчиками.
type NewEventSource func(onRedeem func(twitch.Redemption), onState func(state, detail string)) EventSource

// Status — снимок состояния для интерфейса.
type Status struct {
	Config          Config `json:"config"`
	Twitch          string `json:"twitch"`
	TwitchDetail    string `json:"twitchDetail,omitempty"`
	PlayerConnected bool   `json:"playerConnected"`
}

const (
	redeemTimeout  = 15 * time.Second
	maxStoredInput = 500 // символов ввода зрителя, которые храним (Twitch и так ограничивает ввод)
	maxQueued      = 100 // сколько заявок ждёт очереди; дальше — отказ с возвратом баллов
)

type Service struct {
	st     Store
	tw     Twitch
	player Player
	newES  NewEventSource

	mu      sync.Mutex // порядок: заявки обрабатываются и отдаются плееру по одной
	cfg     Config
	root    context.Context // время жизни сервиса (из Start); от него живёт EventSub
	cancel  context.CancelFunc
	esState string
	esInfo  string
	wg      sync.WaitGroup
}

func New(st Store, tw Twitch, player Player, newES NewEventSource) (*Service, error) {
	cfg, err := st.LoadConfig()
	if err != nil {
		return nil, err
	}
	return &Service{st: st, tw: tw, player: player, newES: newES, cfg: cfg, esState: twitch.ESDisconnected}, nil
}

// Start запускает приём погашений, если функция включена. ctx — время жизни сервиса.
func (s *Service) Start(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.root = ctx
	s.apply()
}

// Stop останавливает приём погашений и ждёт завершения.
func (s *Service) Stop() {
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.mu.Unlock()
	s.wg.Wait()
}

// apply приводит EventSub в соответствие с настройками. Вызывать под s.mu.
func (s *Service) apply() {
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	if !s.cfg.Enabled || s.newES == nil {
		s.esState, s.esInfo = twitch.ESDisconnected, ""
		return
	}
	parent := s.root
	if parent == nil {
		parent = context.Background()
	}
	runCtx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	es := s.newES(s.Handle, func(state, detail string) {
		s.mu.Lock()
		s.esState, s.esInfo = state, detail
		s.mu.Unlock()
	})
	s.wg.Add(1)
	go func() { defer s.wg.Done(); es.Run(runCtx) }()
}

// SetConfig сохраняет настройки и перезапускает приём погашений.
func (s *Service) SetConfig(c Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.st.SaveConfig(c); err != nil {
		return err
	}
	s.cfg = c
	s.apply()
	return nil
}

func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{Config: s.cfg, Twitch: s.esState, TwitchDetail: s.esInfo, PlayerConnected: s.player != nil && s.player.Connected()}
}

// Requests — последние заявки, новые первыми.
func (s *Service) Requests(limit int) ([]Request, error) { return s.st.List(limit) }

// Handle — обработчик погашения (вызывается из EventSub). Чужие награды и выключенную функцию игнорирует.
func (s *Service) Handle(r twitch.Redemption) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.cfg.Enabled || s.cfg.RewardID == "" || r.RewardID != s.cfg.RewardID {
		return
	}
	input := r.Input
	if rs := []rune(input); len(rs) > maxStoredInput {
		input = string(rs[:maxStoredInput])
	}
	req := Request{RedemptionID: r.ID, RewardID: r.RewardID, UserLogin: r.UserLogin, UserName: r.UserName, Input: input}
	tr, ok := ParseYandexTrack(r.Input)
	switch {
	case !ok:
		req.Status, req.Error = StatusRejected, "not a Yandex Music track link"
	default:
		req.TrackID, req.AlbumID, req.Status = tr.TrackID, tr.AlbumID, StatusQueued
		if n, err := s.queuedCount(); err == nil && n >= maxQueued {
			req.TrackID, req.AlbumID = "", ""
			req.Status, req.Error = StatusRejected, "the queue is full"
		}
	}
	stored, created, err := s.st.Add(req)
	if err != nil {
		slog.Error("trackreq: сохранение заявки", "err", err, "redemption", r.ID)
		return
	}
	if !created {
		return // повторная доставка того же погашения
	}
	if stored.Status == StatusRejected {
		s.finishRedemption(stored, twitch.RedemptionCanceled)
		return
	}
	s.dispatch()
}

func (s *Service) queuedCount() (int, error) {
	list, err := s.st.List(maxQueued * 2)
	n := 0
	for _, r := range list {
		if r.Status == StatusQueued {
			n++
		}
	}
	return n, err
}

// finishRedemption сообщает Twitch итог (выполнено / отменено с возвратом баллов). Не блокирует обработку; ошибка не критична:
// у награды, созданной не этим приложением, статус менять нельзя — тогда погашение остаётся в очереди наград на Twitch.
func (s *Service) finishRedemption(r Request, status string) {
	if s.tw == nil {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), redeemTimeout)
		defer cancel()
		if err := s.tw.UpdateRedemption(ctx, r.RewardID, r.RedemptionID, status); err != nil {
			slog.Warn("trackreq: статус погашения не обновлён", "redemption", r.RedemptionID, "status", status, "err", err)
		}
	}()
}

// dispatch отдаёт плееру следующую заявку, если нет активной и плеер подключён. Вызывать под s.mu.
func (s *Service) dispatch() {
	if s.player == nil || !s.player.Connected() {
		return
	}
	active, err := s.st.Active()
	if err != nil {
		slog.Error("trackreq: активная заявка", "err", err)
		return
	}
	if active != nil {
		return
	}
	next, err := s.st.NextQueued()
	if err != nil || next == nil {
		if err != nil {
			slog.Error("trackreq: следующая заявка", "err", err)
		}
		return
	}
	s.send(*next, false)
}

// send отдаёт заявку плееру и отмечает её как sent.
func (s *Service) send(r Request, resend bool) {
	if err := s.player.Send(Command{ID: r.ID, TrackID: r.TrackID, AlbumID: r.AlbumID, Resend: resend}); err != nil {
		slog.Warn("trackreq: плеер не принял команду", "id", r.ID, "err", err)
		return // останется queued/sent: уйдёт при следующем подключении плеера
	}
	if r.Status != StatusSent {
		if err := s.st.SetStatus(r.ID, StatusSent, ""); err != nil {
			slog.Error("trackreq: статус заявки", "id", r.ID, "err", err)
		}
	}
}

// PlayerConnected — плеер (пере)подключился: если заявка уже была отдана, но старт не подтверждён, шлём её ещё раз
// с пометкой Resend (плеер сверит с очередью), иначе отдаём следующую.
func (s *Service) PlayerConnected() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if active, err := s.st.Active(); err == nil && active != nil {
		s.send(*active, true)
		return
	}
	s.dispatch()
}

var ErrUnknownRequest = errors.New("unknown request")

// PlayerStarted — плеер подтвердил, что трек заявки начал играть: заявка выполнена, следующая уходит плееру.
func (s *Service) PlayerStarted(id int64) error {
	return s.finish(id, StatusPlayed, "", twitch.RedemptionFulfilled)
}

// PlayerFailed — плеер не смог добавить трек: заявка снимается, баллы возвращаются зрителю.
func (s *Service) PlayerFailed(id int64, reason string) error {
	return s.finish(id, StatusFailed, reason, twitch.RedemptionCanceled)
}

// Cancel снимает заявку вручную (ещё не сыгравшую): баллы возвращаются зрителю.
func (s *Service) Cancel(id int64) error {
	return s.finish(id, StatusCanceled, "", twitch.RedemptionCanceled)
}

func (s *Service) finish(id int64, status, reason, twitchStatus string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.st.Get(id)
	if err != nil {
		return err
	}
	if r == nil {
		return ErrUnknownRequest
	}
	if r.Status != StatusQueued && r.Status != StatusSent {
		return fmt.Errorf("request %d is already %s", id, r.Status)
	}
	// Подтверждение старта принимаем только для отданной плееру заявки: иначе плеер (или ошибка в нём) мог бы «сыграть» чужую.
	if status == StatusPlayed && r.Status != StatusSent {
		return fmt.Errorf("request %d was not sent to the player", id)
	}
	if err := s.st.SetStatus(id, status, reason); err != nil {
		return err
	}
	s.finishRedemption(*r, twitchStatus)
	s.dispatch()
	return nil
}

// NoPlayer — заглушка, пока мост к плееру не подключён: заявки копятся в очереди и уйдут, когда плеер появится.
type NoPlayer struct{}

func (NoPlayer) Connected() bool { return false }
func (NoPlayer) Send(Command) error {
	return errors.New("player is not connected")
}
