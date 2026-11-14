package policy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

// Schema é o subconjunto de JSON Schema que as ferramentas usam: objetos com
// propriedades tipadas, required, additionalProperties, enum e limites.
type Schema struct {
	Type                 string             `json:"type"`
	Description          string             `json:"description,omitempty"`
	Properties           map[string]*Schema `json:"properties,omitempty"`
	Required             []string           `json:"required,omitempty"`
	AdditionalProperties *bool              `json:"additionalProperties,omitempty"`
	Enum                 []any              `json:"enum,omitempty"`
	MinLength            *int               `json:"minLength,omitempty"`
	MaxLength            *int               `json:"maxLength,omitempty"`
	Minimum              *float64           `json:"minimum,omitempty"`
	Maximum              *float64           `json:"maximum,omitempty"`
	Items                *Schema            `json:"items,omitempty"`
	MaxItems             *int               `json:"maxItems,omitempty"`
}

// ValidateArgs confere os argumentos contra o schema da ferramenta. A saída
// do modelo é não confiável: nada roda antes de passar por aqui.
func ValidateArgs(schemaJSON, args json.RawMessage) error {
	var s Schema
	if err := json.Unmarshal(schemaJSON, &s); err != nil {
		return fmt.Errorf("schema da ferramenta inválido: %w", err)
	}
	if len(bytes.TrimSpace(args)) == 0 {
		args = json.RawMessage(`{}`)
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return fmt.Errorf("argumentos não são JSON válido: %w", err)
	}
	if dec.More() {
		return errors.New("argumentos com conteúdo extra depois do JSON")
	}
	return s.validate("args", v)
}

func (s *Schema) validate(path string, v any) error {
	if len(s.Enum) > 0 && !inEnum(s.Enum, v) {
		return fmt.Errorf("%s: valor fora das opções permitidas", path)
	}
	switch s.Type {
	case "object":
		obj, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: esperado objeto", path)
		}
		for _, name := range s.Required {
			if _, ok := obj[name]; !ok {
				return fmt.Errorf("%s.%s: obrigatório", path, name)
			}
		}
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			sub, ok := s.Properties[k]
			if !ok {
				if s.AdditionalProperties != nil && !*s.AdditionalProperties {
					return fmt.Errorf("%s.%s: campo não permitido", path, k)
				}
				continue
			}
			if err := sub.validate(path+"."+k, obj[k]); err != nil {
				return err
			}
		}
	case "string":
		str, ok := v.(string)
		if !ok {
			return fmt.Errorf("%s: esperado texto", path)
		}
		n := utf8.RuneCountInString(str)
		if s.MinLength != nil && n < *s.MinLength {
			return fmt.Errorf("%s: texto curto demais (mínimo %d)", path, *s.MinLength)
		}
		if s.MaxLength != nil && n > *s.MaxLength {
			return fmt.Errorf("%s: texto longo demais (máximo %d)", path, *s.MaxLength)
		}
	case "integer", "number":
		num, ok := v.(json.Number)
		if !ok {
			return fmt.Errorf("%s: esperado número", path)
		}
		f, err := num.Float64()
		if err != nil {
			return fmt.Errorf("%s: número inválido", path)
		}
		if s.Type == "integer" && (strings.ContainsAny(num.String(), ".eE") && f != math.Trunc(f)) {
			return fmt.Errorf("%s: esperado número inteiro", path)
		}
		if s.Minimum != nil && f < *s.Minimum {
			return fmt.Errorf("%s: menor que %v", path, *s.Minimum)
		}
		if s.Maximum != nil && f > *s.Maximum {
			return fmt.Errorf("%s: maior que %v", path, *s.Maximum)
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("%s: esperado true ou false", path)
		}
	case "array":
		arr, ok := v.([]any)
		if !ok {
			return fmt.Errorf("%s: esperado lista", path)
		}
		if s.MaxItems != nil && len(arr) > *s.MaxItems {
			return fmt.Errorf("%s: itens demais (máximo %d)", path, *s.MaxItems)
		}
		if s.Items != nil {
			for i, item := range arr {
				if err := s.Items.validate(fmt.Sprintf("%s[%d]", path, i), item); err != nil {
					return err
				}
			}
		}
	case "":
		// sem tipo: aceita qualquer valor
	default:
		return fmt.Errorf("%s: tipo de schema não suportado %q", path, s.Type)
	}
	return nil
}

func inEnum(enum []any, v any) bool {
	for _, e := range enum {
		if fmt.Sprint(e) == fmt.Sprint(v) {
			return true
		}
	}
	return false
}
