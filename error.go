package gomme

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// pcbError is an error message from the parser.
// It consists of the text itself and the position in the input where it happened.
type pcbError struct {
	text      string
	pos       int // pos is the byte index in the input (state.input.pos)
	line, col int // col is the 0-based byte index within srcLine; convert to 1-based rune index for user
	srcLine   string
}

// errHand contains all data needed for handling one error.
type errHand struct {
	err             *pcbError // error that is currently handled
	witnessID       uint64    // ID of the immediate parent branch parser that witnessed the error
	witnessPos      int       // input position of the witness parser
	culpritIdx      int       // index of the sub-parser that created the error
	curDel          int       // current number of tokes to delete for error handling
	ignoreErrParser bool      // true if the failing parser should be ignored
}

// IWitnessed lets a branch parser report an error that it witnessed in
// the sub-parser with index `idx` (0 if it has only 1 sub-parser).
func IWitnessed(state State, witnessID uint64, idx int, errState State) State {
	if state.errHand.err != nil {
		return state.NewSemanticError(
			"programming error: IWitnessed called while still handling an error")
	}
	if errState.errHand.witnessID == 0 { // error hasn't been witnessed yet
		if idx < 0 {
			idx = 0
		}
		errState.errHand.witnessID = witnessID
		errState.errHand.witnessPos = state.input.pos
		errState.errHand.culpritIdx = idx
	}
	state.errHand = errState.errHand
	return state
}

// HandleWitness returns the advanced state and output if the parser is
// the witness parser (1).
// If the branch parser isn't the witness (or there is no error case),
// the unmodified `state` and zero output are returned.
// The returned index should be used for distinguishing between the cases.
func HandleWitness[Output any](state State, id uint64, idx int, parsers ...Parser[Output]) (State, Output) {
	var output, zero Output

	if state.errHand.witnessID != id || state.errHand.witnessPos != state.input.pos {
		parse := parsers[idx]
		if parse.PossibleWitness() {

		}
		return parsers[idx].It(state) // this sub-parser or on of its sub-parsers might be the witness parser (1)
	}

	// we are witness
	orgPos := state.input.pos
	if state.errHand.culpritIdx >= len(parsers) {
		state = state.NewSemanticError(fmt.Sprintf(
			"programming error: length of sub-parsers is only %d but index of culprit sub-parser is %d",
			len(parsers), state.errHand.culpritIdx,
		))
		state.errHand.culpritIdx = len(parsers) - 1
	}
	parse := parsers[state.errHand.culpritIdx]
	for {
		switch state.mode {
		case ParsingModeHandle:
			state.errHand.err = nil
			state.errHand.curDel = 1
			state.errHand.ignoreErrParser = false
		case ParsingModeRewind:
			state.errHand.curDel++
			if state.errHand.curDel > state.maxDel {
				if !state.errHand.ignoreErrParser {
					state.errHand.curDel = 0
					state.errHand.ignoreErrParser = true
				} else {
					state.mode = ParsingModeEscape // give up and go the hard way
					return state, zero
				}
			}
		default:
			return state, zero // we are witness parser but there is nothing to do
		}
		state.mode = ParsingModeHappy // try again
		state.input.pos = orgPos
		state = state.deleter(state, state.errHand.curDel)
		if state.errHand.ignoreErrParser {
			return state, zero
		}
		state, output = parse.It(state)
		if !state.Failed() {
			return state, output // first parser succeeded, now try the rest
		}
		state.mode = ParsingModeRewind
	}
}

// ============================================================================
// Recoverers
//

// DefaultRecoverer shouldn't be used outside of this package.
// Please use pcb.BasicRecovererFunc instead.
func DefaultRecoverer[Output any](parse Parser[Output]) Recoverer {
	return DefaultRecovererFunc(parse.It)
}

// DefaultRecovererFunc is the heart of the DefaultRecoverer and shouldn't be used
// outside of this package either.
// Please use pcb.BasicRecovererFunc instead.
func DefaultRecovererFunc[Output any](parse func(State) (State, Output)) Recoverer {
	return func(state State) int {
		curState := state
		for curState.BytesRemaining() > 0 {
			newState, _ := parse(curState)
			if !newState.Failed() {
				return state.ByteCount(curState) // return the bytes up to the successful position
			}
			curState = curState.Delete(1)
		}
		return -1 // absolut worst case! :(
	}
}

