package opencode

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"strings"
	"text/template"

	"github.com/pragmataW/graphmaster/internal/hookapi"
)

const pluginFileName = "graphmaster.js"

//go:embed plugin.js.tmpl
var pluginSource string

var pluginTmpl = template.Must(template.New("plugin").Parse(pluginSource))

// pluginData parameterizes the generated plugin.
type pluginData struct {
	Binary   string
	Timeouts map[hookapi.Event]int // milliseconds per hook event
}

// Hook timeouts. They match the other agents' installed hooks (Cursor: 5s
// for gating hooks) and leave headroom over the dispatcher's own per-event
// deadline (hookrun.EventTimeout: a 1.5s lock budget plus grace), so a slow
// but successful decision still reaches OpenCode. Background notifications
// get more room for a post-shell Freshen.
func defaultPluginData(bin string) pluginData {
	return pluginData{
		Binary: bin,
		Timeouts: map[hookapi.Event]int{
			hookapi.EventPreToolUse:    5000,
			hookapi.EventPreCompact:    5000,
			hookapi.EventPostToolUse:   8000,
			hookapi.EventSessionStart:  5000,
			hookapi.EventSubagentStart: 5000,
		},
	}
}

// renderPlugin produces the plugin source with every value embedded as a
// JavaScript literal.
func renderPlugin(d pluginData) ([]byte, error) {
	if d.Timeouts == nil {
		d.Timeouts = defaultPluginData(d.Binary).Timeouts
	}
	vals := map[string]any{
		"Binary":    d.Binary,
		"PreTools":  preTools,
		"PostTools": postTools,
		"Timeouts":  d.Timeouts,
	}
	lit := map[string]string{}
	for k, v := range vals {
		s, err := jsLiteral(v)
		if err != nil {
			return nil, err
		}
		lit[k] = s
	}
	var buf bytes.Buffer
	if err := pluginTmpl.Execute(&buf, lit); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// jsLiteral encodes v as JSON, which is valid JavaScript: encoding/json
// always escapes U+2028 and U+2029, the only JSON characters JS rejects.
func jsLiteral(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}
