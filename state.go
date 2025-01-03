package gomme

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
)

// ============================================================================
// This File contains only the State and cache data structures and all of their
// methods.
// ============================================================================

var cachingRecovererIDs = &atomic.Uint64{}

type cachedWaste struct {
	pos   int // position in the input
	waste int // waste of the recoverer
}

var combiningRecovererIDs = &atomic.Uint64{}

type cachedWasteIdx struct {
	pos   int // position in the input
	waste int // waste of the recoverer
	idx   int // index of the best sub-recoverer
}

var combiningParserIDs = &atomic.Uint64{}

type ParserResult struct {
	pos           int          // position in the input
	Idx           int          // index of the chosen branch or parser (success or fail)
	HasSaveSpot   bool         // true if the SaveSpot mark has been moved
	SaveSpotIdx   int          // index of last sub-parser that moved the mark
	SaveSpotStart int          // start of the input (relative to `pos`) for the SaveSpot parser
	SaveSpot      int          // the new SaveSpot mark (if HasSaveSpot) or -1
	Failed        bool         // true if the sub-parser failed and provided the error to be handled
	ErrorStart    int          // start of the input (relative to `pos`) for the failed sub-parser
	Consumed      int          // number of bytes consumed from the input during successful parsing
	Output        interface{}  // the Output of the parser (nil if it failed)
	Error         *ParserError // the error if the parser failed (nil if it succeeded)
}

type ParserOutput struct {
	pos    int         // position in the input
	Output interface{} // the Output of the parser
}

var callIDs = &atomic.Uint64{} // used for endless loop prevention

// State represents the current state of a parser.
type State struct {
	mode                   ParsingMode // one of: happy, error, handle, record, choose, play
	input                  Input
	saveSpot               int           // mark set by the SaveSpot parser
	recover                bool          // recover from errors
	errHand                errHand       // everything for handling one error
	oldErrors              []ParserError // errors that are or have been handled
	recovererWasteCache    map[uint64][]cachedWaste
	recovererWasteIdxCache map[uint64][]cachedWasteIdx
	parserCache            map[uint64][]ParserResult
	outputCache            map[int32][]ParserOutput
}

// ============================================================================
// Handle Input
//

func (st State) AtEnd() bool {
	return st.input.pos >= st.input.n
}

func (st State) BytesRemaining() int {
	return st.input.n - st.input.pos
}

func (st State) CurrentString() string {
	if st.input.binary && len(st.input.text) < st.input.n {
		st.input.text = string(st.input.bytes)
	}
	return st.input.text[st.input.pos:]
}

func (st State) CurrentBytes() []byte {
	if !st.input.binary && len(st.input.bytes) < st.input.n {
		st.input.bytes = []byte(st.input.text)
	}
	return st.input.bytes[st.input.pos:]
}

func (st State) CurrentPos() int {
	return st.input.pos
}

func (st State) StringTo(remaining State) string {
	if remaining.input.pos < st.input.pos {
		return ""
	}
	if st.input.binary && len(st.input.text) < st.input.n {
		st.input.text = string(st.input.bytes)
	}
	if remaining.input.pos > len(st.input.text) {
		return st.input.text[st.input.pos:]
	}
	return st.input.text[st.input.pos:remaining.input.pos]
}

func (st State) BytesTo(remaining State) []byte {
	if remaining.input.pos < st.input.pos {
		return []byte{}
	}
	if !st.input.binary && len(st.input.bytes) < st.input.n {
		st.input.bytes = []byte(st.input.text)
	}
	if remaining.input.pos > len(st.input.bytes) {
		return st.input.bytes[st.input.pos:]
	}
	return st.input.bytes[st.input.pos:remaining.input.pos]
}

func (st State) ByteCount(remaining State) int {
	if remaining.input.pos < st.input.pos {
		return 0 // we never go back so we don't give negative count back
	}
	n := st.input.n
	if remaining.input.pos > n {
		return n - st.input.pos
	}
	return remaining.input.pos - st.input.pos
}

