package audit

import (
	"bufio"
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"github.com/lib/pq"
	"net/http"
	"strings"
)

//go:embed evaluation_defaults.jsonl
var evaluationDefaults string

type EvaluationSample struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Input    string `json:"input,omitempty"`
	Expected string `json:"expected"`
	Note     string `json:"note"`
	Revision int64  `json:"revision"`
}

func validateEvaluationSample(sample EvaluationSample) error {
	if strings.TrimSpace(sample.Name) == "" || len(sample.Name) > 200 || len(sample.Note) > 1000 {
		return problem(400, "invalid_sample", "请输入样本名称；名称最多 200 字节，说明最多 1000 字节")
	}
	if sample.Expected != "allow" && sample.Expected != "flagged" && sample.Expected != "manual" {
		return problem(400, "invalid_sample", "预期结果必须为放行、命中或待人工判断")
	}
	_, err := validateText(sample.Input)
	return err
}
func (s *Store) EvaluationSample(ctx context.Context, id string) (EvaluationSample, error) {
	var sample EvaluationSample
	var encrypted []byte
	err := s.DB.QueryRowContext(ctx, "SELECT id,name,input_cipher,expected,note,revision FROM evaluation_samples WHERE id=$1", id).Scan(&sample.ID, &sample.Name, &encrypted, &sample.Expected, &sample.Note, &sample.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return sample, ErrNotFound
	}
	if err != nil {
		return sample, err
	}
	sample.Input, err = s.Vault.Open(encrypted, "evaluation-sample:"+id)
	return sample, err
}
func (s *Server) evaluationSamples(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.Store.DB.QueryContext(r.Context(), "SELECT id,name,expected,note,revision FROM evaluation_samples ORDER BY name,id LIMIT 1000")
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []EvaluationSample{}
	for rows.Next() {
		var item EvaluationSample
		if err := rows.Scan(&item.ID, &item.Name, &item.Expected, &item.Note, &item.Revision); err != nil {
			return err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return writeJSON(w, 200, items)
}
func (s *Server) evaluationSample(w http.ResponseWriter, r *http.Request) error {
	item, err := s.Store.EvaluationSample(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	return writeJSON(w, 200, item)
}
func (s *Server) saveEvaluationSample(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Name     string `json:"name"`
		Input    string `json:"input"`
		Expected string `json:"expected"`
		Note     string `json:"note"`
		Revision int64  `json:"expected_revision"`
	}
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	sample := EvaluationSample{ID: r.PathValue("id"), Name: in.Name, Input: in.Input, Expected: in.Expected, Note: in.Note}
	if err := validateEvaluationSample(sample); err != nil {
		return err
	}
	create := sample.ID == ""
	if create {
		sample.ID = randomToken("sample_")
	}
	err := s.Store.mutate(r.Context(), actor(r), "evaluation.sample.save", sample.ID, func(tx *sql.Tx) error {
		if create {
			if err := evaluationSampleCapacity(r.Context(), tx, []string{sample.ID}); err != nil {
				return err
			}
		}
		encrypted := s.Store.Vault.Seal(sample.Input, "evaluation-sample:"+sample.ID)
		if create {
			_, err := tx.ExecContext(r.Context(), "INSERT INTO evaluation_samples(id,name,input_cipher,expected,note) VALUES($1,$2,$3,$4,$5)", sample.ID, sample.Name, encrypted, sample.Expected, sample.Note)
			return err
		}
		res, err := tx.ExecContext(r.Context(), "UPDATE evaluation_samples SET name=$1,input_cipher=$2,expected=$3,note=$4,revision=revision+1,updated_at=NOW() WHERE id=$5 AND revision=$6", sample.Name, encrypted, sample.Expected, sample.Note, sample.ID, in.Revision)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return ErrConflict
		}
		return nil
	})
	if err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]string{"id": sample.ID})
}
func (s *Server) deleteEvaluationSample(w http.ResponseWriter, r *http.Request) error {
	err := s.Store.mutate(r.Context(), actor(r), "evaluation.sample.delete", r.PathValue("id"), func(tx *sql.Tx) error {
		res, err := tx.ExecContext(r.Context(), "DELETE FROM evaluation_samples WHERE id=$1", r.PathValue("id"))
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) importEvaluationSamples(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Builtin bool               `json:"builtin"`
		Samples []EvaluationSample `json:"samples"`
	}
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	if in.Builtin {
		if len(in.Samples) > 0 {
			return problem(400, "invalid_sample", "内置导入不能同时提供样本")
		}
		scanner := bufio.NewScanner(strings.NewReader(evaluationDefaults))
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			var row struct {
				ID, Input, Group string
				Expected         string `json:"expected_decision"`
				Scored           bool   `json:"include_in_accuracy"`
				Basis            string `json:"expected_basis"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
				return err
			}
			expected := "manual"
			if row.Scored {
				if row.Expected == "allow" {
					expected = "allow"
				} else {
					expected = "flagged"
				}
			}
			in.Samples = append(in.Samples, EvaluationSample{ID: "builtin_" + row.ID, Name: row.ID + " · " + row.Group, Input: row.Input, Expected: expected, Note: row.Basis})
		}
		if err := scanner.Err(); err != nil {
			return err
		}
	}
	if len(in.Samples) == 0 || len(in.Samples) > 100 {
		return problem(400, "invalid_sample", "每次导入 1～100 条样本")
	}
	for i := range in.Samples {
		if err := validateEvaluationSample(in.Samples[i]); err != nil {
			return err
		}
		if !in.Builtin {
			in.Samples[i].ID = randomToken("sample_")
		}
	}
	inserted := 0
	err := s.Store.mutate(r.Context(), actor(r), "evaluation.sample.import", "samples", func(tx *sql.Tx) error {
		ids := make([]string, len(in.Samples))
		for i := range in.Samples {
			ids[i] = in.Samples[i].ID
		}
		if err := evaluationSampleCapacity(r.Context(), tx, ids); err != nil {
			return err
		}
		for _, sample := range in.Samples {
			res, err := tx.ExecContext(r.Context(), "INSERT INTO evaluation_samples(id,name,input_cipher,expected,note) VALUES($1,$2,$3,$4,$5) ON CONFLICT(id) DO NOTHING", sample.ID, sample.Name, s.Store.Vault.Seal(sample.Input, "evaluation-sample:"+sample.ID), sample.Expected, sample.Note)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			inserted += int(n)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]int{"imported": inserted})
}
func evaluationSampleCapacity(ctx context.Context, tx *sql.Tx, ids []string) error {
	var count, existing int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*),COUNT(*) FILTER(WHERE id=ANY($1)) FROM evaluation_samples", pq.Array(ids)).Scan(&count, &existing); err != nil {
		return err
	}
	if count+len(ids)-existing > 1000 {
		return problem(400, "sample_limit", "样本库最多 1000 条，请先删除不再使用的样本")
	}
	return nil
}