// CachingRecoverer should only be used in places where the Recoverer
// will be used multiple times with the exact same input position.
// The NoWayBack parser is such a case.
func CachingRecoverer(recoverer Recoverer) Recoverer {
	id := cachingRecovererIDs.Add(1)

	return func(state State) int {
		waste, ok := state.cachedRecovererWaste(id)
		if !ok {
			waste = recoverer(state)
			state.cacheRecovererWaste(id, waste)
		}
		return waste
	}
}

type CombiningRecoverer struct {
	recoverers []Recoverer
	id         uint64
}

// NewCombiningRecoverer recovers by calling all sub-recoverers and returning
// the minimal waste.
// The index of the best Recoverer is stored in the cache.
// func NewCombiningRecoverer(recoverers ...Recoverer) CombiningRecoverer {
func NewCombiningRecoverer(recoverers ...Recoverer) CombiningRecoverer {
	return CombiningRecoverer{
		recoverers: recoverers,
		id:         combiningRecovererIDs.Add(1),
	}
}

func (crc CombiningRecoverer) Recover(state State) int {
	waste, _, ok := state.cachedRecovererWasteIdx(crc.id)
	if ok {
		return waste
	}

	waste = -1
	idx := -1
	for i, recoverer := range crc.recoverers {
		w := recoverer(state)
		switch {
		case w == -1: // ignore
		case w == 0: // it won't get better than this
			waste = 0
			idx = i
			break
		case waste < 0 || w < waste:
			waste = w
			idx = i
		}
	}
	state.cacheRecovererWasteIdx(crc.id, waste, idx)
	return waste
}

func (crc CombiningRecoverer) CachedIndex(state State) (idx int, ok bool) {
	_, idx, ok = state.cachedRecovererWasteIdx(crc.id)
	if !ok {
		return -1, false
	}
	return idx, true
}

// ============================================================================
// Deleters
//

// DefaultBinaryDeleter shouldn't be used outside of this package.
// Please use pcb.ByteDeleter instead.
func DefaultBinaryDeleter(state State, count int) State {
	return state.MoveBy(count)
}

// DefaultTextDeleter shouldn't be used outside of this package.
// Please use pcb.RuneTypeChangeDeleter instead.
func DefaultTextDeleter(state State, count int) State {
	found := 0
	oldTyp := rune(0)

	byteCount := strings.IndexFunc(state.CurrentString(), func(r rune) bool {
		var typ, paren rune

		switch {
		case unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_':
			typ = 'a'
		case unicode.IsSpace(r):
			typ = ' '
		case slices.Contains([]rune{'(', '[', '{', '}', ']', ')'}, r):
			typ = '('
		case slices.Contains([]rune{
			'+', '-', '*', '/', '%', '^', '=', ':', '<', '>', '~',
			'|', '\\', ';', '.', ',', '"', '`', '\'',
		}, r):
			typ = '+'
		default:
			typ = utf8.RuneError
		}

		if typ != oldTyp {
			if typ != ' ' && oldTyp != 0 {
				found++
			}
			if typ == '(' && oldTyp == '(' && r != paren {
				found++
			}
			oldTyp = typ
			paren = r // works just fine even if r isn't a parenthesis at all and saves an if
		}
		return found == count
	})

	if byteCount < 0 {
		return state.MoveBy(state.BytesRemaining())
	}
	return state.MoveBy(byteCount)
}

// ============================================================================
// Error Reporting
//

func singleErrorMsg(pcbErr pcbError) string {
	fullMsg := strings.Builder{}
	fullMsg.WriteString(pcbErr.text)
	fullMsg.WriteString(formatSrcLine(pcbErr.line, pcbErr.col, pcbErr.srcLine))

	return fullMsg.String()
}

func formatSrcLine(line, col int, srcLine string) string {
	result := strings.Builder{}
	lineStart := srcLine[:col]
	result.WriteString(lineStart)
	result.WriteRune(0x25B6) // easy to spot marker (▶) for exact error position
	result.WriteString(srcLine[col:])
	return fmt.Sprintf(" [%d:%d] %q",
		line, utf8.RuneCountInString(lineStart)+1, result.String()) // columns for the user start at 1
}

func pcbErrorsToGoErrors(pcbErrors []pcbError) error {
	if len(pcbErrors) == 0 {
		return nil
	}

	goErrors := make([]error, len(pcbErrors))
	for i, pe := range pcbErrors {
		goErrors[i] = errors.New(singleErrorMsg(pe))
	}

	return errors.Join(goErrors...)
}