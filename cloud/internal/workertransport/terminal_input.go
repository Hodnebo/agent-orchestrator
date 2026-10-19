package workertransport

import "bytes"

// terminalInputToken is one key or terminal reply in agent terminal input.
type terminalInputToken struct {
	kind terminalInputKind
	// text is the byte of a terminalInputText token.
	text byte
	// params are a control sequence's parameter bytes, final its final byte
	// (0 when the input ended inside the sequence).
	params []byte
	final  byte
}

type terminalInputKind int

const (
	terminalInputText terminalInputKind = iota
	// terminalInputEscape is a lone Esc (or the Esc of an Alt chord).
	terminalInputEscape
	// terminalInputCSI is a control sequence: ESC [ params final.
	terminalInputCSI
	// terminalInputSS3 is ESC O final: application mode cursor and keypad keys.
	terminalInputSS3
	// terminalInputString is an OSC, DCS, APC, PM or SOS string: only a
	// terminal answers with one.
	terminalInputString
)

// scanTerminalInput splits terminal input into keys and terminal replies.
func scanTerminalInput(data []byte, visit func(terminalInputToken)) {
	for i := 0; i < len(data); {
		if data[i] != 0x1b || i+1 == len(data) {
			if data[i] == 0x1b {
				visit(terminalInputToken{kind: terminalInputEscape})
			} else {
				visit(terminalInputToken{kind: terminalInputText, text: data[i]})
			}
			i++
			continue
		}
		switch data[i+1] {
		case '[':
			j := i + 2
			for j < len(data) && data[j] >= 0x30 && data[j] <= 0x3f {
				j++
			}
			params := data[i+2 : j]
			for j < len(data) && data[j] >= 0x20 && data[j] <= 0x2f {
				j++
			}
			token := terminalInputToken{kind: terminalInputCSI, params: params}
			if j < len(data) {
				token.final = data[j]
				j++
			}
			visit(token)
			i = j
		case 'O':
			if i+2 == len(data) {
				// Alt+O.
				visit(terminalInputToken{kind: terminalInputEscape})
				i++
				continue
			}
			visit(terminalInputToken{kind: terminalInputSS3, final: data[i+2]})
			i += 3
		case ']', 'P', '_', '^', 'X':
			// Ends at BEL or ST (ESC \).
			j := i + 2
			for j < len(data) && data[j] != 0x07 && (data[j] != 0x1b || j+1 == len(data) || data[j+1] != '\\') {
				j++
			}
			switch {
			case j == len(data):
			case data[j] == 0x07:
				j++
			default:
				j += 2
			}
			visit(terminalInputToken{kind: terminalInputString})
			i = j
		default:
			visit(terminalInputToken{kind: terminalInputEscape})
			i++
		}
	}
}

// keyCode is a control sequence's first parameter without its sub-parameters
// (the kitty keyboard protocol adds alternate key codes after a colon).
func (t terminalInputToken) keyCode() string {
	code, _, _ := bytes.Cut(t.params, []byte{';'})
	code, _, _ = bytes.Cut(code, []byte{':'})
	return string(code)
}

// mayStartTurn reports whether terminal input can submit a prompt and so
// start a turn: it carries Enter, as CR or LF, keypad Enter (SS3 M), or the
// kitty keyboard protocol's Enter (CSI 13 u, with any modifiers). Esc, typing,
// navigation and function keys, and terminal replies such as a cursor
// position report (CSI 13;1R) cannot.
func mayStartTurn(data []byte) bool {
	submit := false
	scanTerminalInput(data, func(token terminalInputToken) {
		switch token.kind {
		case terminalInputText:
			submit = submit || token.text == '\r' || token.text == '\n'
		case terminalInputSS3:
			submit = submit || token.final == 'M'
		case terminalInputCSI:
			submit = submit || (token.final == 'u' && token.keyCode() == "13")
		}
	})
	return submit
}
