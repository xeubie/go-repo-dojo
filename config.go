package gitgonano

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

type AddConfigInput struct {
	Name  string
	Value string
}

type configVariable struct {
	name  string
	value string
}

type configSection struct {
	name      string
	variables []configVariable
}

// Config represents a parsed git config file.
type Config struct {
	sections []configSection
}

func LoadConfig(repoDir string) (*Config, error) {
	configPath := filepath.Join(repoDir, "config")
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, err
	}
	return parseConfig(string(data))
}

func parseConfig(content string) (*Config, error) {
	config := &Config{}
	lines := strings.Split(content, "\n")

	var currentSection *configSection

	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		tokens, tokenKinds := tokenizeLine(line)

		if len(tokenKinds) == 0 {
			continue
		}

		parsed := parseLine(tokens, tokenKinds)
		switch parsed.kind {
		case parsedEmpty:
			continue
		case parsedSectionHeader:
			if currentSection != nil {
				config.sections = append(config.sections, *currentSection)
			}
			currentSection = &configSection{name: parsed.sectionName}
		case parsedVariable:
			if currentSection != nil {
				currentSection.variables = append(currentSection.variables, configVariable{
					name:  parsed.varName,
					value: parsed.varValue,
				})
			}
		case parsedInvalid:
			// skip invalid lines
		}
	}

	if currentSection != nil {
		config.sections = append(config.sections, *currentSection)
	}

	return config, nil
}

type tokenKind int

const (
	tokenWhitespace tokenKind = iota
	tokenComment
	tokenOpenBracket
	tokenCloseBracket
	tokenEquals
	tokenQuote
	tokenSymbol
)

func classifyChar(r rune) tokenKind {
	switch r {
	case ' ', '\t':
		return tokenWhitespace
	case '#':
		return tokenComment
	case '[':
		return tokenOpenBracket
	case ']':
		return tokenCloseBracket
	case '=':
		return tokenEquals
	case '"':
		return tokenQuote
	default:
		return tokenSymbol
	}
}

func tokenizeLine(line string) ([]string, []tokenKind) {
	var tokens []string
	var kinds []tokenKind

	type currentTokenState struct {
		kind  tokenKind
		start int
	}
	var current *currentTokenState

	i := 0
	for i < len(line) {
		r, size := utf8.DecodeRuneInString(line[i:])
		charKind := classifyChar(r)

		if current != nil {
			if current.kind == tokenQuote && r == '\\' {
				// escape: skip next character
				if i+size < len(line) {
					_, nextSize := utf8.DecodeRuneInString(line[i+size:])
					i += size + nextSize
				} else {
					i += size
				}
				continue
			} else if current.kind == tokenQuote && charKind == tokenQuote {
				// end of quote
				tokens = append(tokens, line[current.start:i+size])
				kinds = append(kinds, current.kind)
				current = nil
				i += size
				continue
			} else if current.kind == charKind || current.kind == tokenComment || current.kind == tokenQuote {
				i += size
				continue
			} else {
				switch current.kind {
				case tokenWhitespace, tokenComment:
					// don't save
				default:
					tokens = append(tokens, line[current.start:i])
					kinds = append(kinds, current.kind)
				}
			}
		}

		current = &currentTokenState{kind: charKind, start: i}
		i += size
	}

	if current != nil {
		switch current.kind {
		case tokenWhitespace, tokenComment:
			// don't save
		default:
			tokens = append(tokens, line[current.start:])
			kinds = append(kinds, current.kind)
		}
	}

	return tokens, kinds
}

type parsedLineKind int

const (
	parsedEmpty parsedLineKind = iota
	parsedSectionHeader
	parsedVariable
	parsedInvalid
)

type parsedLine struct {
	kind        parsedLineKind
	sectionName string
	varName     string
	varValue    string
}