func (st State) MoveBy(countBytes int) State {
	if countBytes < 0 {
		countBytes = 0
	}

	pos := st.input.pos
	n := min(st.input.n, pos+countBytes)
	st.input.pos = n

	if !st.input.binary {
		moveText := st.input.text[pos:n]
		lastNlPos := strings.LastIndexByte(moveText, '\n') // this is Unicode safe!!!
		if lastNlPos >= 0 {
			st.input.prevNl += lastNlPos + 1 // this works even if '\n' wasn't found at all
			st.input.line += strings.Count(moveText, "\n")
		}
	}

	return st
}

func (st State) Moved(other State) bool {
	return st.input.pos != other.input.pos
}

// Delete moves forward in the input, thus simulating deletion of input.
// For binary input it moves forward by bytes otherwise by UNICODE runes.
func (st State) Delete(count int) State {
	if count <= 0 { // don't delete at all
		return st
	}
	if st.input.binary {
		return st.MoveBy(count)
	}

	byteCount, j := 0, 0
	for i := range st.CurrentString() {
		byteCount += i
		j++
		if j >= count {
			return st.MoveBy(byteCount)
		}
	}
	return st.MoveBy(byteCount)
}

// ============================================================================
// Caching
//

// NewBranchParserID returns a new ID for a combining parser.
// This ID should be retrieved in the construction phase of the parsers and
// used in the runtime phase for caching.
func NewBranchParserID() uint64 {
	return combiningParserIDs.Add(1)
}

// NewCallID returns a new ID for a function call that might run into an
// endless loop.
// This ID should be retrieved for every call and passed on if calling
// functions of sub-parsers.
func NewCallID() uint64 {
	return callIDs.Add(1)
}

// cacheRecovererWaste remembers the `waste` at the current input position
// for the CachingRecoverer with ID `id`.
func (st State) cacheRecovererWaste(id uint64, waste int) {
	cacheValue(st.recovererWasteCache, id, cachedWaste{pos: st.input.pos, waste: waste},
		func(a, b cachedWaste) int {
			return cmp.Compare(a.pos, b.pos)
		}, st.maxDel)
}

// cachedRecovererWaste returns the saved waste for the current
// input position and CachingRecoverer ID `id` or (-1, false) if not found.
func (st State) cachedRecovererWaste(id uint64) (waste int, ok bool) {
	var wasteData cachedWaste

	wasteData, ok = cachedValue(st.recovererWasteCache, id, func(wasteData cachedWaste) bool {
		return wasteData.pos == st.input.pos
	})
	if !ok {
		return -1, false
	}
	return wasteData.waste, true
}

// cacheRecovererWasteIdx remembers the `waste` and index at the
// current input position for the CombiningRecoverer with ID `crID`.
func (st State) cacheRecovererWasteIdx(crID uint64, waste, idx int) {
	cacheValue(st.recovererWasteIdxCache, crID, cachedWasteIdx{pos: st.input.pos, waste: waste, idx: idx},
		func(a, b cachedWasteIdx) int {
			return cmp.Compare(a.pos, b.pos)
		}, st.maxDel)
}

// cachedRecovererWasteIdx returns the saved waste and index for the current
// input position and CombiningRecoverer ID or (-1, -1, false) if not found.
func (st State) cachedRecovererWasteIdx(crID uint64) (waste, idx int, ok bool) {
	var wasteData cachedWasteIdx

	wasteData, ok = cachedValue(st.recovererWasteIdxCache, crID, func(wasteData cachedWasteIdx) bool {
		return wasteData.pos == st.input.pos
	})
	if !ok {
		return -1, -1, false
	}
	return wasteData.waste, wasteData.idx, true
}

