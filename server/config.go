package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

const configStorePath = "interface-config.json"

type InterfaceConfig struct {
	Baud      int    `json:"baud"`
	Parity    string `json:"parity"`   // "none" | "even" | "odd"
	StopBits  int    `json:"stopBits"` // 1 | 2
	DataBits  int    `json:"dataBits"` // 7 | 8
	UpdatedAt string `json:"updatedAt"`
}

var validBaud = map[int]bool{1200: true, 2400: true, 4800: true, 9600: true, 19200: true, 38400: true, 57600: true, 115200: true}
var validParity = map[string]bool{"none": true, "even": true, "odd": true}
var validStopBits = map[int]bool{1: true, 2: true}
var validDataBits = map[int]bool{7: true, 8: true}

func validateInterfaceConfig(c InterfaceConfig) error {
	if !validBaud[c.Baud] {
		return fmt.Errorf("invalid baud rate")
	}
	if !validParity[c.Parity] {
		return fmt.Errorf("invalid parity")
	}
	if !validStopBits[c.StopBits] {
		return fmt.Errorf("invalid stop bits")
	}
	if !validDataBits[c.DataBits] {
		return fmt.Errorf("invalid data bits")
	}
	return nil
}

type ConfigStore struct {
	mu   sync.RWMutex
	data map[string]InterfaceConfig
	path string
}

func NewConfigStore(path string) *ConfigStore {
	s := &ConfigStore{data: map[string]InterfaceConfig{}, path: path}
	s.load()
	return s
}

func (s *ConfigStore) load() {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return // fine on first run — no config saved yet
	}
	_ = json.Unmarshal(raw, &s.data)
}

func (s *ConfigStore) save() error {
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, raw, 0644)
}

func (s *ConfigStore) Get(slot string) (InterfaceConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.data[slot]
	return c, ok
}

func (s *ConfigStore) Set(slot string, c InterfaceConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	s.data[slot] = c
	return s.save()
}
