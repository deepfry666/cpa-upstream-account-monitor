package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const monitorPrefsFormatVersion = 2

const (
	patActionKeep    = "keep"
	patActionReplace = "replace"
	patActionClear   = "clear"
	patStatusMissing = "missing"
	patStatusReady   = "ready"
	patStatusError   = "decrypt_error"
	patStatusRelink  = "relink_required"
)

type persistedMonitorPrefs struct {
	FormatVersion  int                          `json:"format_version"`
	Revision       int64                        `json:"revision"`
	Known          map[string]bool              `json:"known"`
	Monitored      map[string]bool              `json:"monitored"`
	Adapters       map[string]string            `json:"adapters"`
	PATs           map[string]string            `json:"management_pats"`
	Names          map[string]string            `json:"names"`
	AccountOrder   []string                     `json:"account_order,omitempty"`
	FirstSeen      map[string]time.Time         `json:"first_seen,omitempty"`
	Identities     map[string]persistedIdentity `json:"identities,omitempty"`
	RelinkRequired map[string]bool              `json:"relink_required,omitempty"`
	QueryRevisions map[string]int64             `json:"query_revisions,omitempty"`
	Thresholds     *thresholdConfig             `json:"thresholds,omitempty"`
}

type persistedIdentity struct {
	AccountID   string    `json:"account_id"`
	Source      string    `json:"source"`
	SourceID    string    `json:"source_id,omitempty"`
	LegacyID    string    `json:"legacy_id,omitempty"`
	BaseURL     string    `json:"base_url,omitempty"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	Provider    string    `json:"provider,omitempty"`
	FirstSeenAt time.Time `json:"first_seen_at"`
}

type providerPreferenceMutation struct {
	ID           string
	Monitored    *bool
	Adapter      string
	ClearAdapter bool
	PATAction    string
	PAT          string
	CustomName   *string
}

type revisionConflictError struct {
	Expected int64
	Actual   int64
}

func (e *revisionConflictError) Error() string {
	return fmt.Sprintf("monitor preferences revision conflict: expected %d, current %d", e.Expected, e.Actual)
}

type preferencePersistenceError struct {
	Err error
}

func (e *preferencePersistenceError) Error() string {
	return fmt.Sprintf("persist monitor preferences: %v", e.Err)
}

func (e *preferencePersistenceError) Unwrap() error {
	return e.Err
}

type monitorPrefs struct {
	mu       sync.RWMutex
	commitMu sync.Mutex
	path     string
	keyPath  string
	key      []byte
	keyErr   error
	loaded   bool
	data     persistedMonitorPrefs
}

func newMonitorPrefs() *monitorPrefs {
	return &monitorPrefs{data: newPersistedMonitorPrefs()}
}

func newPersistedMonitorPrefs() persistedMonitorPrefs {
	return persistedMonitorPrefs{
		FormatVersion:  monitorPrefsFormatVersion,
		Known:          map[string]bool{},
		Monitored:      map[string]bool{},
		Adapters:       map[string]string{},
		PATs:           map[string]string{},
		Names:          map[string]string{},
		FirstSeen:      map[string]time.Time{},
		Identities:     map[string]persistedIdentity{},
		RelinkRequired: map[string]bool{},
		QueryRevisions: map[string]int64{},
	}
}

func (p *monitorPrefs) configure(path string) error {
	if p == nil || strings.TrimSpace(path) == "" {
		return nil
	}
	p.commitMu.Lock()
	defer p.commitMu.Unlock()
	path = strings.TrimSpace(path)
	p.mu.RLock()
	alreadyLoaded := p.path == path && p.loaded
	p.mu.RUnlock()
	if alreadyLoaded {
		return nil
	}

	raw, readErr := os.ReadFile(path)
	if readErr != nil && !os.IsNotExist(readErr) {
		return fmt.Errorf("read monitor preferences: %w", readErr)
	}
	data := newPersistedMonitorPrefs()
	legacyFormat := false
	if readErr == nil && len(raw) > 0 {
		var header struct {
			FormatVersion int `json:"format_version"`
		}
		if err := json.Unmarshal(raw, &header); err != nil {
			return fmt.Errorf("decode monitor preferences: %w", err)
		}
		if err := json.Unmarshal(raw, &data); err != nil {
			return fmt.Errorf("decode monitor preferences: %w", err)
		}
		legacyFormat = header.FormatVersion == 0 || header.FormatVersion < monitorPrefsFormatVersion
		if header.FormatVersion > monitorPrefsFormatVersion {
			return fmt.Errorf("monitor preferences format %d is newer than supported format %d", header.FormatVersion, monitorPrefsFormatVersion)
		}
		data.FormatVersion = monitorPrefsFormatVersion
	}
	normalizePersistedPrefs(&data)
	if legacyFormat {
		for id, encrypted := range data.PATs {
			if strings.TrimSpace(encrypted) != "" {
				data.RelinkRequired[id] = true
			}
		}
	}

	keyPath := path + ".key"
	key, keyErr := loadKey(keyPath)
	if keyErr != nil {
		if os.IsNotExist(keyErr) && len(data.PATs) == 0 {
			key, keyErr = loadOrCreateKey(keyPath)
		}
	}

	p.mu.Lock()
	p.path = path
	p.keyPath = keyPath
	p.key = append([]byte(nil), key...)
	p.keyErr = keyErr
	p.data = data
	p.loaded = true
	p.mu.Unlock()
	return nil
}

func normalizePersistedPrefs(data *persistedMonitorPrefs) {
	if data == nil {
		return
	}
	data.FormatVersion = monitorPrefsFormatVersion
	if data.Known == nil {
		data.Known = map[string]bool{}
	}
	if data.Monitored == nil {
		data.Monitored = map[string]bool{}
	}
	if data.Adapters == nil {
		data.Adapters = map[string]string{}
	}
	if data.PATs == nil {
		data.PATs = map[string]string{}
	}
	if data.Names == nil {
		data.Names = map[string]string{}
	}
	if data.FirstSeen == nil {
		data.FirstSeen = map[string]time.Time{}
	}
	if data.Identities == nil {
		data.Identities = map[string]persistedIdentity{}
	}
	if data.RelinkRequired == nil {
		data.RelinkRequired = map[string]bool{}
	}
	if data.QueryRevisions == nil {
		data.QueryRevisions = map[string]int64{}
	}
	seenOrder := make(map[string]struct{}, len(data.AccountOrder))
	order := data.AccountOrder[:0]
	for _, id := range data.AccountOrder {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, exists := seenOrder[id]; exists {
			continue
		}
		seenOrder[id] = struct{}{}
		order = append(order, id)
	}
	data.AccountOrder = order
}

func clonePersistedPrefs(data persistedMonitorPrefs) persistedMonitorPrefs {
	clone := persistedMonitorPrefs{
		FormatVersion:  data.FormatVersion,
		Revision:       data.Revision,
		Known:          cloneBoolMap(data.Known),
		Monitored:      cloneBoolMap(data.Monitored),
		Adapters:       cloneStringMap(data.Adapters),
		PATs:           cloneStringMap(data.PATs),
		Names:          cloneStringMap(data.Names),
		AccountOrder:   append([]string(nil), data.AccountOrder...),
		FirstSeen:      cloneTimeMap(data.FirstSeen),
		Identities:     cloneIdentityMap(data.Identities),
		RelinkRequired: cloneBoolMap(data.RelinkRequired),
		QueryRevisions: cloneInt64Map(data.QueryRevisions),
	}
	if data.Thresholds != nil {
		thresholds := *data.Thresholds
		thresholds.CashByCurrency = cloneCashThresholdMap(data.Thresholds.CashByCurrency)
		thresholds.CashByAccount = cloneCashThresholdMap(data.Thresholds.CashByAccount)
		clone.Thresholds = &thresholds
	}
	return clone
}

func cloneCashThresholdMap(values map[string]cashThreshold) map[string]cashThreshold {
	if values == nil {
		return nil
	}
	out := make(map[string]cashThreshold, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func cloneBoolMap(values map[string]bool) map[string]bool {
	out := make(map[string]bool, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func cloneStringMap(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func cloneTimeMap(values map[string]time.Time) map[string]time.Time {
	out := make(map[string]time.Time, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func cloneIdentityMap(values map[string]persistedIdentity) map[string]persistedIdentity {
	out := make(map[string]persistedIdentity, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func cloneInt64Map(values map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
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
	keyErr := p.keyErr
	relink := p.data.RelinkRequired[id]
	p.mu.RUnlock()
	if encoded == "" || keyErr != nil || len(key) != 32 || relink {
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

func (p *monitorPrefs) patStatus(id string) string {
	if p == nil {
		return patStatusMissing
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.data.RelinkRequired[id] {
		return patStatusRelink
	}
	encoded := p.data.PATs[id]
	if encoded == "" {
		return patStatusMissing
	}
	if p.keyErr != nil {
		if os.IsNotExist(p.keyErr) {
			return patStatusMissing
		}
		return patStatusError
	}
	if len(p.key) != 32 {
		return patStatusError
	}
	if _, err := decryptPreference(p.key, id, encoded); err != nil {
		return patStatusError
	}
	return patStatusReady
}

func (p *monitorPrefs) revision() int64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.data.Revision
}

func (p *monitorPrefs) queryRevision(id string) int64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.data.QueryRevisions[id]
}

func (p *monitorPrefs) thresholds(fallback thresholdConfig) thresholdConfig {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.data.Thresholds == nil {
		return fallback
	}
	out := *p.data.Thresholds
	out.CashByCurrency = cloneCashThresholdMap(p.data.Thresholds.CashByCurrency)
	out.CashByAccount = cloneCashThresholdMap(p.data.Thresholds.CashByAccount)
	return out
}

func (p *monitorPrefs) updateThresholdsAtRevision(expected *int64, thresholds thresholdConfig) (int64, error) {
	return p.commitAtRevision(expected, func(data *persistedMonitorPrefs) (bool, error) {
		if data.Thresholds != nil &&
			data.Thresholds.WarningPercent == thresholds.WarningPercent &&
			data.Thresholds.CriticalPercent == thresholds.CriticalPercent &&
			cashThresholdMapsEqual(data.Thresholds.CashByCurrency, thresholds.CashByCurrency) &&
			cashThresholdMapsEqual(data.Thresholds.CashByAccount, thresholds.CashByAccount) {
			return false, nil
		}
		next := thresholds
		next.CashByCurrency = cloneCashThresholdMap(thresholds.CashByCurrency)
		next.CashByAccount = cloneCashThresholdMap(thresholds.CashByAccount)
		data.Thresholds = &next
		return true, nil
	})
}

func (p *monitorPrefs) invalidateQueryResults(ids []string) error {
	_, err := p.commit(func(data *persistedMonitorPrefs) (bool, error) {
		changed := false
		seen := make(map[string]struct{}, len(ids))
		for _, rawID := range ids {
			id := strings.TrimSpace(rawID)
			if id == "" {
				continue
			}
			if _, duplicate := seen[id]; duplicate {
				continue
			}
			seen[id] = struct{}{}
			data.QueryRevisions[id]++
			changed = true
		}
		return changed, nil
	})
	return err
}

func cashThresholdMapsEqual(left, right map[string]cashThreshold) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func (p *monitorPrefs) canCommitQueryResult(id string, revision int64) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.data.Known[id] && p.data.Monitored[id] && p.data.QueryRevisions[id] == revision
}

func (p *monitorPrefs) firstSeen(id string) *time.Time {
	p.mu.RLock()
	value, ok := p.data.FirstSeen[id]
	p.mu.RUnlock()
	if !ok || value.IsZero() {
		return nil
	}
	out := value
	return &out
}

func (p *monitorPrefs) accountOrder() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]string(nil), p.data.AccountOrder...)
}

func (p *monitorPrefs) relinkRequired(id string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.data.RelinkRequired[id]
}

func (p *monitorPrefs) reconcile(ids []string) {
	now := time.Now().UTC()
	mutations := make([]providerPreferenceMutation, 0, len(ids))
	for _, id := range ids {
		mutations = append(mutations, providerPreferenceMutation{ID: id, PATAction: patActionKeep})
	}
	_, _ = p.commit(func(data *persistedMonitorPrefs) (bool, error) {
		changed := false
		for _, mutation := range mutations {
			changed = ensureKnownPreference(data, strings.TrimSpace(mutation.ID), false, now) || changed
		}
		return changed, nil
	})
}

func (p *monitorPrefs) reconcileDefaults(defaults map[string]bool) {
	now := time.Now().UTC()
	_, _ = p.commit(func(data *persistedMonitorPrefs) (bool, error) {
		changed := false
		for id, defaultValue := range defaults {
			changed = ensureKnownPreference(data, id, defaultValue, now) || changed
		}
		return changed, nil
	})
}

func ensureKnownPreference(data *persistedMonitorPrefs, id string, defaultValue bool, now time.Time) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	changed := false
	if !data.Known[id] {
		data.Known[id] = true
		data.Monitored[id] = defaultValue
		changed = true
	}
	if _, ok := data.FirstSeen[id]; !ok || data.FirstSeen[id].IsZero() {
		data.FirstSeen[id] = now
		changed = true
	}
	if !containsString(data.AccountOrder, id) {
		data.AccountOrder = append(data.AccountOrder, id)
		changed = true
	}
	return changed
}

func (p *monitorPrefs) update(id string, monitored *bool, adapter, pat string, clearPAT, clearAdapter bool, customName *string) error {
	action := patActionKeep
	if clearPAT {
		if strings.TrimSpace(pat) != "" {
			return fmt.Errorf("management PAT cannot be both cleared and replaced")
		}
		action = patActionClear
	} else if strings.TrimSpace(pat) != "" {
		action = patActionReplace
	}
	_, err := p.updateBatch([]providerPreferenceMutation{{
		ID:           id,
		Monitored:    monitored,
		Adapter:      adapter,
		ClearAdapter: clearAdapter,
		PATAction:    action,
		PAT:          pat,
		CustomName:   customName,
	}})
	return err
}

func (p *monitorPrefs) updateBatch(mutations []providerPreferenceMutation) (int64, error) {
	return p.updateBatchAtRevision(nil, mutations)
}

func (p *monitorPrefs) updateBatchAtRevision(expected *int64, mutations []providerPreferenceMutation) (int64, error) {
	return p.updateBatchAndThresholdsAtRevision(expected, mutations, nil)
}

func (p *monitorPrefs) updateBatchAndThresholdsAtRevision(expected *int64, mutations []providerPreferenceMutation, thresholds *thresholdConfig) (int64, error) {
	return p.commitAtRevision(expected, func(data *persistedMonitorPrefs) (bool, error) {
		now := time.Now().UTC()
		changed := false
		for _, mutation := range mutations {
			mutation.ID = strings.TrimSpace(mutation.ID)
			if mutation.ID == "" {
				return false, fmt.Errorf("provider id is required")
			}
			action := strings.ToLower(strings.TrimSpace(mutation.PATAction))
			if action == "" {
				action = patActionKeep
			}
			switch action {
			case patActionKeep, patActionReplace, patActionClear:
			default:
				return false, fmt.Errorf("unsupported management PAT action %q", mutation.PATAction)
			}
			if mutation.CustomName != nil {
				if err := validateCustomName(*mutation.CustomName); err != nil {
					return false, err
				}
			}
			if mutation.ClearAdapter && strings.TrimSpace(mutation.Adapter) != "" {
				return false, fmt.Errorf("adapter cannot be both cleared and replaced")
			}
			if action == patActionReplace && strings.TrimSpace(mutation.PAT) == "" {
				return false, fmt.Errorf("replacement management PAT is required")
			}
			if action == patActionClear && strings.TrimSpace(mutation.PAT) != "" {
				return false, fmt.Errorf("management PAT cannot be both cleared and replaced")
			}
		}
		for _, mutation := range mutations {
			id := mutation.ID
			changed = ensureKnownPreference(data, id, true, now) || changed
			queryChanged := false
			if mutation.Monitored != nil && data.Monitored[id] != *mutation.Monitored {
				data.Monitored[id] = *mutation.Monitored
				changed = true
				queryChanged = true
			}
			if mutation.ClearAdapter {
				if _, ok := data.Adapters[id]; ok {
					delete(data.Adapters, id)
					changed = true
					queryChanged = true
				}
			} else if strings.TrimSpace(mutation.Adapter) != "" {
				adapter := strings.ToLower(strings.TrimSpace(mutation.Adapter))
				if data.Adapters[id] != adapter {
					data.Adapters[id] = adapter
					changed = true
					queryChanged = true
				}
			}
			switch strings.ToLower(strings.TrimSpace(mutation.PATAction)) {
			case patActionReplace:
				encrypted, err := encryptPreference(p.preferenceKey(), id, strings.TrimSpace(mutation.PAT))
				if err != nil {
					return false, err
				}
				data.PATs[id] = encrypted
				delete(data.RelinkRequired, id)
				changed = true
				queryChanged = true
			case patActionClear:
				if _, ok := data.PATs[id]; ok {
					delete(data.PATs, id)
					changed = true
					queryChanged = true
				}
				if data.RelinkRequired[id] {
					delete(data.RelinkRequired, id)
					changed = true
					queryChanged = true
				}
			}
			if mutation.CustomName != nil {
				name := strings.TrimSpace(*mutation.CustomName)
				if name == "" {
					if _, ok := data.Names[id]; ok {
						delete(data.Names, id)
						changed = true
					}
				} else if data.Names[id] != name {
					data.Names[id] = name
					changed = true
				}
			}
			if queryChanged {
				data.QueryRevisions[id]++
			}
		}
		if thresholds != nil && !thresholdConfigsEqual(data.Thresholds, thresholds) {
			next := *thresholds
			next.CashByCurrency = cloneCashThresholdMap(thresholds.CashByCurrency)
			next.CashByAccount = cloneCashThresholdMap(thresholds.CashByAccount)
			data.Thresholds = &next
			changed = true
		}
		return changed, nil
	})
}

func thresholdConfigsEqual(left *thresholdConfig, right *thresholdConfig) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.WarningPercent == right.WarningPercent &&
		left.CriticalPercent == right.CriticalPercent &&
		cashThresholdMapsEqual(left.CashByCurrency, right.CashByCurrency) &&
		cashThresholdMapsEqual(left.CashByAccount, right.CashByAccount)
}

func validateCustomName(name string) error {
	name = strings.TrimSpace(name)
	if len([]rune(name)) > 80 {
		return fmt.Errorf("custom name must be 80 characters or fewer")
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("custom name contains a control character")
		}
	}
	return nil
}

func (p *monitorPrefs) preferenceKey() []byte {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]byte(nil), p.key...)
}

func (p *monitorPrefs) resolveIdentities(candidates []credentialCandidate) ([]string, error) {
	if p == nil {
		return nil, fmt.Errorf("monitor preferences are unavailable")
	}
	ids := make([]string, len(candidates))
	legacyCounts := make(map[string]int, len(candidates))
	for _, candidate := range candidates {
		if legacyID := strings.TrimSpace(candidate.LegacyID); legacyID != "" {
			legacyCounts[legacyID]++
		}
	}
	_, err := p.commit(func(data *persistedMonitorPrefs) (bool, error) {
		now := time.Now().UTC()
		changed := false
		for index, candidate := range candidates {
			if strings.TrimSpace(candidate.AccountID) != "" {
				ids[index] = strings.TrimSpace(candidate.AccountID)
				changed = ensureKnownPreference(data, ids[index], false, now) || changed
				continue
			}
			for accountID, identity := range data.Identities {
				if identityMatchesCandidate(identity, candidate) {
					ids[index] = accountID
					break
				}
			}
			if ids[index] != "" {
				changed = ensureKnownPreference(data, ids[index], false, now) || changed
				continue
			}
			accountID := ""
			if strings.EqualFold(candidate.Source, "host") && strings.TrimSpace(candidate.SourceID) != "" {
				accountID = stableAccountID(p.preferenceKey(), candidate.Source, candidate.SourceID, candidate.Fingerprint)
			} else {
				generated, err := newAccountID()
				if err != nil {
					return false, err
				}
				accountID = generated
			}
			ids[index] = accountID
			if legacyID := strings.TrimSpace(candidate.LegacyID); legacyID != "" && hasLegacyPreference(data, legacyID) {
				if legacyCounts[legacyID] == 1 && hasStableIdentityBasis(candidate) {
					if err := migratePreferenceData(data, p.preferenceKey(), legacyID, accountID); err != nil {
						data.RelinkRequired[accountID] = true
						changed = true
					} else {
						changed = true
					}
				} else if data.PATs[legacyID] != "" || data.RelinkRequired[legacyID] {
					data.RelinkRequired[accountID] = true
					changed = true
				}
			}
			data.Identities[accountID] = persistedIdentity{
				AccountID:   accountID,
				Source:      candidate.Source,
				SourceID:    candidate.SourceID,
				LegacyID:    candidate.LegacyID,
				BaseURL:     candidate.BaseURL,
				Fingerprint: candidate.Fingerprint,
				Provider:    candidate.Provider,
				FirstSeenAt: now,
			}
			changed = ensureKnownPreference(data, accountID, false, now) || changed
		}
		return changed, nil
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

func hasStableIdentityBasis(candidate credentialCandidate) bool {
	switch strings.ToLower(strings.TrimSpace(candidate.Source)) {
	case "host":
		return strings.TrimSpace(candidate.SourceID) != ""
	case "static":
		return normalizeIdentityURL(candidate.BaseURL) != "" && strings.TrimSpace(candidate.Fingerprint) != ""
	default:
		return false
	}
}

func identityMatchesCandidate(identity persistedIdentity, candidate credentialCandidate) bool {
	if identity.Source != candidate.Source {
		return false
	}
	if strings.TrimSpace(candidate.SourceID) != "" {
		return identity.SourceID == candidate.SourceID
	}
	if strings.TrimSpace(candidate.Fingerprint) == "" || strings.TrimSpace(identity.Fingerprint) == "" {
		return false
	}
	return normalizeIdentityURL(identity.BaseURL) == normalizeIdentityURL(candidate.BaseURL) &&
		identity.Fingerprint == candidate.Fingerprint
}

func hasLegacyPreference(data *persistedMonitorPrefs, id string) bool {
	if data == nil || id == "" {
		return false
	}
	if _, ok := data.Known[id]; ok {
		return true
	}
	if _, ok := data.Monitored[id]; ok {
		return true
	}
	if _, ok := data.Adapters[id]; ok {
		return true
	}
	if _, ok := data.PATs[id]; ok {
		return true
	}
	if _, ok := data.Names[id]; ok {
		return true
	}
	if _, ok := data.FirstSeen[id]; ok {
		return true
	}
	if _, ok := data.RelinkRequired[id]; ok {
		return true
	}
	return containsString(data.AccountOrder, id)
}

func stableAccountID(key []byte, source, sourceID, fingerprint string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(strings.ToLower(strings.TrimSpace(source))))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(strings.TrimSpace(sourceID)))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(strings.TrimSpace(fingerprint)))
	sum := mac.Sum(nil)
	return "acct_" + hex.EncodeToString(sum[:12])
}

func credentialIdentityFingerprint(key []byte, source, sourceID, baseURL, secret string) string {
	mac := hmac.New(sha256.New, key)
	for _, value := range []string{strings.ToLower(strings.TrimSpace(source)), strings.TrimSpace(sourceID), normalizeIdentityURL(baseURL), secret} {
		_, _ = mac.Write([]byte(value))
		_, _ = mac.Write([]byte{0})
	}
	return hex.EncodeToString(mac.Sum(nil))
}

func normalizeIdentityURL(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	raw = strings.TrimRight(raw, "/")
	return raw
}

func (p *monitorPrefs) migratePAT(oldID, newID string) error {
	if p == nil {
		return fmt.Errorf("monitor preferences are unavailable")
	}
	oldID = strings.TrimSpace(oldID)
	newID = strings.TrimSpace(newID)
	if oldID == "" || newID == "" || oldID == newID {
		return nil
	}
	_, err := p.commit(func(data *persistedMonitorPrefs) (bool, error) {
		return true, migratePreferenceData(data, p.preferenceKey(), oldID, newID)
	})
	return err
}

func migratePreferenceData(data *persistedMonitorPrefs, key []byte, oldID, newID string) error {
	if data == nil || oldID == "" || newID == "" || oldID == newID {
		return nil
	}
	encoded := data.PATs[oldID]
	if encoded != "" {
		plain, err := decryptPreference(key, oldID, encoded)
		if err != nil {
			return fmt.Errorf("decrypt legacy management PAT: %w", err)
		}
		reencrypted, err := encryptPreference(key, newID, plain)
		if err != nil {
			return fmt.Errorf("encrypt migrated management PAT: %w", err)
		}
		data.PATs[newID] = reencrypted
		delete(data.PATs, oldID)
	}
	if data.RelinkRequired[oldID] {
		delete(data.RelinkRequired, oldID)
	}
	movePreferenceValue(data.Known, oldID, newID)
	movePreferenceValue(data.Monitored, oldID, newID)
	movePreferenceString(data.Adapters, oldID, newID)
	movePreferenceString(data.Names, oldID, newID)
	if firstSeen, ok := data.FirstSeen[oldID]; ok {
		data.FirstSeen[newID] = firstSeen
		delete(data.FirstSeen, oldID)
	}
	replaceString(data.AccountOrder, oldID, newID)
	return nil
}

func movePreferenceValue[T any](values map[string]T, oldID, newID string) {
	if value, ok := values[oldID]; ok {
		values[newID] = value
		delete(values, oldID)
	}
}

func movePreferenceString(values map[string]string, oldID, newID string) {
	if value, ok := values[oldID]; ok {
		values[newID] = value
		delete(values, oldID)
	}
}

func replaceString(values []string, oldValue, newValue string) {
	for index := range values {
		if values[index] == oldValue {
			values[index] = newValue
		}
	}
}

func (p *monitorPrefs) save() error {
	_, err := p.commit(func(*persistedMonitorPrefs) (bool, error) { return true, nil })
	return err
}

func (p *monitorPrefs) commit(mutator func(*persistedMonitorPrefs) (bool, error)) (int64, error) {
	return p.commitAtRevision(nil, mutator)
}

func (p *monitorPrefs) commitAtRevision(expected *int64, mutator func(*persistedMonitorPrefs) (bool, error)) (int64, error) {
	if p == nil {
		return 0, fmt.Errorf("monitor preferences are unavailable")
	}
	p.commitMu.Lock()
	defer p.commitMu.Unlock()
	p.mu.RLock()
	current := clonePersistedPrefs(p.data)
	path := p.path
	p.mu.RUnlock()
	if expected != nil && current.Revision != *expected {
		return current.Revision, &revisionConflictError{Expected: *expected, Actual: current.Revision}
	}

	changed, err := mutator(&current)
	if err != nil {
		return current.Revision, err
	}
	if !changed {
		return current.Revision, nil
	}
	normalizePersistedPrefs(&current)
	current.Revision++
	if path != "" {
		raw, err := json.Marshal(current)
		if err != nil {
			return p.revision(), fmt.Errorf("encode monitor preferences: %w", err)
		}
		if err := writeFileAtomic(path, raw, 0o600); err != nil {
			return p.revision(), &preferencePersistenceError{Err: err}
		}
	}
	p.mu.Lock()
	p.data = current
	p.loaded = true
	p.mu.Unlock()
	return current.Revision, nil
}

func newAccountID() (string, error) {
	raw := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", err
	}
	return "acct_" + base64.RawURLEncoding.EncodeToString(raw), nil
}

func loadOrCreateKey(path string) ([]byte, error) {
	key, err := loadKey(path)
	if err == nil {
		return key, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	key = make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(path, key, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

func loadKey(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("monitor key must contain 32 bytes")
	}
	return raw, nil
}

func encryptPreference(key []byte, aad, value string) (string, error) {
	if len(key) != 32 {
		return "", fmt.Errorf("monitor encryption key is unavailable")
	}
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

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