func (st State) CacheParserResult(
	id uint64,
	idx int,
	saveSpotIdx int,
	saveSpotStart int,
	newState State,
	output interface{},
) {
	mark := -1
	if saveSpotStart >= 0 {
		mark = newState.saveSpot
	}

	errStart := 0
	if newState.errHand.err != nil {
		errStart = st.ByteCount(newState)
	}
	result := ParserResult{
		pos:           st.input.pos,
		Idx:           idx,
		Failed:        newState.Failed(),
		SaveSpotIdx:   saveSpotIdx,
		HasSaveSpot:   saveSpotStart >= 0,
		SaveSpotStart: saveSpotStart,
		SaveSpot:      mark,
		Error:         newState.errHand.err,
		ErrorStart:    errStart,
		Output:        output,
	}

	cacheValue(st.parserCache, id, result, func(a, b ParserResult) int {
		return cmp.Compare(a.pos, b.pos)
	}, st.maxDel)
}

func (st State) CachedParserResult(id uint64) (result ParserResult, ok bool) {
	return cachedValue(st.parserCache, id, func(data ParserResult) bool {
		return data.pos == st.input.pos
	})
}

func cacheValue[T any, U cmp.Ordered](cache map[U][]T, id U, value T, f func(T, T) int, maxDel int) {
	cacheSize := max(maxDel+1, 8)

	scache, ok := cache[id]
	if !ok {
		scache = make([]T, 0, cacheSize)
		cache[id] = append(scache, value)
		return
	}

	if len(scache) < cacheSize {
		i := slices.IndexFunc(scache, func(t T) bool {
			return f(t, value) == 0
		})
		if i < 0 {
			cache[id] = append(scache, value)
			return
		}
		scache[i] = value
		return
	}

	i := IndexOrMinFunc(scache, value, f) // will never be -1
	scache[i] = value
}

func cachedValue[T any, U cmp.Ordered](cache map[U][]T, id U, f func(T) bool) (result T, ok bool) {
	var zero T
	var scache []T

	if scache, ok = cache[id]; !ok {
		return zero, false
	}

	i := slices.IndexFunc(scache, f)
	if i < 0 {
		return zero, false
	}
	return scache[i], true
}

func (st State) CacheOutput(id int32, output interface{}) {
	cacheValue(st.outputCache, id, ParserOutput{pos: st.input.pos, Output: output},
		func(a, b ParserOutput) int {
			return cmp.Compare(a.pos, b.pos)
		}, st.maxRecursion)
}
func (st State) CachedOutput(id int32) (output interface{}, ok bool) {
	return cachedValue(st.outputCache, id, func(data ParserOutput) bool {
		return data.pos == st.input.pos
	})
}
func (st State) PurgeOutput(id int32) {
	var scache []ParserOutput
	ok := false

	if scache, ok = st.outputCache[id]; !ok {
		return
	}

	i := slices.IndexFunc(scache, func(o ParserOutput) bool {
		return cmp.Compare(o.pos, st.input.pos) == 0
	})
	if i >= 0 {
		scache[i] = ParserOutput{pos: -1}
	}
}

// ClearAllCaches empties all caches of this state.
// It should be used after reaching a safe state.
// So after successfully handling an error or at the end of a
// successful SaveSpot parser.
// This helps to keep the memory overhead of the parser to a minimum.
// Since we reached a new position in the input and won't go back anymore,
// the cache contains nothing useful anymore.
func (st State) ClearAllCaches() State {
	clear(st.recovererWasteCache)
	clear(st.recovererWasteIdxCache)
	clear(st.parserCache)
	// clear(st.outputCache) the output might be needed by later parsers as it isn't part of the error handling
	return st
}

// ============================================================================
// Handle success and failure
//

// ParsingMode returns the current mode of the parser at the current
// input position.
// All combining parsers have to use this to know what to do.
func (st State) ParsingMode() ParsingMode {
	return st.mode
}

