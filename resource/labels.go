package resource

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type LabelSelectorOperator string

const LabelSelectorOperatorIn LabelSelectorOperator = "In"

// Extending label selector support
//
// Today this package supports only the "in" operator end-to-end:
//   - formatting: FormatLabelSelectors() prints "<key> in (<values>)"
//   - parsing:    ParseLabelSelectors() accepts only "in" requirements
//
// When you want to add a new operator (e.g. NotIn, Equals, NotEquals), keep
// formatter, parser and validation in sync. Suggested sequence:
//
//  1. Public API: add a new LabelSelectorOperator constant.
//  2. Validation: add the operator to allowedOperators and extend
//     ValidateLabelSelectors() with operator-specific rules (e.g. equals expects
//     exactly one value, set-based ops expect a value-set, etc).
//  3. Formatting: extend formatLabelSelectorRequirement() to render the new
//     operator (and update value formatting rules if needed).
//  4. Parsing: extend the kube-style lexer/parser below:
//     - add tokens (keyword / symbol) to labelSelectorToken
//     - update labelSelectorString2Token and/or scanIdentifierOrKeyword()
//     - update parseOperator() mapping token -> LabelSelectorOperator
//     - update parseValues() for operator-specific value parsing
//     Note: lookahead/consume() uses a Values context that treats "in" as an
//     identifier when it appears inside a value list; if you introduce more
//     keyword-operators, consider whether they should behave the same way.
//  5. Tests: add round-trip tests (Format -> Parse) and Parse-only tests for
//     legacy inputs and edge cases (empty string values, quoted values with
//     commas/parentheses/spaces, keyword-values like "in").
//  6. Consumers: ensure downstream code can execute the new semantics:
//     - state-manager SQL builder currently assumes IN for all selectors
//     and must be updated before enabling new operators.
var allowedOperators = labelSelectorOperators{LabelSelectorOperatorIn}

type labelSelectorOperators []LabelSelectorOperator

func (l labelSelectorOperators) String() string {
	var mapped []string

	for _, op := range l {
		mapped = append(mapped, string(op))
	}

	return strings.Join(mapped, ",")
}

const maxLabelPartRunes = 255

// ValidateLabels validates resource labels:
// keys must be non-empty and neither keys nor values may contain "=".
func ValidateLabels(labels map[string]string) error {
	if len(labels) == 0 {
		return nil
	}

	for key, value := range labels {
		if err := validateLabelPartKey(key); err != nil {
			return fmt.Errorf("invalid label key %q: %w", key, err)
		}

		if err := validateLabelPartValue(value); err != nil {
			return fmt.Errorf("invalid label value for %q: %w", key, err)
		}
	}

	return nil
}

type LabelSelector struct {
	Key      string
	Operator LabelSelectorOperator
	Values   []string
}

// ValidateLabelSelectors validates IN-only label selector requirements.
func ValidateLabelSelectors(labelSelectors []LabelSelector) error {
	for i, labelSelector := range labelSelectors {
		if err := validateLabelPartKey(labelSelector.Key); err != nil {
			return fmt.Errorf("invalid label selector requirement %d key %q: %w", i, labelSelector.Key, err)
		}

		if !slices.Contains(allowedOperators, labelSelector.Operator) {
			return fmt.Errorf(
				"invalid label selector requirement %d operator %q: only [%s] is supported",
				i,
				labelSelector.Operator,
				allowedOperators.String(),
			)
		}

		for valueIndex, value := range labelSelector.Values {
			if err := validateLabelPartValue(value); err != nil {
				return fmt.Errorf(
					"invalid label selector requirement %d value %d %q: %w",
					i,
					valueIndex,
					value,
					err,
				)
			}
		}
	}

	return nil
}

// FormatLabelSelectors converts IN-only requirements into a selector string.
func FormatLabelSelectors(labelSelectors []LabelSelector) (string, error) {
	if len(labelSelectors) == 0 {
		return "", nil
	}
	if err := ValidateLabelSelectors(labelSelectors); err != nil {
		return "", err
	}

	formattedSelectors := make([]string, 0, len(labelSelectors))
	for _, labelSelector := range labelSelectors {
		formattedSelector, err := formatLabelSelectorRequirement(labelSelector)
		if err != nil {
			return "", err
		}
		formattedSelectors = append(formattedSelectors, formattedSelector)
	}

	return strings.Join(formattedSelectors, ","), nil
}

// ParseLabelSelectors parses a selector string into IN-only requirements.
func ParseLabelSelectors(selector string) ([]LabelSelector, error) {
	if selector == "" {
		return nil, nil
	}

	labelSelectors, err := parseLabelSelectorQuery(selector)
	if err != nil {
		return nil, err
	}

	if err := ValidateLabelSelectors(labelSelectors); err != nil {
		return nil, err
	}

	return labelSelectors, nil
}

