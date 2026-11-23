package obs

import (
	"errors"
	"math"
	"sort"
	"sync"
)

// Микшер (PoC, #234): уровни, mute и громкость активных источников звука. OBS присылает событие InputVolumeMeters каждые 50 мс
// только пока на него подписались, поэтому подписка включается, пока есть хотя бы один наблюдатель (WatchMixer), и выключается после.
const (
	subInputs            = 1 << 3  // InputMuteStateChanged, InputVolumeChanged, …
	subInputVolumeMeters = 1 << 16 // InputVolumeMeters — высокочастотное, по умолчанию не входит
)

// MixerInput — активный источник звука. Peak — пик по каналам в дБFS (−100 — тишина).
type MixerInput struct {
	Name     string  `json:"name"`
	Peak     float64 `json:"peak"`
	Muted    bool    `json:"muted"`
	VolumeDB float64 `json:"volumeDb"`
	// Monitor — прослушивание в наушниках: none — выключено, both — слышно и в наушниках, и в эфире, only — только в наушниках.
	Monitor string `json:"monitor"`
}

type inputState struct {
	muted   bool
	volDB   float64
	monitor string // none | both | only
	settled bool   // начальные mute, громкость и прослушивание уже запрошены у OBS
}

// Типы прослушивания OBS и их короткие имена для страницы.
var monitorTypes = map[string]string{
	"OBS_MONITORING_TYPE_NONE":               "none",
	"OBS_MONITORING_TYPE_MONITOR_ONLY":       "only",
	"OBS_MONITORING_TYPE_MONITOR_AND_OUTPUT": "both",
}

func monitorFromOBS(t string) string {
	if m, ok := monitorTypes[t]; ok {
		return m
	}
	return "none"
}

func monitorToOBS(m string) string {
	for k, v := range monitorTypes {
		if v == m {
			return k
		}
	}
	return "OBS_MONITORING_TYPE_NONE"
}

// nextMonitor — что делает кнопка прослушивания: выключено → слышно в наушниках, эфир не меняется; любое включённое
// состояние (в том числе «только наушники», выставленное в самом OBS) → выключено. «Только наушники» убирает источник из эфира,
// поэтому случайным нажатием на кнопку до него не дойти.
func nextMonitor(m string) string {
	if m == "none" {
		return "both"
	}
	return "none"
}

type mixer struct {
	subMu    sync.Mutex // включение/выключение подписки — по одному
	mu       sync.Mutex
	next     int
	watchers map[int]func([]MixerInput)
	state    map[string]*inputState
	active   []MixerInput
}

// ErrInputNotActive — источника нет среди активных (его не показывает микшер).
var ErrInputNotActive = errors.New("audio input is not active")

// Пределы громкости, которые разрешает микшер, в дБ. Выше 0 дБ не поднимаем: усиление легко приводит к клиппингу.
const (
	minVolumeDB = -100.0
	maxVolumeDB = 0.0
)

func clampVolumeDB(db float64) float64 { return math.Min(math.Max(db, minVolumeDB), maxVolumeDB) }

func dbFromMul(x float64) float64 {
	if !(x > 1e-5) { // и NaN
		return -100
	}
	return math.Max(20*math.Log10(x), -100)
}

// WatchMixer вызывает fn с актуальным списком активных источников при каждом событии уровней (около 20 раз в секунду).
// fn не должна блокироваться. stop снимает наблюдателя; когда их не остаётся, подписка на уровни в OBS выключается.
func (c *Client) WatchMixer(fn func([]MixerInput)) (stop func()) {
	m := &c.mix
	m.subMu.Lock()
	defer m.subMu.Unlock()
	m.mu.Lock()
	if m.watchers == nil {
		m.watchers = map[int]func([]MixerInput){}
	}
	id := m.next
	m.next++
	first := len(m.watchers) == 0
	m.watchers[id] = fn
	if first {
		m.state = map[string]*inputState{}
	}
	m.mu.Unlock()
	if first {
		c.setMixerEvents(true)
	}
	return func() {
		m.subMu.Lock()
		defer m.subMu.Unlock()
		m.mu.Lock()
		delete(m.watchers, id)
		last := len(m.watchers) == 0
		if last {
			m.state, m.active = nil, nil
		}
		m.mu.Unlock()
		if last {
			c.setMixerEvents(false)
		}
	}
}

// setMixerEvents переключает подписку на уровни у уже идентифицированного соединения (Reidentify, op 3).
func (c *Client) setMixerEvents(on bool) {
	if !c.Connected() {
		return // подписка включится после Identified (resubscribeMixer)
	}
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return
	}
	sub := eventSubscriptions
	if on {
		sub |= subInputs | subInputVolumeMeters
	}
	_ = c.write(conn, map[string]any{"op": 3, "d": map[string]any{"eventSubscriptions": sub}})
}

// resubscribeMixer возвращает подписку после переподключения к OBS.
func (c *Client) resubscribeMixer() {
	m := &c.mix
	m.subMu.Lock()
	defer m.subMu.Unlock()
	m.mu.Lock()
	watched := len(m.watchers) > 0
	m.mu.Unlock()
	if watched {
		c.setMixerEvents(true)
	}
}