func parseLine(tokens []string, kinds []tokenKind) parsedLine {
	if len(kinds) == 0 {
		return parsedLine{kind: parsedEmpty}
	}

	// [section]
	if len(kinds) == 3 &&
		kinds[0] == tokenOpenBracket &&
		kinds[1] == tokenSymbol &&
		kinds[2] == tokenCloseBracket {
		return parsedLine{kind: parsedSectionHeader, sectionName: tokens[1]}
	}

	// [section "subsection"]
	if len(kinds) == 4 &&
		kinds[0] == tokenOpenBracket &&
		kinds[1] == tokenSymbol &&
		kinds[2] == tokenQuote &&
		kinds[3] == tokenCloseBracket {
		quotedStr := tokens[2]
		subsection := unescapeConfigStr(quotedStr[1 : len(quotedStr)-1])
		return parsedLine{
			kind:        parsedSectionHeader,
			sectionName: tokens[1] + "." + subsection,
		}
	}

	// variable = value (value may have multiple tokens)
	if len(kinds) >= 3 && kinds[0] == tokenSymbol && kinds[1] == tokenEquals {
		value := strings.Join(tokens[2:], " ")
		return parsedLine{kind: parsedVariable, varName: tokens[0], varValue: value}
	}

	return parsedLine{kind: parsedInvalid}
}

func unescapeConfigStr(s string) string {
	var result strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			next := s[i+1]
			switch next {
			case '"', '\'':
				result.WriteByte(next)
			case 'n':
				result.WriteByte('\n')
			case 'r':
				result.WriteByte('\r')
			case 't':
				result.WriteByte('\t')
			default:
				result.WriteByte('\\')
				result.WriteByte(next)
			}
			i++
		} else {
			result.WriteByte(s[i])
		}
	}
	return result.String()
}

func escapeConfigStr(s string) string {
	var result strings.Builder
	for _, c := range s {
		switch c {
		case '"', '\'':
			result.WriteByte('\\')
			result.WriteRune(c)
		case '\n':
			result.WriteString("\\n")
		case '\r':
			result.WriteString("\\r")
		case '\t':
			result.WriteString("\\t")
		default:
			result.WriteRune(c)
		}
	}
	return result.String()
}

// GetSection returns the variables for a section, or nil if not found.
func (c *Config) GetSection(name string) map[string]string {
	for _, s := range c.sections {
		if s.name == name {
			vars := make(map[string]string)
			for _, v := range s.variables {
				vars[v.name] = v.value
			}
			return vars
		}
	}
	return nil
}

// Add adds or updates a config entry.
func (c *Config) Add(input AddConfigInput) error {
	lastDot := strings.LastIndex(input.Name, ".")
	if lastDot < 0 {
		return fmt.Errorf("key does not contain a section")
	}

	sectionNameOrig := input.Name[:lastDot]
	var subsectionName string
	firstDot := strings.Index(sectionNameOrig, ".")
	if firstDot >= 0 {
		subsectionName = sectionNameOrig[firstDot+1:]
		sectionNameOrig = sectionNameOrig[:firstDot]
	}
	varNameOrig := input.Name[lastDot+1:]

	// validate section and var names
	for _, name := range []string{sectionNameOrig, varNameOrig} {
		if len(name) == 0 {
			return fmt.Errorf("invalid config name")
		}
		for _, ch := range name {
			if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
				(ch >= '0' && ch <= '9') || ch == '-') {
				return fmt.Errorf("invalid config name")
			}
		}
	}

	sectionName := strings.ToLower(sectionNameOrig)
	if subsectionName != "" {
		sectionName = sectionName + "." + subsectionName
	}
	varName := strings.ToLower(varNameOrig)

	// find or create section
	found := false
	for i := range c.sections {
		if c.sections[i].name == sectionName {
			varFound := false
			for j := range c.sections[i].variables {
				if c.sections[i].variables[j].name == varName {
					c.sections[i].variables[j].value = input.Value
					varFound = true
					break
				}
			}
			if !varFound {
				c.sections[i].variables = append(c.sections[i].variables,
					configVariable{name: varName, value: input.Value})
			}
			found = true
			break
		}
	}
	if !found {
		c.sections = append(c.sections, configSection{
			name:      sectionName,
			variables: []configVariable{{name: varName, value: input.Value}},
		})
	}

	return nil
}

// Write writes the config to a file (typically a lock file).
func (c *Config) Write(f *os.File) error {
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}

	for _, section := range c.sections {
		if dotIdx := strings.Index(section.name, "."); dotIdx >= 0 {
			mainName := section.name[:dotIdx]
			subName := escapeConfigStr(section.name[dotIdx+1:])
			fmt.Fprintf(f, "[%s \"%s\"]\n", mainName, subName)
		} else {
			fmt.Fprintf(f, "[%s]\n", section.name)
		}

		for _, v := range section.variables {
			fmt.Fprintf(f, "\t%s = %s\n", v.name, v.value)
		}
	}

	return nil
}
