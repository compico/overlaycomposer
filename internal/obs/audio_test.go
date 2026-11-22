package obs

import (
	"errors"
	"reflect"
	"testing"
)

func TestMuteStateFollowsEventsAndOnlyKnownInputsToggle(t *testing.T) {
	c := New("", "")
	c.audio = map[string]bool{"Mic": false, "Desktop": true}
	c.handleEvent(map[string]any{"eventType": "InputMuteStateChanged", "eventData": map[string]any{"inputName": "Mic", "inputMuted": true}})
	// событие о незнакомом источнике (например, не аудио) не добавляет его в список
	c.handleEvent(map[string]any{"eventType": "InputMuteStateChanged", "eventData": map[string]any{"inputName": "Image", "inputMuted": true}})
	if !c.Muted("Mic") || !c.Muted("Desktop") || c.Muted("Image") {
		t.Fatalf("mute: %v", c.audio)
	}
	if got := c.AudioInputs(); !reflect.DeepEqual(got, []string{"Desktop", "Mic"}) {
		t.Fatalf("inputs: %v", got)
	}
	if err := c.ToggleMute("Image"); !errors.Is(err, ErrUnknownInput) {
		t.Fatalf("неизвестный источник: %v", err)
	}
}
