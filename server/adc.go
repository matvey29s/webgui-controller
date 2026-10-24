package main

// ADC / hardware-detection layer.
//
// Of the 9 ports drawn in the artwork, 8 are simple presence/link
// indicators (power, ethernet1, ethernet2, usb, audioIn, audioOut, sdcard,
// lcd) read from ordinary Linux interfaces. Only "modules" -- the expansion
// bay -- carries an identification ADC channel that reports which module or
// field-bus interface (if any) is plugged in.
//
// Real hardware notes for the AM3352:
//   - "modules" bay identification: the on-chip ADC (tscadc) is exposed by
//     the Linux IIO subsystem, typically at
//     /sys/bus/iio/devices/iio:device0/in_voltage<N>_raw. The bay's
//     identification resistor is wired to one such channel; reading the raw
//     value and mapping it against known thresholds (idTable below) tells
//     you which module type, if any, is plugged in.
//   - ethernet1 / ethernet2: /sys/class/net/eth0/carrier, eth1/carrier
//   - usb: presence under /sys/bus/usb/devices or udev
//   - sdcard: presence of the SD block device, e.g. /dev/mmcblk1
//   - audioIn / audioOut / power / lcd: whatever GPIO/status line your board
//     exposes for these; wire readAudioIn() etc. accordingly.
//
// Everything above this file (main.go, the frontend) only ever sees the
// resolved SlotState values, so swapping any mock read below for a real one
// requires no changes anywhere else in the stack.

import (
	"encoding/json"
	"sync"
	"time"
)

type idBand struct {
	min, max float64
	slotType string
	kind     string // "module" or "interface"
}

// Example calibration table for the "modules" bay -- replace with real
// measured voltage bands for your identification resistors.
var idTable = []idBand{
	{0.00, 0.15, "", ""}, // nothing plugged in
	{0.60, 0.90, "relay8x", "module"},
	{1.10, 1.40, "analog_in_4ch", "module"},
	{1.60, 1.90, "rs485", "interface"},
	{2.10, 2.40, "rs232", "interface"},
	{2.60, 2.90, "can_bus", "interface"},
}

func resolveIdentity(voltage float64) (slotType, kind string, detected bool) {
	for _, b := range idTable {
		if voltage >= b.min && voltage <= b.max {
			if b.slotType == "" {
				return "", "", false
			}
			return b.slotType, b.kind, true
		}
	}
	return "", "", false
}

// SlotState is the JSON shape sent to the frontend for one port.
type SlotState struct {
	Connected *bool   `json:"connected,omitempty"`
	Present   *bool   `json:"present,omitempty"`
	OK        *bool   `json:"ok,omitempty"`
	Detected  *bool   `json:"detected,omitempty"`
	Type      *string `json:"type,omitempty"`
	Kind      *string `json:"kind,omitempty"`
}

func boolPtr(b bool) *bool    { return &b }
func strPtr(s string) *string { return &s }

// --- Mock hardware reads (swap for real syscalls per the notes above) -----
// simState is the in-memory stand-in for real hardware, protected by simMu,
// so this program is runnable stand-alone without any physical PLC attached.
var (
	simMu    sync.Mutex
	simState = map[string]float64{
		"ethernet1": 1, // serving this page over it, so start "connected"
		"ethernet2": 0,
		"usb":       0,
		"audioIn":   0,
		"audioOut":  0,
		"sdcard":    1,
		"lcd":       1,
		"bay":       1.75, // "modules" bay ADC channel -> rs485 present by default (demo)
	}
)

func readBool(key string) bool {
	simMu.Lock()
	defer simMu.Unlock()
	return simState[key] != 0
}

func readBayVoltage() float64 {
	simMu.Lock()
	defer simMu.Unlock()
	return simState["bay"]
}

// --- Public monitor ----------------------------------------------------------

type AdcMonitor struct {
	mu     sync.RWMutex
	state  map[string]SlotState
	Events chan map[string]SlotState // emits only the changed slots (diff)
}

func NewAdcMonitor() *AdcMonitor {
	m := &AdcMonitor{state: map[string]SlotState{}, Events: make(chan map[string]SlotState, 16)}
	m.state = m.readAll()
	return m
}

func (m *AdcMonitor) readAll() map[string]SlotState {
	voltage := readBayVoltage()
	slotType, kind, detected := resolveIdentity(voltage)
	modules := SlotState{Detected: boolPtr(detected)}
	if detected {
		modules.Type = strPtr(slotType)
		modules.Kind = strPtr(kind)
	}

	return map[string]SlotState{
		"power":     {OK: boolPtr(true)}, // presence implied — board is running
		"ethernet1": {Connected: boolPtr(readBool("ethernet1"))},
		"ethernet2": {Connected: boolPtr(readBool("ethernet2"))},
		"usb":       {Present: boolPtr(readBool("usb"))},
		"audioIn":   {Present: boolPtr(readBool("audioIn"))},
		"audioOut":  {Present: boolPtr(readBool("audioOut"))},
		"sdcard":    {Present: boolPtr(readBool("sdcard"))},
		"lcd":       {OK: boolPtr(readBool("lcd"))},
		"modules":   modules,
	}
}

func (m *AdcMonitor) GetFullState() map[string]SlotState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]SlotState, len(m.state))
	for k, v := range m.state {
		out[k] = v
	}
	return out
}

func slotStateEqual(a, b SlotState) bool {
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	return string(aj) == string(bj)
}

// Start begins polling in the background at the given interval.
func (m *AdcMonitor) Start(interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			next := m.readAll()

			m.mu.Lock()
			diff := map[string]SlotState{}
			for k, v := range next {
				if old, ok := m.state[k]; !ok || !slotStateEqual(old, v) {
					diff[k] = v
				}
			}
			m.state = next
			m.mu.Unlock()

			if len(diff) > 0 {
				select {
				case m.Events <- diff:
				default: // drop if no one is listening fast enough; SSE clients get full state on connect anyway
				}
			}
		}
	}()
}

// SimulateHotplug lets the demo UI fake hardware changes without real
// hardware attached. Remove this once wired to a real board.
// For "modules" the value is one of: "off", "relay", "rs485".
func (m *AdcMonitor) SimulateHotplug(slot string, value string) {
	simMu.Lock()
	defer simMu.Unlock()

	if slot == "modules" {
		switch value {
		case "rs485":
			simState["bay"] = 1.75
		case "relay":
			simState["bay"] = 0.75
		default:
			simState["bay"] = 0.0
		}
		return
	}

	if _, ok := simState[slot]; ok {
		if value == "on" {
			simState[slot] = 1
		} else {
			simState[slot] = 0
		}
	}
}
