package resource

import (
	"reflect"
	"strings"
	"testing"
)

func TestValidateLabels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		labels  map[string]string
		wantErr bool
	}{
		{
			name: "valid labels",
			labels: map[string]string{
				"team": "network",
			},
		},
		{
			name: "max length key and value are allowed",
			labels: map[string]string{
				strings.Repeat("z", 255): strings.Repeat("z", 255),
			},
		},
		{
			name: "empty value is allowed",
			labels: map[string]string{
				"owner": "",
			},
		},
		{
			name: "empty key is rejected",
			labels: map[string]string{
				"": "ops",
			},
			wantErr: true,
		},
		{
			name: "invalid key",
			labels: map[string]string{
				"team=name": "network",
			},
			wantErr: true,
		},
		{
			name: "invalid value",
			labels: map[string]string{
				"team": "net=work",
			},
			wantErr: true,
		},
		{
			name: "too long key is rejected",
			labels: map[string]string{
				strings.Repeat("z", 256): "ops",
			},
			wantErr: true,
		},
		{
			name: "too long value is rejected",
			labels: map[string]string{
				"team": strings.Repeat("z", 256),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateLabels(tt.labels)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateLabels() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestFormatAndParseLabelSelectors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		labelSelectors  []LabelSelector
		selectorToParse string
		wantFormatted   string
		wantParsed      []LabelSelector
	}{
		{
			name: "with values",
			labelSelectors: []LabelSelector{
				{
					Key:      "test",
					Operator: LabelSelectorOperatorIn,
					Values:   []string{"true", "shadow"},
				},
				{
					Key:      "team",
					Operator: LabelSelectorOperatorIn,
					Values:   []string{"network"},
				},
			},
			wantFormatted: "test in (true,shadow),team in (network)",
			wantParsed: []LabelSelector{
				{
					Key:      "test",
					Operator: LabelSelectorOperatorIn,
					Values:   []string{"true", "shadow"},
				},
				{
					Key:      "team",
					Operator: LabelSelectorOperatorIn,
					Values:   []string{"network"},
				},
			},
		},
		{
			name: "with empty values",
			labelSelectors: []LabelSelector{
				{
					Key:      "team",
					Operator: LabelSelectorOperatorIn,
					Values:   []string{},
				},
			},
			wantFormatted: `team in ("")`,
			wantParsed: []LabelSelector{
				{
					Key:      "team",
					Operator: LabelSelectorOperatorIn,
					Values:   []string{""},
				},
			},
		},
		{
			name: "with nil values",
			labelSelectors: []LabelSelector{
				{
					Key:      "owner",
					Operator: LabelSelectorOperatorIn,
					Values:   nil,
				},
			},
			wantFormatted: `owner in ("")`,
			wantParsed: []LabelSelector{
				{
					Key:      "owner",
					Operator: LabelSelectorOperatorIn,
					Values:   []string{""},
				},
			},
		},
		{
			name: "with empty string value",
			labelSelectors: []LabelSelector{
				{
					Key:      "owner",
					Operator: LabelSelectorOperatorIn,
					Values:   []string{"", "data"},
				},
			},
			wantFormatted: `owner in ("",data)`,
			wantParsed: []LabelSelector{
				{
					Key:      "owner",
					Operator: LabelSelectorOperatorIn,
					Values:   []string{"", "data"},
				},
			},
		},
		{
			name: "with several values",
			labelSelectors: []LabelSelector{
				{
					Key:      "team",
					Operator: LabelSelectorOperatorIn,
					Values:   []string{"network", "shadow"},
				},
			},
			wantFormatted: "team in (network,shadow)",
			wantParsed: []LabelSelector{
				{
					Key:      "team",
					Operator: LabelSelectorOperatorIn,
					Values:   []string{"network", "shadow"},
				},
			},
		},
		{
			name: "with one value",
			labelSelectors: []LabelSelector{
				{
					Key:      "team",
					Operator: LabelSelectorOperatorIn,
					Values:   []string{"network"},
				},
			},
			wantFormatted: "team in (network)",
			wantParsed: []LabelSelector{
				{
					Key:      "team",
					Operator: LabelSelectorOperatorIn,
					Values:   []string{"network"},
				},
			},
		},
		{
			name: "with two label selectors value",
			labelSelectors: []LabelSelector{
				{
					Key:      "team",
					Operator: LabelSelectorOperatorIn,
					Values:   []string{"network"},
				},
				{
					Key:      "data",
					Operator: LabelSelectorOperatorIn,
					Values:   []string{"network", "shadow"},
				},
			},
			wantFormatted: "team in (network),data in (network,shadow)",
			wantParsed: []LabelSelector{
				{
					Key:      "team",
					Operator: LabelSelectorOperatorIn,
					Values:   []string{"network"},
				},
				{
					Key:      "data",
					Operator: LabelSelectorOperatorIn,
					Values:   []string{"network", "shadow"},
				},
			},
		},
		{
			name:            "parse legacy empty values format",
			labelSelectors:  []LabelSelector{{Key: "team", Operator: LabelSelectorOperatorIn, Values: []string{""}}},
			selectorToParse: "team in ()",
			wantFormatted:   `team in ("")`,
			wantParsed: []LabelSelector{
				{
					Key:      "team",
					Operator: LabelSelectorOperatorIn,
					Values:   []string{""},
				},
			},
		},
		{
			name: "values with parentheses are quoted and round-trip",
			labelSelectors: []LabelSelector{
				{
					Key:      "team",
					Operator: LabelSelectorOperatorIn,
					Values:   []string{"(", "shadow"},
				},
			},
			wantFormatted: `team in ("(",shadow)`,
			wantParsed: []LabelSelector{
				{
					Key:      "team",
					Operator: LabelSelectorOperatorIn,
					Values:   []string{"(", "shadow"},
				},
			},
		},
		{
			name: "operator keywords can be values",
			labelSelectors: []LabelSelector{
				{
					Key:      "test",
					Operator: LabelSelectorOperatorIn,
					Values:   []string{"in", "notin"},
				},
			},
			wantFormatted: `test in (in,notin)`,
			wantParsed: []LabelSelector{
				{
					Key:      "test",
					Operator: LabelSelectorOperatorIn,
					Values:   []string{"in", "notin"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			formattedSelector, err := FormatLabelSelectors(tt.labelSelectors)
			if err != nil {
				t.Fatalf("FormatLabelSelectors() error = %v", err)
			}

			if formattedSelector != tt.wantFormatted {
				t.Fatalf("FormatLabelSelectors() = %q, want %q", formattedSelector, tt.wantFormatted)
			}

			selectorToParse := formattedSelector
			if tt.selectorToParse != "" {
				selectorToParse = tt.selectorToParse
			}
			parsedLabelSelectors, err := ParseLabelSelectors(selectorToParse)
			if err != nil {
				t.Fatalf("ParseLabelSelectors() error = %v", err)
			}

			if !reflect.DeepEqual(parsedLabelSelectors, tt.wantParsed) {
				t.Fatalf("ParseLabelSelectors() = %#v, want %#v", parsedLabelSelectors, tt.wantParsed)
			}
		})
	}
}

func TestValidateLabelSelectorsRejectsUnsupportedOperator(t *testing.T) {
	t.Parallel()

	err := ValidateLabelSelectors([]LabelSelector{
		{
			Key:      "team",
			Operator: "NotIn",
			Values:   []string{"network"},
		},
	})
	if err == nil {
		t.Fatal("ValidateLabelSelectors() expected error for unsupported operator")
	}
}

func TestValidateLabelSelectorsAllowsEmptyValueItem(t *testing.T) {
	t.Parallel()

	err := ValidateLabelSelectors([]LabelSelector{
		{
			Key:      "team",
			Operator: LabelSelectorOperatorIn,
			Values:   []string{""},
		},
	})
	if err != nil {
		t.Fatalf("ValidateLabelSelectors() error = %v", err)
	}
}