// Succeed returns the State with SaveSpot mark and mode saved from
// the subState.
// The error handling is not kept so it will turn a failed result into a
// successful one.
// This should only be used by the pcb.Optional parser.
func (st State) Succeed(subState State) State {
	st.saveSpot = max(st.saveSpot, subState.saveSpot)
	if st.mode != ParsingModeHappy || subState.mode != ParsingModeError {
		st.mode = subState.mode
	}
	return st
}

// Preserve returns the State with the error handling, saveSpot and
// mode kept from the subState.
func (st State) Preserve(subState State) State {
	st.saveSpot = max(st.saveSpot, subState.saveSpot)
	st.mode = subState.mode

	if subState.errHand.err != nil || subState.errHand.witnessID > 0 { // should be true
		st.errHand = subState.errHand
	}

	return st
}

// Fail returns the State with the error (without handling) kept from the
// subState. The mode will be set to `error`.
// The SaveSpot mark is intentionally not kept.
// This is useful for branch parsers that are leaf parsers to the outside.
func (st State) Fail(subState State) State {
	if st.mode == ParsingModeHappy {
		st.mode = ParsingModeError
		if subState.errHand.err != nil { // should be true
			st.errHand.err = subState.errHand.err
		}
	} else {
		st.mode = subState.mode
		st.errHand = subState.errHand
	}

	return st
}

// SucceedAgain sets the SaveSpot mark and input position from the result.
func (st State) SucceedAgain(result ParserResult) State {
	if result.SaveSpot >= 0 {
		st.saveSpot = result.SaveSpot
	}
	return st.MoveBy(result.Consumed)
}

// ErrorAgain is really just like NewError.
// It just exists for cached error results.
func (st State) ErrorAgain(newErr *ParserError) State {
	switch st.mode {
	case ParsingModeHappy:
		st.errHand.err = newErr
		if st.errHand.witnessID == 0 {
			st.mode = ParsingModeError
		} else {
			st.mode = ParsingModeRewind
		}
	default:
		return st.NewSemanticError(fmt.Sprintf(
			"programming error: State.NewError/ErrorAgain called in mode `%s`", st.mode))
	}
	return st
}

// NewError sets a syntax error with the message in this state at the current position.
// For syntax errors `expected ` is prepended to the message and the usual
// position and source line including marker are appended.
func (st State) NewError(message string) State {
	newErr := st.newParserError()
	newErr.text = "expected " + message

	return st.ErrorAgain(&newErr)
}

// NewSemanticError sets a semantic error with the messages in this state at the
// current position.
// For semantic errors `expected` is NOT prepended to the message but the usual
// position and source line including marker are appended.
func (st State) NewSemanticError(message string) State {
	err := st.newParserError()
	err.text = message
	st.oldErrors = append(st.oldErrors, err)
	return st
}

func (st State) newParserError() ParserError {
	newErr := ParserError{pos: st.input.pos, binary: st.input.binary, parserID: -1}
	if st.input.binary { // the rare binary case is misusing the text case data a bit...
		newErr.line, newErr.col, newErr.srcLine = st.bytesAround(st.input.pos)
	} else {
		newErr.line, newErr.col, newErr.srcLine = st.textAround(st.input.pos)
	}
	return newErr
}

func (st State) CurrentError() *ParserError {
	return st.errHand.err
}
func (st State) SaveError(err *ParserError) State {
	st.oldErrors = append(st.oldErrors, *err)
	return st
}

// Failed returns whether this state is in a failed state or not.
// The state is only failed if the last parser failed.
// Old errors that have been handled already don't count.
// Use State.HasError to check that (or just call State.Error).
func (st State) Failed() bool {
	return st.errHand.err != nil
}

// HasError returns true if any handled errors are registered.
// (Errors that would be returned by State.Errors())
func (st State) HasError() bool {
	return len(st.oldErrors) > 0 || st.errHand.err != nil
}

// StillHandlingError returns true if we are still handling an error
// as opposed to witnessing a new error.
func (st State) StillHandlingError() bool {
	return st.errHand.ignoreErrParser || st.errHand.curDel > 1
}

