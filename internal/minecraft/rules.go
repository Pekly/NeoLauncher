package minecraft

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

func (r *rule) UnmarshalJSON(b []byte) error {
	type plain rule
	var v plain
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&v); err != nil {
		return errors.New("unsupported rule structure")
	}
	*r = rule(v)
	return nil
}
func (s *Service) allowed(rules []rule) (bool, error) {
	if len(rules) == 0 {
		return true, nil
	}
	allowed := false
	knownFeatures := map[string]bool{"is_demo_user": false, "has_custom_resolution": false, "has_quick_plays_support": false, "is_quick_play_singleplayer": false, "is_quick_play_multiplayer": false, "is_quick_play_realms": false}
	for _, r := range rules {
		if r.Action != "allow" && r.Action != "disallow" {
			return false, errors.New("unsupported rule action")
		}
		match := true
		for k, v := range r.Features {
			actual, ok := knownFeatures[k]
			if !ok {
				return false, errors.New("unsupported feature rule: " + k)
			}
			if actual != v {
				match = false
			}
		}
		for k := range r.OS {
			if k != "name" && k != "arch" && k != "version" {
				return false, errors.New("unsupported OS rule")
			}
		}
		if name, ok := r.OS["name"]; ok && name != s.platform.OS {
			match = false
		}
		if arch, ok := r.OS["arch"]; ok {
			re, err := regexp.Compile("^(?:" + arch + ")$")
			if err != nil {
				return false, errors.New("invalid architecture rule")
			}
			aliases := []string{s.platform.Arch}
			switch s.platform.Arch {
			case "amd64":
				aliases = append(aliases, "x86_64")
			case "386":
				aliases = append(aliases, "x86", "i386")
			case "arm64":
				aliases = append(aliases, "aarch64")
			}
			found := false
			for _, a := range aliases {
				if re.MatchString(a) {
					found = true
				}
			}
			if !found {
				match = false
			}
		}
		if version, ok := r.OS["version"]; ok {
			re, err := regexp.Compile(version)
			if err != nil {
				return false, errors.New("invalid OS version rule")
			}
			if match {
				if s.platform.Version == "" {
					return false, errors.New("OS version rules are not supported on this platform")
				}
				if !re.MatchString(s.platform.Version) {
					match = false
				}
			}
		}
		if match {
			allowed = r.Action == "allow"
		}
	}
	return allowed, nil
}
func (s *Service) arguments(raw []json.RawMessage) ([]string, error) {
	var args []string
	for _, v := range raw {
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return nil, errors.New("null launch argument")
		}
		var text string
		if json.Unmarshal(v, &text) == nil {
			if strings.ContainsRune(text, 0) {
				return nil, errors.New("invalid argument")
			}
			args = append(args, text)
			continue
		}
		var conditional struct {
			Rules []rule          `json:"rules"`
			Value json.RawMessage `json:"value"`
		}
		d := json.NewDecoder(bytes.NewReader(v))
		d.DisallowUnknownFields()
		if err := d.Decode(&conditional); err != nil || len(conditional.Rules) == 0 {
			return nil, errors.New("unsupported conditional argument")
		}
		allowed, err := s.allowed(conditional.Rules)
		if err != nil {
			return nil, err
		}
		var values []string
		if len(conditional.Value) == 0 || bytes.Equal(bytes.TrimSpace(conditional.Value), []byte("null")) {
			return nil, errors.New("missing conditional argument value")
		}
		if json.Unmarshal(conditional.Value, &text) == nil {
			values = []string{text}
		} else if err = json.Unmarshal(conditional.Value, &values); err != nil {
			return nil, errors.New("invalid conditional argument value")
		}
		for _, text := range values {
			if strings.ContainsRune(text, 0) {
				return nil, errors.New("invalid argument")
			}
		}
		if allowed {
			args = append(args, values...)
		}
	}
	return args, nil
}
func (s *Service) gameArguments(m metadata) ([]string, error) {
	if len(m.Arguments.Game) > 0 {
		return s.arguments(m.Arguments.Game)
	}
	if m.MinecraftArguments == "" {
		return nil, errors.New("missing game arguments")
	}
	// Old official arguments are whitespace-separated, not shell syntax. Reject
	// quoted/escaped forms rather than implement an ambiguous shell interpreter.
	if strings.ContainsAny(m.MinecraftArguments, "\"'\\\x00") {
		return nil, errors.New("unsupported legacy argument quoting")
	}
	return strings.Fields(m.MinecraftArguments), nil
}

var mainClassPattern = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*(\.[A-Za-z_$][A-Za-z0-9_$]*)+$`)

func validMainClass(name string) bool { return mainClassPattern.MatchString(name) }

var placeholderPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

func substitute(args []string, values map[string]string) ([]string, error) {
	out := make([]string, len(args))
	for i, arg := range args {
		var failed bool
		out[i] = placeholderPattern.ReplaceAllStringFunc(arg, func(token string) string {
			v, ok := values[token[2:len(token)-1]]
			if !ok {
				failed = true
			}
			return v
		})
		if failed || strings.Contains(out[i], "${") || strings.ContainsRune(out[i], 0) {
			return nil, errors.New("unresolved or invalid launch argument")
		}
	}
	return out, nil
}