func normalizeEmptyLabelSelectorValues(values []string) []string {
	if len(values) == 0 {
		return []string{""}
	}

	return values
}

func formatLabelSelectorValue(value string) string {
	if value == "" || strings.ContainsAny(value, `,()"\`) || strings.IndexFunc(value, unicode.IsSpace) != -1 {
		return strconv.Quote(value)
	}

	return value
}

func formatLabelSelectorRequirement(labelSelector LabelSelector) (string, error) {
	switch labelSelector.Operator {
	case LabelSelectorOperatorIn:
		values := normalizeEmptyLabelSelectorValues(labelSelector.Values)
		formattedValues := make([]string, 0, len(values))
		for _, value := range values {
			formattedValues = append(formattedValues, formatLabelSelectorValue(value))
		}
		return fmt.Sprintf("%s in (%s)", labelSelector.Key, strings.Join(formattedValues, ",")), nil
	default:
		return "", fmt.Errorf("unsupported label selector operator %q", labelSelector.Operator)
	}
}

func validateLabelPartKey(value string) error {
	if value == "" {
		return fmt.Errorf("key must not be empty")
	}

	if utf8.RuneCountInString(value) > maxLabelPartRunes {
		return fmt.Errorf("key must not exceed %d characters", maxLabelPartRunes)
	}

	if strings.Contains(value, "=") {
		return fmt.Errorf("key must not contain '='")
	}
	return nil
}

func validateLabelPartValue(value string) error {
	if utf8.RuneCountInString(value) > maxLabelPartRunes {
		return fmt.Errorf("value must not exceed %d characters", maxLabelPartRunes)
	}

	if strings.Contains(value, "=") {
		return fmt.Errorf("value must not contain '='")
	}
	return nil
}

type labelSelectorToken int

const (
	labelSelectorErrorToken labelSelectorToken = iota
	labelSelectorEndOfStringToken
	labelSelectorIdentifierToken
	labelSelectorStringToken
	labelSelectorInToken
	labelSelectorOpenParToken
	labelSelectorClosedParToken
	labelSelectorCommaToken
)

type labelSelectorScannedItem struct {
	tok     labelSelectorToken
	literal string
}

var labelSelectorString2Token = map[string]labelSelectorToken{
	")": labelSelectorClosedParToken,
	",": labelSelectorCommaToken,
	"(": labelSelectorOpenParToken,
}

func labelSelectorIsWhitespace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n'
}

func labelSelectorIsSpecialSymbol(ch byte) bool {
	switch ch {
	case '=', '!', '(', ')', ',':
		return true
	default:
		return false
	}
}

type labelSelectorLexer struct {
	s   string
	pos int
}

func (l *labelSelectorLexer) read() (b byte) {
	b = 0
	if l.pos < len(l.s) {
		b = l.s[l.pos]
		l.pos++
	}
	return b
}

func (l *labelSelectorLexer) unread() {
	l.pos--
}

func (l *labelSelectorLexer) skipWhiteSpaces(ch byte) byte {
	for {
		if !labelSelectorIsWhitespace(ch) {
			return ch
		}
		ch = l.read()
	}
}

func (l *labelSelectorLexer) scanIdentifierOrKeyword() (tok labelSelectorToken, lit string) {
	var buffer []byte
IdentifierLoop:
	for {
		switch ch := l.read(); {
		case ch == 0:
			break IdentifierLoop
		case ch == '"' || labelSelectorIsSpecialSymbol(ch) || labelSelectorIsWhitespace(ch):
			l.unread()
			break IdentifierLoop
		default:
			buffer = append(buffer, ch)
		}
	}

	raw := string(buffer)
	switch strings.ToLower(raw) {
	case "in":
		return labelSelectorInToken, raw
	default:
		return labelSelectorIdentifierToken, raw
	}
}

func (l *labelSelectorLexer) scanSpecialSymbol() (labelSelectorToken, string) {
	lastScannedItem := labelSelectorScannedItem{}
	var buffer []byte
SpecialSymbolLoop:
	for {
		switch ch := l.read(); {
		case ch == 0:
			break SpecialSymbolLoop
		case labelSelectorIsSpecialSymbol(ch):
			buffer = append(buffer, ch)
			if token, ok := labelSelectorString2Token[string(buffer)]; ok {
				lastScannedItem = labelSelectorScannedItem{tok: token, literal: string(buffer)}
			} else if lastScannedItem.tok != 0 {
				l.unread()
				break SpecialSymbolLoop
			}
		default:
			l.unread()
			break SpecialSymbolLoop
		}
	}

	if lastScannedItem.tok == 0 {
		return labelSelectorErrorToken, fmt.Sprintf("unexpected token %q", string(buffer))
	}

	return lastScannedItem.tok, lastScannedItem.literal
}

func (l *labelSelectorLexer) scanQuotedString() (labelSelectorToken, string) {
	var buffer []byte
	start := l.read()
	if start != '"' {
		return labelSelectorErrorToken, "internal error: expected '\"'"
	}
	buffer = append(buffer, start)

	escaped := false
	for {
		ch := l.read()
		if ch == 0 {
			return labelSelectorErrorToken, "unterminated quoted value"
		}

		buffer = append(buffer, ch)
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if ch == '"' {
			return labelSelectorStringToken, string(buffer)
		}
	}
}

func (l *labelSelectorLexer) Lex() (tok labelSelectorToken, lit string) {
	switch ch := l.skipWhiteSpaces(l.read()); {
	case ch == 0:
		return labelSelectorEndOfStringToken, ""
	case ch == '"':
		l.unread()
		return l.scanQuotedString()
	case labelSelectorIsSpecialSymbol(ch):
		l.unread()
		return l.scanSpecialSymbol()
	default:
		l.unread()
		return l.scanIdentifierOrKeyword()
	}
}

type labelSelectorParserContext int

const (
	labelSelectorKeyAndOperator labelSelectorParserContext = iota
	labelSelectorValues
)

type labelSelectorParser struct {
	l            *labelSelectorLexer
	scannedItems []labelSelectorScannedItem
	position     int
	selector     string
}

func (p *labelSelectorParser) scan() {
	for {
		token, literal := p.l.Lex()
		p.scannedItems = append(p.scannedItems, labelSelectorScannedItem{token, literal})
		if token == labelSelectorEndOfStringToken {
			break
		}
	}
}

func (p *labelSelectorParser) lookahead(context labelSelectorParserContext) (labelSelectorToken, string) {
	tok, lit := p.scannedItems[p.position].tok, p.scannedItems[p.position].literal
	if context == labelSelectorValues {
		switch tok {
		case labelSelectorInToken:
			tok = labelSelectorIdentifierToken
		}
	}
	return tok, lit
}

func (p *labelSelectorParser) consume(context labelSelectorParserContext) (labelSelectorToken, string) {
	p.position++
	tok, lit := p.scannedItems[p.position-1].tok, p.scannedItems[p.position-1].literal
	if context == labelSelectorValues {
		switch tok {
		case labelSelectorInToken:
			tok = labelSelectorIdentifierToken
		}
	}
	return tok, lit
}

func parseLabelSelectorQuery(selector string) ([]LabelSelector, error) {
	p := &labelSelectorParser{
		l:        &labelSelectorLexer{s: selector, pos: 0},
		selector: selector,
	}
	return p.parse()
}

func (p *labelSelectorParser) parse() ([]LabelSelector, error) {
	p.scan()

	labelSelectors := make([]LabelSelector, 0, 1)
	requirementIndex := 0

	for {
		tok, lit := p.lookahead(labelSelectorValues)
		switch tok {
		case labelSelectorEndOfStringToken:
			if requirementIndex == 0 && strings.TrimSpace(p.selector) == "" {
				return nil, fmt.Errorf("empty label selector requirement in %q", p.selector)
			}
			return labelSelectors, nil
		case labelSelectorCommaToken:
			return nil, fmt.Errorf("empty label selector requirement in %q", p.selector)
		case labelSelectorErrorToken:
			return nil, fmt.Errorf("invalid label selector %q: %s", p.selector, lit)
		default:
			labelSelector, err := p.parseRequirement()
			if err != nil {
				return nil, fmt.Errorf("invalid label selector requirement %d %s: %w", requirementIndex, p.selector, err)
			}
			labelSelectors = append(labelSelectors, labelSelector)
			requirementIndex++

			tok, lit = p.lookahead(labelSelectorValues)
			switch tok {
			case labelSelectorEndOfStringToken:
				return labelSelectors, nil
			case labelSelectorCommaToken:
				p.consume(labelSelectorValues)
				tok2, _ := p.lookahead(labelSelectorValues)
				if tok2 == labelSelectorEndOfStringToken || tok2 == labelSelectorCommaToken {
					return nil, fmt.Errorf("empty label selector requirement in %q", p.selector)
				}
				continue
			case labelSelectorErrorToken:
				return nil, fmt.Errorf("invalid label selector %q: %s", p.selector, lit)
			default:
				return nil, fmt.Errorf("found %q, expected ',' or end of string", lit)
			}
		}
	}
}

func (p *labelSelectorParser) parseRequirement() (LabelSelector, error) {
	key, err := p.parseKey()
	if err != nil {
		return LabelSelector{}, err
	}

	operator, err := p.parseOperator()
	if err != nil {
		return LabelSelector{}, err
	}

	values, err := p.parseValues(operator)
	if err != nil {
		return LabelSelector{}, err
	}

	return LabelSelector{
		Key:      key,
		Operator: operator,
		Values:   values,
	}, nil
}

func (p *labelSelectorParser) parseKey() (string, error) {
	tok, lit := p.consume(labelSelectorValues)
	switch tok {
	case labelSelectorIdentifierToken:
		if lit == "" {
			return "", fmt.Errorf("key is required")
		}
		return lit, nil
	case labelSelectorErrorToken:
		return "", fmt.Errorf("%s", lit)
	default:
		if tok == labelSelectorEndOfStringToken || tok == labelSelectorCommaToken {
			return "", fmt.Errorf("key is required")
		}
		return "", fmt.Errorf("found %q, expected label key", lit)
	}
}

func (p *labelSelectorParser) parseOperator() (LabelSelectorOperator, error) {
	tok, lit := p.consume(labelSelectorKeyAndOperator)
	switch tok {
	case labelSelectorInToken:
		return LabelSelectorOperatorIn, nil
	case labelSelectorErrorToken:
		return "", fmt.Errorf("%s", lit)
	default:
		if tok == labelSelectorEndOfStringToken || tok == labelSelectorCommaToken {
			return "", fmt.Errorf("operator is required")
		}
		return "", fmt.Errorf("must use supported operators")
	}
}

func (p *labelSelectorParser) parseValues(operator LabelSelectorOperator) ([]string, error) {
	switch operator {
	case LabelSelectorOperatorIn:
		return p.parseValueSet()
	default:
		return nil, fmt.Errorf("unsupported operator %q", operator)
	}
}

func (p *labelSelectorParser) parseValueSet() ([]string, error) {
	tok, lit := p.consume(labelSelectorValues)
	if tok == labelSelectorErrorToken {
		return nil, fmt.Errorf("%s", lit)
	}
	if tok != labelSelectorOpenParToken {
		return nil, fmt.Errorf("found %q, expected '('", lit)
	}

	values := make([]string, 0, 1)

	for {
		tok, lit := p.lookahead(labelSelectorValues)
		switch tok {
		case labelSelectorEndOfStringToken:
			return nil, fmt.Errorf("unterminated values list, expected ')'")
		case labelSelectorErrorToken:
			return nil, fmt.Errorf("%s", lit)
		case labelSelectorClosedParToken:
			p.consume(labelSelectorValues)
			if len(values) == 0 {
				return []string{""}, nil
			}
			return values, nil
		case labelSelectorCommaToken:
			p.consume(labelSelectorValues)
			values = append(values, "")
			continue
		default:
			v, err := p.parseValue()
			if err != nil {
				return nil, err
			}
			values = append(values, v)

			tok, lit = p.lookahead(labelSelectorValues)
			switch tok {
			case labelSelectorCommaToken:
				p.consume(labelSelectorValues)
				if tok2, _ := p.lookahead(labelSelectorValues); tok2 == labelSelectorClosedParToken {
					values = append(values, "")
					p.consume(labelSelectorValues)
					return values, nil
				}
				continue
			case labelSelectorClosedParToken:
				p.consume(labelSelectorValues)
				return values, nil
			case labelSelectorEndOfStringToken:
				return nil, fmt.Errorf("unterminated values list, expected ')'")
			case labelSelectorErrorToken:
				return nil, fmt.Errorf("%s", lit)
			default:
				return nil, fmt.Errorf("found %q, expected ',' or ')'", lit)
			}
		}
	}
}

func (p *labelSelectorParser) parseValue() (string, error) {
	tok, lit := p.consume(labelSelectorValues)
	switch tok {
	case labelSelectorIdentifierToken:
		return lit, nil
	case labelSelectorStringToken:
		parsedValue, err := strconv.Unquote(lit)
		if err != nil {
			return "", fmt.Errorf("invalid quoted value %q: %w", lit, err)
		}
		return parsedValue, nil
	case labelSelectorErrorToken:
		return "", fmt.Errorf("%s", lit)
	default:
		if tok == labelSelectorEndOfStringToken || tok == labelSelectorCommaToken || tok == labelSelectorClosedParToken {
			return "", fmt.Errorf("values must not be empty")
		}
		return "", fmt.Errorf("found %q, expected label value", lit)
	}
}
