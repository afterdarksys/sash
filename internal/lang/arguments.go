package lang

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var argNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// Arguments is a static schema, permitted only at the start of a script.
// Values are bound before expansion, so invalid input performs no host reads.
type Arguments struct{ Fields []Argument }
type Argument struct {
	Name, Type, Help string
	Default          *string
	Choices          []string
}

func (*Arguments) stmt() {}

func (p *parser) arguments() (Stmt, error) {
	p.i++
	if p.peek().kind != kLBrace {
		return nil, fmt.Errorf("parse: args needs {")
	}
	p.i++
	p.skipSep()
	a := &Arguments{}
	seen := map[string]bool{}
	literal := func() (string, error) {
		t := p.peek()
		if t.kind != kWord || len(t.word.Parts) != 1 || t.word.Parts[0].Exp || t.word.Parts[0].X != nil {
			return "", fmt.Errorf("parse: args needs literal values")
		}
		p.i++
		return t.word.Parts[0].Lit, nil
	}
	for p.peek().kind != kRBrace {
		name, err := p.plainName("argument")
		if err != nil {
			return nil, err
		}
		// A name $name can read: letters, digits and _, not starting with a digit.
		if !argNameRe.MatchString(name) {
			return nil, fmt.Errorf("parse: argument name %q is not an identifier", name)
		}
		if seen[name] || name == "help" {
			return nil, fmt.Errorf("parse: duplicate or reserved argument %s", name)
		}
		seen[name] = true
		typ, err := literal()
		if err != nil {
			return nil, err
		}
		switch typ {
		case "string", "int", "bool", "duration":
		default:
			return nil, fmt.Errorf("parse: argument type must be string, int, bool, or duration")
		}
		f := Argument{Name: name, Type: typ}
		mode, err := literal()
		if err != nil {
			return nil, err
		}
		switch mode {
		case "required":
		case "default":
			v, err := literal()
			if err != nil {
				return nil, err
			}
			f.Default = &v
		default:
			return nil, fmt.Errorf("parse: argument needs required or default")
		}
		options := map[string]bool{}
		for !p.atSep() && p.peek().kind != kRBrace && p.peek().kind != kEOF {
			option, err := literal()
			if err != nil {
				return nil, err
			}
			if options[option] {
				return nil, fmt.Errorf("parse: duplicate argument option %s", option)
			}
			options[option] = true
			v, err := literal()
			if err != nil {
				return nil, err
			}
			switch option {
			case "help":
				f.Help = v
			case "choices":
				f.Choices = strings.Split(v, ",")
			default:
				return nil, fmt.Errorf("parse: unknown argument option %s", option)
			}
		}
		for _, c := range f.Choices {
			if _, err := argumentValue(f.Type, c); err != nil {
				return nil, fmt.Errorf("parse: invalid choice for %s", name)
			}
		}
		if f.Default != nil {
			if _, err := f.value(*f.Default); err != nil {
				return nil, fmt.Errorf("parse: invalid default for %s", name)
			}
		}
		a.Fields = append(a.Fields, f)
		p.skipSep()
	}
	p.i++
	return a, nil
}

func argumentValue(typ, text string) (value, error) {
	var d any
	var err error
	switch typ {
	case "string":
		return value{kind: valStr, s: text}, nil
	case "int":
		d, err = strconv.ParseInt(text, 10, 64)
	case "bool":
		if text != "true" && text != "false" {
			err = fmt.Errorf("boolean")
		} else {
			d = text == "true"
		}
	case "duration":
		var duration time.Duration
		duration, err = time.ParseDuration(text)
		if err == nil && (duration <= 0 || duration > 24*time.Hour) {
			err = fmt.Errorf("duration")
		}
		d = text
	}
	if err != nil {
		return value{}, fmt.Errorf("invalid %s", typ)
	}
	return fromDatum(d), nil
}
func (f Argument) value(text string) (value, error) {
	if len(f.Choices) > 0 {
		found := false
		for _, c := range f.Choices {
			if c == text {
				found = true
			}
		}
		if !found {
			return value{}, fmt.Errorf("must be one of %s", strings.Join(f.Choices, ", "))
		}
	}
	return argumentValue(f.Type, text)
}

func scriptArguments(src string, args []string) (map[string]value, string, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, "", nil
	} // The normal parser reports syntax errors.
	p := &parser{tok: toks}
	p.skipSep()
	if p.peek().kind != kWord || plain(p.peek().word) != "args" {
		if len(args) > 0 {
			return nil, "", fmt.Errorf("args: script has no argument declaration")
		}
		return nil, "", nil
	}
	stmt, err := p.arguments()
	if err != nil {
		return nil, "", err
	}
	a := stmt.(*Arguments)
	if len(args) == 1 && args[0] == "--help" {
		var b strings.Builder
		b.WriteString("Script arguments (pass after --):\n")
		for _, f := range a.Fields {
			mode := "required"
			if f.Default != nil {
				mode = "default: " + *f.Default
			}
			fmt.Fprintf(&b, "  --%s <%s> (%s)", f.Name, f.Type, mode)
			if len(f.Choices) > 0 {
				fmt.Fprintf(&b, " choices: %s", strings.Join(f.Choices, ", "))
			}
			if f.Help != "" {
				fmt.Fprintf(&b, " — %s", f.Help)
			}
			b.WriteByte('\n')
		}
		return nil, safeText(b.String()), nil
	}
	known := map[string]Argument{}
	for _, f := range a.Fields {
		known[f.Name] = f
	}
	supplied := map[string]string{}
	for i := 0; i < len(args); i += 2 {
		name := strings.TrimPrefix(args[i], "--")
		if !strings.HasPrefix(args[i], "--") {
			return nil, "", fmt.Errorf("args: expected --name value")
		}
		if _, ok := known[name]; !ok {
			return nil, "", fmt.Errorf("args: unknown argument %s", safeText(args[i]))
		}
		if _, ok := supplied[name]; ok {
			return nil, "", fmt.Errorf("args: duplicate --%s", name)
		}
		if i+1 >= len(args) {
			return nil, "", fmt.Errorf("args: missing value for --%s", name)
		}
		supplied[name] = args[i+1]
	}
	values := map[string]value{}
	for _, f := range a.Fields {
		text, ok := supplied[f.Name]
		if !ok {
			if f.Default == nil {
				return nil, "", fmt.Errorf("args: missing required --%s", f.Name)
			}
			text = *f.Default
		}
		v, err := f.value(text)
		if err != nil {
			return nil, "", fmt.Errorf("args: --%s %s", f.Name, err)
		}
		values[f.Name] = v
	}
	return values, "", nil
}
