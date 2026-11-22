package obs

import (
	"math"
	"reflect"
	"testing"
)

func meterEvent(inputs ...map[string]any) map[string]any {
	arr := make([]any, len(inputs))
	for i, in := range inputs {
		arr[i] = in
	}
	return map[string]any{"eventType": "InputVolumeMeters", "eventData": map[string]any{"inputs": arr}}
}

func levels(peak ...float64) map[string]any {
	ch := make([]any, len(peak))
	for i, p := range peak {
		ch[i] = []any{p / 2, p, p}
	}
	return map[string]any{"inputLevelsMul": ch}
}

func TestDbFromMul(t *testing.T) {
	for in, want := range map[float64]float64{1: 0, 0.5: -6.0206, 0: -100, math.NaN(): -100, 1e-9: -100} {
		if got := dbFromMul(in); math.Abs(got-want) > 0.001 {
			t.Errorf("dbFromMul(%v) = %v, want %v", in, got, want)
		}
	}
}

func TestMixerListsOnlyInputsFromTheEventAndTakesTheLoudestChannel(t *testing.T) {
	c := New("", "")
	var got [][]MixerInput
	stop := c.WatchMixer(func(in []MixerInput) { got = append(got, in) })

	a := levels(0.1, 0.5)
	a["inputName"] = "Mic"
	b := levels(1)
	b["inputName"] = "Desktop"
	if !c.mixerEvent(meterEvent(a, b)) {
		t.Fatal("InputVolumeMeters обрабатывается микшером")
	}
	if len(got) != 1 || len(got[0]) != 2 || got[0][0].Name != "Desktop" || got[0][1].Name != "Mic" {
		t.Fatalf("источники по алфавиту: %+v", got)
	}
	if p := got[0][1].Peak; math.Abs(p-(-6.0206)) > 0.001 {
		t.Fatalf("пик берётся по самому громкому каналу: %v", p)
	}

	// следующее событие без «Mic» (сменили сцену): источник исчезает из списка
	c.mixerEvent(meterEvent(b))
	names := []string{}
	for _, in := range got[1] {
		names = append(names, in.Name)
	}
	if !reflect.DeepEqual(names, []string{"Desktop"}) {
		t.Fatalf("после смены сцены: %v", names)
	}

	if err := c.SetInputMute("Mic", true); err != ErrInputNotActive {
		t.Fatalf("неактивный источник менять нельзя: %v", err)
	}
	stop()
	if c.mixerEvent(meterEvent(a)) && len(got) != 2 {
		t.Fatal("после stop наблюдатель событий не получает")
	}
}

func TestMixerMuteAndVolumeEventsAreRememberedButStillReachTheGeneralHandler(t *testing.T) {
	c := New("", "")
	defer c.WatchMixer(func([]MixerInput) {})()
	a := levels(0.5)
	a["inputName"] = "Mic"
	c.mixerEvent(meterEvent(a))
	if c.mixerEvent(map[string]any{"eventType": "InputMuteStateChanged", "eventData": map[string]any{"inputName": "Mic", "inputMuted": true}}) {
		t.Fatal("mute должен дойти и до общего обработчика")
	}
	c.mixerEvent(map[string]any{"eventType": "InputVolumeChanged", "eventData": map[string]any{"inputName": "Mic", "inputVolumeDb": -12.5}})
	var last []MixerInput
	c.WatchMixer(func(in []MixerInput) { last = in })
	c.mixerEvent(meterEvent(a))
	if len(last) != 1 || !last[0].Muted || last[0].VolumeDB != -12.5 {
		t.Fatalf("%+v", last)
	}
}