// mixerEvent разбирает события микшера. true — событие целиком обработано здесь (уровни); mute и громкость
// микшер только запоминает, а дальше событие идёт обычному обработчику (там тоже следят за mute).
func (c *Client) mixerEvent(d map[string]any) bool {
	typ, _ := d["eventType"].(string)
	data, _ := d["eventData"].(map[string]any)
	switch typ {
	case "InputVolumeMeters":
		c.meters(data)
		return true
	case "InputMuteStateChanged":
		name, _ := data["inputName"].(string)
		muted, _ := data["inputMuted"].(bool)
		c.mix.mu.Lock()
		if st := c.mix.state[name]; st != nil {
			st.muted = muted
		}
		c.mix.mu.Unlock()
	case "InputAudioMonitorTypeChanged":
		name, _ := data["inputName"].(string)
		t, _ := data["monitorType"].(string)
		c.mix.mu.Lock()
		if st := c.mix.state[name]; st != nil {
			st.monitor = monitorFromOBS(t)
		}
		c.mix.mu.Unlock()
	case "InputVolumeChanged":
		name, _ := data["inputName"].(string)
		db, _ := data["inputVolumeDb"].(float64)
		c.mix.mu.Lock()
		if st := c.mix.state[name]; st != nil {
			st.volDB = db
		}
		c.mix.mu.Unlock()
	}
	return false
}

func (c *Client) meters(data map[string]any) {
	arr, _ := data["inputs"].([]any)
	m := &c.mix
	list := make([]MixerInput, 0, len(arr))
	var load []string
	m.mu.Lock()
	if m.state == nil { // событие пришло уже после снятия последнего наблюдателя
		m.mu.Unlock()
		return
	}
	for _, it := range arr {
		in, _ := it.(map[string]any)
		name, _ := in["inputName"].(string)
		if name == "" || len(list) >= maxAudioInputs {
			continue
		}
		peak := 0.0
		chans, _ := in["inputLevelsMul"].([]any)
		for _, ch := range chans {
			if v, _ := ch.([]any); len(v) >= 2 { // [величина, пик, пик входа]
				if p, _ := v[1].(float64); p > peak {
					peak = p
				}
			}
		}
		st := m.state[name]
		if st == nil {
			st = &inputState{monitor: "none"}
			m.state[name] = st
		}
		if !st.settled {
			st.settled = true
			load = append(load, name)
		}
		list = append(list, MixerInput{Name: name, Peak: dbFromMul(peak), Muted: st.muted, VolumeDB: st.volDB, Monitor: st.monitor})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	m.active = list
	ws := make([]func([]MixerInput), 0, len(m.watchers))
	for _, f := range m.watchers {
		ws = append(ws, f)
	}
	m.mu.Unlock()
	for _, name := range load {
		go c.loadInputState(name) // запрос ждёт ответ, который приходит в этом же цикле чтения — из него нельзя ждать
	}
	for _, f := range ws {
		f(list)
	}
}

// loadInputState читает у OBS начальные mute и громкость источника, который впервые появился среди активных.
func (c *Client) loadInputState(name string) {
	var muted, hasMute, hasVol, hasMon bool
	var db float64
	var mon string
	if r, ok := c.Request("GetInputMute", map[string]any{"inputName": name}); ok {
		muted, _ = r["inputMuted"].(bool)
		hasMute = true
	}
	if r, ok := c.Request("GetInputVolume", map[string]any{"inputName": name}); ok {
		db, _ = r["inputVolumeDb"].(float64)
		hasVol = true
	}
	if r, ok := c.Request("GetInputAudioMonitorType", map[string]any{"inputName": name}); ok {
		t, _ := r["monitorType"].(string)
		mon, hasMon = monitorFromOBS(t), true
	}
	c.mix.mu.Lock()
	defer c.mix.mu.Unlock()
	if st := c.mix.state[name]; st != nil {
		if hasMute {
			st.muted = muted
		}
		if hasVol {
			st.volDB = db
		}
		if hasMon {
			st.monitor = mon
		}
	}
}

func (c *Client) mixerActive(name string) bool {
	c.mix.mu.Lock()
	defer c.mix.mu.Unlock()
	for _, a := range c.mix.active {
		if a.Name == name {
			return true
		}
	}
	return false
}

// SetInputMute включает или выключает mute активного источника.
func (c *Client) SetInputMute(name string, muted bool) error {
	if !c.mixerActive(name) {
		return ErrInputNotActive
	}
	if _, ok := c.Request("SetInputMute", map[string]any{"inputName": name, "inputMuted": muted}); !ok {
		return errors.New("OBS did not confirm the mute change")
	}
	return nil
}

// SetInputVolumeDB задаёт громкость активного источника в дБ (не выше 0).
func (c *Client) SetInputVolumeDB(name string, db float64) error {
	if math.IsNaN(db) || math.IsInf(db, 0) {
		return errors.New("bad volume")
	}
	if !c.mixerActive(name) {
		return ErrInputNotActive
	}
	db = clampVolumeDB(db)
	if _, ok := c.Request("SetInputVolume", map[string]any{"inputName": name, "inputVolumeDb": db}); !ok {
		return errors.New("OBS did not confirm the volume change")
	}
	return nil
}

// ToggleInputMute переключает mute активного источника по настоящему состоянию в OBS, а не по тому, что видела страница.
func (c *Client) ToggleInputMute(name string) error {
	if !c.mixerActive(name) {
		return ErrInputNotActive
	}
	if _, ok := c.Request("ToggleInputMute", map[string]any{"inputName": name}); !ok {
		return errors.New("OBS did not confirm the mute change")
	}
	return nil
}

// ToggleInputMonitor включает или выключает прослушивание активного источника по текущему состоянию в OBS (см. nextMonitor).
func (c *Client) ToggleInputMonitor(name string) error {
	if !c.mixerActive(name) {
		return ErrInputNotActive
	}
	c.mix.mu.Lock()
	cur := "none"
	if st := c.mix.state[name]; st != nil {
		cur = st.monitor
	}
	c.mix.mu.Unlock()
	if _, ok := c.Request("SetInputAudioMonitorType", map[string]any{"inputName": name, "monitorType": monitorToOBS(nextMonitor(cur))}); !ok {
		return errors.New("OBS did not confirm the monitoring change")
	}
	return nil
}