// ============================================================================
// Produce error messages and give them back
//

// CurrentSourceLine returns the source line corresponding to the current position
// including [line:column] at the start and a marker at the exact error position.
// This should be used for reporting errors that are detected later.
// The binary case is handled accordingly.
func (st State) CurrentSourceLine() string {
	if st.input.binary {
		return formatBinaryLine(st.bytesAround(st.input.pos))
	} else {
		return formatSrcLine(st.textAround(st.input.pos))
	}
}

func (st State) bytesAround(pos int) (line, col int, srcLine string) {
	start := max(0, pos-8)
	end := min(start+16, st.input.n)
	if end-start < 16 { // try to fill up from the other end...
		start = max(0, end-16)
	}
	srcLine = string(st.input.bytes[start:end])
	return start, pos - start, srcLine
}

func (st State) textAround(pos int) (line, col int, srcLine string) {
	if pos < 0 {
		pos = 0
	}
	if len(st.input.text) == 0 {
		return 1, 0, ""
	}
	if pos > st.input.prevNl { // pos is ahead of prevNL => search forward
		return st.whereForward(pos, st.input.line, st.input.prevNl)
	} else if pos <= st.input.prevNl-pos { // pos is too far back => search from start
		return st.whereForward(pos, 1, -1)
	} else { // pos is just a little back => search backward
		return st.whereBackward(pos, st.input.line, st.input.prevNl)
	}
}
func (st State) whereForward(pos, lineNum, prevNl int) (line, col int, srcLine string) {
	text := st.input.text
	var nextNl int // Position of next newline or end

	for {
		nextNl = strings.IndexByte(text[prevNl+1:], '\n')
		if nextNl < 0 {
			nextNl = len(text)
		} else {
			nextNl += prevNl + 1
		}

		stop := false
		line, col, srcLine, stop = st.tryWhere(prevNl, pos, nextNl, lineNum)
		if stop {
			return line, col, srcLine
		}
		prevNl = nextNl
		lineNum++
	}
}
func (st State) whereBackward(pos, lineNum, nextNl int) (line, col int, srcLine string) {
	text := st.input.text
	var prevNl int // Line start (position of preceding newline)

	for {
		prevNl = strings.LastIndexByte(text[0:nextNl], '\n')
		lineNum--

		stop := false
		line, col, srcLine, stop = st.tryWhere(prevNl, pos, nextNl, lineNum)
		if stop {
			return line, col, srcLine
		}
		nextNl = prevNl
	}
}
func (st State) tryWhere(prevNl int, pos int, nextNl int, lineNum int) (line, col int, srcLine string, stop bool) {
	if prevNl < pos && pos <= nextNl {
		return lineNum, pos - prevNl - 1, string(st.input.text[prevNl+1 : nextNl]), true
	}
	return 1, 0, "", false
}

// Errors returns all error messages accumulated by the state as a Go error.
// Multiple errors have been joined (by errors.Join()).
func (st State) Errors() error {
	pcbErrors := slices.Clone(st.oldErrors)
	n := len(pcbErrors)
	if st.errHand.err != nil && (n == 0 || st.errHand.err.pos != pcbErrors[n-1].pos) {
		pcbErrors = append(pcbErrors, *st.errHand.err)
	}

	if len(pcbErrors) == 0 {
		return nil
	}

	goErrors := make([]error, len(pcbErrors))
	for i, pe := range pcbErrors {
		goErrors[i] = errors.New(singleErrorMsg(pe))
	}

	return errors.Join(goErrors...)
}

// SaveSpot is true iff we crossed a saveSpot.
func (st State) SaveSpot() bool {
	return st.saveSpot >= st.input.pos
}

// SaveSpotMoved is true iff the saveSpot is different between the 2 states.
func (st State) SaveSpotMoved(other State) bool {
	return st.saveSpot != other.saveSpot
}
