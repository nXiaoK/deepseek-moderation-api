package audit

import (
	"encoding/json"
	"slices"
	"time"
)

type storedOutputs struct {
	Final    string   `json:"final"`
	Attempts []string `json:"attempts"`
}

func (s *Store) prepareModelOutput(log *AuditLog, days int) ([]byte, *time.Time, error) {
	value := storedOutputs{Final: log.ModelOutput, Attempts: make([]string, len(log.Attempts))}
	log.Attempts = slices.Clone(log.Attempts)
	hasOutput := log.ModelOutput != ""
	log.ModelOutput = ""
	for i := range log.Attempts {
		value.Attempts[i] = log.Attempts[i].ModelOutput
		hasOutput = hasOutput || value.Attempts[i] != ""
		log.Attempts[i].ModelOutput = ""
	}
	log.ModelOutputStored = log.ModelOutputStored && hasOutput
	if !log.ModelOutputStored {
		return nil, nil, nil
	}
	retention := log.ModelOutputRetentionDays
	if retention <= 0 {
		retention = days
	}
	expires := time.Now().Add(time.Duration(min(retention, days)) * 24 * time.Hour)
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, nil, err
	}
	return s.Vault.Seal(string(raw), "model-output:"+log.ID), &expires, nil
}
func (s *Store) restoreModelOutput(log *AuditLog, encrypted []byte) error {
	if len(encrypted) > 0 {
		raw, err := s.Vault.Open(encrypted, "model-output:"+log.ID)
		if err != nil {
			return err
		}
		var value storedOutputs
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return err
		}
		log.ModelOutput = value.Final
		for i := range log.Attempts {
			if i < len(value.Attempts) {
				log.Attempts[i].ModelOutput = value.Attempts[i]
			}
		}
	}
	log.ModelOutputStored = log.ModelOutput != ""
	for _, attempt := range log.Attempts {
		log.ModelOutputStored = log.ModelOutputStored || attempt.ModelOutput != ""
	}
	return nil
}
