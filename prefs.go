package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type persistedMonitorPrefs struct {
	Known     map[string]bool   `json:"known"`
	Monitored map[string]bool   `json:"monitored"`
	Adapters  map[string]string `json:"adapters"`
	PATs      map[string]string `json:"management_pats"`
	Names     map[string]string `json:"names"`
}

type monitorPrefs struct {
	mu      sync.RWMutex
	path    string
	keyPath string
	key     []byte
	data    persistedMonitorPrefs
}

func newMonitorPrefs() *monitorPrefs {
	return &monitorPrefs{data: persistedMonitorPrefs{Known: map[string]bool{}, Monitored: map[string]bool{}, Adapters: map[string]string{}, PATs: map[string]string{}, Names: map[string]string{}}}
}

func (p *monitorPrefs) configure(path string) error {
	if p == nil || strings.TrimSpace(path) == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.path == path && len(p.key) == 32 {
		return nil
	}
	p.path = path
	p.keyPath = path + ".key"
	key, err := loadOrCreateKey(p.keyPath)
	if err != nil {
		return err
	}
	p.key = key
	p.data = persistedMonitorPrefs{Known: map[string]bool{}, Monitored: map[string]bool{}, Adapters: map[string]string{}, PATs: map[string]string{}, Names: map[string]string{}}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read monitor preferences: %w", err)
	}
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, &p.data); err != nil {
		return fmt.Errorf("decode monitor preferences: %w", err)
	}
	if p.data.Known == nil {
		p.data.Known = map[string]bool{}
	}
	if p.data.Monitored == nil {
		p.data.Monitored = map[string]bool{}
	}
	if p.data.Adapters == nil {
		p.data.Adapters = map[string]string{}
	}
	if p.data.PATs == nil {
		p.data.PATs = map[string]string{}
	}
	if p.data.Names == nil {
		p.data.Names = map[string]string{}
	}
	return nil
}

func (p *monitorPrefs) isMonitored(id string, defaultValue bool) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	value, ok := p.data.Monitored[id]
	if ok {
		return value
	}
	return defaultValue
}

func (p *monitorPrefs) adapter(id, fallback string) string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return firstNonEmpty(p.data.Adapters[id], fallback)
}

func (p *monitorPrefs) name(id, fallback string) string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return firstNonEmpty(p.data.Names[id], fallback)
}

func (p *monitorPrefs) pat(id string) string {
	p.mu.RLock()
	encoded := p.data.PATs[id]
	key := append([]byte(nil), p.key...)
	p.mu.RUnlock()
	if encoded == "" || len(key) != 32 {
		return ""
	}
	value, err := decryptPreference(key, id, encoded)
	if err != nil {
		return ""
	}
	return value
}

func (p *monitorPrefs) patConfigured(id string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.data.PATs[id] != ""
}

func (p *monitorPrefs) reconcile(ids []string) {
	defaults := make(map[string]bool, len(ids))
	for _, id := range ids {
		defaults[id] = true
	}
	p.reconcileDefaults(defaults)
}

func (p *monitorPrefs) reconcileDefaults(defaults map[string]bool) {
	p.mu.Lock()
	changed := false
	for id, defaultValue := range defaults {
		if !p.data.Known[id] {
			p.data.Known[id] = true
			p.data.Monitored[id] = defaultValue
			changed = true
		}
	}
	p.mu.Unlock()
	if changed {
		_ = p.save()
	}
}

func (p *monitorPrefs) update(id string, monitored *bool, adapter, pat string, clearPAT, clearAdapter bool, customName *string) error {
	if p == nil || id == "" {
		return fmt.Errorf("provider id is required")
	}
	p.mu.Lock()
	if monitored != nil {
		p.data.Monitored[id] = *monitored
	}
	if clearAdapter {
		delete(p.data.Adapters, id)
	} else if strings.TrimSpace(adapter) != "" {
		p.data.Adapters[id] = strings.ToLower(strings.TrimSpace(adapter))
	}
	if clearPAT {
		delete(p.data.PATs, id)
	} else if strings.TrimSpace(pat) != "" {
		encrypted, err := encryptPreference(p.key, id, strings.TrimSpace(pat))
		if err != nil {
			p.mu.Unlock()
			return err
		}
		p.data.PATs[id] = encrypted
	}
	if customName != nil {
		name := strings.TrimSpace(*customName)
		if len([]rune(name)) > 80 {
			p.mu.Unlock()
			return fmt.Errorf("custom name must be 80 characters or fewer")
		}
		for _, r := range name {
			if r < 0x20 || r == 0x7f {
				p.mu.Unlock()
				return fmt.Errorf("custom name contains a control character")
			}
		}
		if name == "" {
			delete(p.data.Names, id)
		} else {
			p.data.Names[id] = name
		}
	}
	p.data.Known[id] = true
	p.mu.Unlock()
	return p.save()
}

func (p *monitorPrefs) save() error {
	if p == nil || p.path == "" {
		return nil
	}
	p.mu.RLock()
	raw, err := json.Marshal(p.data)
	path := p.path
	p.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("encode monitor preferences: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".upstream-monitor-prefs-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func loadOrCreateKey(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err == nil {
		if len(raw) != 32 {
			return nil, fmt.Errorf("monitor key must contain 32 bytes")
		}
		return raw, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

func encryptPreference(key []byte, aad, value string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, nonce, []byte(value), []byte(aad))
	return base64.RawStdEncoding.EncodeToString(append(nonce, sealed...)), nil
}

func decryptPreference(key []byte, aad, encoded string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	raw, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil || len(raw) < gcm.NonceSize() {
		return "", fmt.Errorf("invalid encrypted preference")
	}
	value, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], []byte(aad))
	if err != nil {
		return "", err
	}
	return string(value), nil
}
