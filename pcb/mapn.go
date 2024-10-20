package pcb

import (
	"fmt"
	"github.com/oleiade/gomme"
	"slices"
	"strings"
)

// MapN is a helper for easily implementing Map like parsers.
// It is not meant for writing grammars, but only for implementing parsers.
// Only the `fn`n function has to be provided.
// All other `fn`X functions are expected to be `nil`.
// Only parsers up to `p`n have to be provided.
// All higher numbered parsers are expected to be nil.
func MapN[PO1, PO2, PO3, PO4, PO5 any, MO any](
	p1 gomme.Parser[PO1], p2 gomme.Parser[PO2], p3 gomme.Parser[PO3], p4 gomme.Parser[PO4], p5 gomme.Parser[PO5],
	n int,
	fn1 func(PO1) (MO, error), fn2 func(PO1, PO2) (MO, error), fn3 func(PO1, PO2, PO3) (MO, error),
	fn4 func(PO1, PO2, PO3, PO4) (MO, error), fn5 func(PO1, PO2, PO3, PO4, PO5) (MO, error),
) gomme.Parser[MO] {
	var zero1 PO1
	var zero2 PO2
	var zero3 PO3
	var zero4 PO4
	var zero5 PO5

	expected := strings.Builder{}
	expected.WriteString(p1.Expected())
	if n > 1 {
		expected.WriteString(" + ")
		expected.WriteString(p2.Expected())
		if n > 2 {
			expected.WriteString(" + ")
			expected.WriteString(p3.Expected())
			if n > 3 {
				expected.WriteString(" + ")
				expected.WriteString(p4.Expected())
				if n > 4 {
					expected.WriteString(" + ")
					expected.WriteString(p5.Expected())
				}
			}
		}
	}

	containsNoWayBack := p1.ContainsNoWayBack()
	if n > 1 {
		containsNoWayBack = max(containsNoWayBack, p2.ContainsNoWayBack())
		if n > 2 {
			containsNoWayBack = max(containsNoWayBack, p3.ContainsNoWayBack())
			if n > 3 {
				containsNoWayBack = max(containsNoWayBack, p4.ContainsNoWayBack())
				if n > 4 {
					containsNoWayBack = max(containsNoWayBack, p5.ContainsNoWayBack())
				}
			}
		}
	}

	// Construct myNoWayBackRecoverer from the sub-parsers
	subRecoverers := make([]gomme.Recoverer, 0, 5)
	if p1.ContainsNoWayBack() > gomme.TernaryNo {
		subRecoverers = append(subRecoverers, p1.NoWayBackRecoverer)
	}
	if n > 1 {
		if p2.ContainsNoWayBack() > gomme.TernaryNo {
			subRecoverers = append(subRecoverers, p2.NoWayBackRecoverer)
		}
		if n > 2 {
			if p3.ContainsNoWayBack() > gomme.TernaryNo {
				subRecoverers = append(subRecoverers, p3.NoWayBackRecoverer)
			}
			if n > 3 {
				if p4.ContainsNoWayBack() > gomme.TernaryNo {
					subRecoverers = append(subRecoverers, p4.NoWayBackRecoverer)
				}
				if n > 4 {
					if p5.ContainsNoWayBack() > gomme.TernaryNo {
						subRecoverers = append(subRecoverers, p5.NoWayBackRecoverer)
					}
				}
			}
		}
	}
	myNoWayBackRecoverer := gomme.NewCombiningRecoverer(true, subRecoverers...)

	md := &mapData[PO1, PO2, PO3, PO4, PO5, MO]{
		id:                gomme.NewBranchParserID(),
		expected:          expected.String(),
		containsNoWayBack: containsNoWayBack,
		p1:                p1, p2: p2, p3: p3, p4: p4, p5: p5,
		n:   n,
		fn1: fn1, fn2: fn2, fn3: fn3, fn4: fn4, fn5: fn5,
		noWayBackRecoverer: myNoWayBackRecoverer,
		subRecoverers:      subRecoverers,
	}

	mapParse := func(state gomme.State) (gomme.State, MO) {
		return md.mapnAny(
			state, state,
			0,
			-1, -1,
			zero1, zero2, zero3, zero4, zero5,
		)
	}

	return gomme.NewParser[MO](
		expected.String(),
		mapParse,
		true,
		BasicRecovererFunc(mapParse),
		containsNoWayBack,
		myNoWayBackRecoverer.Recover,
	)
}

type mapData[PO1, PO2, PO3, PO4, PO5 any, MO any] struct {
	id                 uint64
	expected           string
	containsNoWayBack  gomme.Ternary
	p1                 gomme.Parser[PO1]
	p2                 gomme.Parser[PO2]
	p3                 gomme.Parser[PO3]
	p4                 gomme.Parser[PO4]
	p5                 gomme.Parser[PO5]
	n                  int
	fn1                func(PO1) (MO, error)
	fn2                func(PO1, PO2) (MO, error)
	fn3                func(PO1, PO2, PO3) (MO, error)
	fn4                func(PO1, PO2, PO3, PO4) (MO, error)
	fn5                func(PO1, PO2, PO3, PO4, PO5) (MO, error)
	noWayBackRecoverer gomme.CombiningRecoverer
	subRecoverers      []gomme.Recoverer
}

func (md *mapData[PO1, PO2, PO3, PO4, PO5, MO]) mapnAny(
	state gomme.State, remaining gomme.State,
	startIdx int,
	noWayBackStart int, noWayBackIdx int,
	out1 PO1, out2 PO2, out3 PO3, out4 PO4, out5 PO5,
) (gomme.State, MO) {
	var zero MO

	if startIdx >= md.n {
		if state.ParsingMode() == gomme.ParsingModeHappy {
			return md.mapnMap(state, out1, out2, out3, out4, out5)
		}
		return state, zero
	}

	switch state.ParsingMode() {
	case gomme.ParsingModeHappy: // normal parsing
		return md.sequenceHappy(
			state, remaining, startIdx, noWayBackStart, noWayBackIdx,
			out1, out2, out3, out4, out5,
		)
	case gomme.ParsingModeError: // find previous NoWayBack (backward)
		return md.mapnError(state, startIdx, out1, out2, out3, out4, out5)
	case gomme.ParsingModeHandle: // find error again (forward)
		return md.mapnHandle(state, startIdx, out1, out2, out3, out4, out5)
	case gomme.ParsingModeRewind: // go back to error / witness parser (1) (backward)
		return md.mapnRewind(state, startIdx, out1, out2, out3, out4, out5)
	case gomme.ParsingModeEscape: // escape the mess the hard way: use recoverer (forward)
		return md.mapnEscape(state, remaining, startIdx, out1, out2, out3, out4, out5)
	}
	return state.NewSemanticError(fmt.Sprintf(
		"programming error: MapN didn't handle parsing mode `%s`", state.ParsingMode())), zero

}

func (md *mapData[PO1, PO2, PO3, PO4, PO5, MO]) sequenceHappy(
	state gomme.State, remaining gomme.State,
	startIdx int,
	noWayBackStart int, noWayBackIdx int,
	out1 PO1, out2 PO2, out3 PO3, out4 PO4, out5 PO5,
) (gomme.State, MO) {
	var zeroMO MO

	if startIdx <= 0 { // caching only works if parsing from the start
		// use cache to know result immediately (Failed, Error, Consumed, Output)
		result, ok := state.CachedParserResult(md.id)
		if ok {
			if result.Failed {
				return state.ErrorAgain(result.Error), zeroMO
			}
			return state.MoveBy(result.Consumed), result.Output.(MO)
		}
	}

	// cache miss: parse
	outputs := make([]interface{}, 0, 4)
	var newState1 gomme.State
	if startIdx <= 0 {
		newState1, out1 = md.p1.It(state)
		if newState1.Failed() {
			state.CacheParserResult(md.id, 0, noWayBackIdx, noWayBackStart, newState1, outputs)
			return gomme.IWitnessed(state, md.id, 0, newState1), zeroMO
		}
		if state.NoWayBackMoved(newState1) {
			noWayBackIdx = 0
			noWayBackStart = 0
		}
	}
	outputs = append(outputs, out1)

	if md.n > 1 {
		var newState2 gomme.State
		if startIdx <= 1 {
			newState2, out2 = md.p2.It(newState1)
			if newState2.Failed() {
				state.CacheParserResult(md.id, 1, noWayBackIdx, noWayBackStart, newState2, outputs)
				state = gomme.IWitnessed(state, md.id, 0, newState2)
				if noWayBackStart < 0 { // we can't do anything here
					return state, zeroMO
				}
				return md.mapnError(state, 1, out1, out2, out3, out4, out5) // handle error locally
			}
			if newState1.NoWayBackMoved(newState2) {
				noWayBackIdx = 1
				noWayBackStart = state.ByteCount(newState1)
			}
		}
		outputs = append(outputs, out2)

		if md.n > 2 {
			var newState3 gomme.State
			if startIdx <= 2 {
				newState3, out3 = md.p3.It(newState2)
				if newState3.Failed() {
					state.CacheParserResult(md.id, 2, noWayBackIdx, noWayBackStart, newState3, outputs)
					state = gomme.IWitnessed(state, md.id, 0, newState3)
					if noWayBackStart < 0 { // we can't do anything here
						return state, zeroMO
					}
					return md.mapnError(state, 2, out1, out2, out3, out4, out5) // handle error locally
				}
				if newState2.NoWayBackMoved(newState3) {
					noWayBackIdx = 2
					noWayBackStart = state.ByteCount(newState2)
				}
			}
			outputs = append(outputs, out3)

			if md.n > 3 {
				var newState4 gomme.State
				if startIdx <= 3 {
					newState4, out4 = md.p4.It(newState3)
					if newState4.Failed() {
						state.CacheParserResult(md.id, 3, noWayBackIdx, noWayBackStart, newState4, outputs)
						state = gomme.IWitnessed(state, md.id, 0, newState4)
						if noWayBackStart < 0 { // we can't do anything here
							return state, zeroMO
						}
						return md.mapnError(state, 3, out1, out2, out3, out4, out5) // handle error locally
					}
					if newState3.NoWayBackMoved(newState4) {
						noWayBackIdx = 3
						noWayBackStart = state.ByteCount(newState3)
					}
				}
				outputs = append(outputs, out4)

				if md.n > 4 {
					var newState5 gomme.State
					newState5, out5 = md.p5.It(newState4)
					if newState5.Failed() {
						state.CacheParserResult(md.id, 4, noWayBackIdx, noWayBackStart, newState5, outputs)
						state = gomme.IWitnessed(state, md.id, 0, newState5)
						if noWayBackStart < 0 { // we can't do anything here
							return state, zeroMO
						}
						return md.mapnError(state, 4, out1, out2, out3, out4, out5) // handle error locally
					}
					if newState4.NoWayBackMoved(newState5) {
						noWayBackIdx = 4
						noWayBackStart = state.ByteCount(newState4)
					}

					mapped, err := md.fn5(out1, out2, out3, out4, out5)
					if err != nil {
						state.CacheParserResult(md.id, 4, noWayBackIdx, noWayBackStart, newState5, zeroMO)
						return newState5.NewSemanticError(err.Error()), zeroMO
					}
					state.CacheParserResult(md.id, 4, noWayBackIdx, noWayBackStart, newState5, mapped)
					return newState5, mapped
				}
				mapped, err := md.fn4(out1, out2, out3, out4)
				if err != nil {
					state.CacheParserResult(md.id, 3, noWayBackIdx, noWayBackStart, newState4, zeroMO)
					return newState4.NewSemanticError(err.Error()), zeroMO
				}
				state.CacheParserResult(md.id, 3, noWayBackIdx, noWayBackStart, newState4, mapped)
				return newState4, mapped
			}
			mapped, err := md.fn3(out1, out2, out3)
			if err != nil {
				state.CacheParserResult(md.id, 2, noWayBackIdx, noWayBackStart, newState3, zeroMO)
				return newState3.NewSemanticError(err.Error()), zeroMO
			}
			state.CacheParserResult(md.id, 2, noWayBackIdx, noWayBackStart, newState3, mapped)
			return newState3, mapped
		}
		mapped, err := md.fn2(out1, out2)
		if err != nil {
			state.CacheParserResult(md.id, 1, noWayBackIdx, noWayBackStart, newState2, zeroMO)
			return newState2.NewSemanticError(err.Error()), zeroMO
		}
		state.CacheParserResult(md.id, 1, noWayBackIdx, noWayBackStart, newState2, mapped)
		return newState2, mapped
	}
	mapped, err := md.fn1(out1)
	if err != nil {
		state.CacheParserResult(md.id, 0, noWayBackIdx, noWayBackStart, newState1, zeroMO)
		return newState1.NewSemanticError(err.Error()), zeroMO
	}
	state.CacheParserResult(md.id, 0, noWayBackIdx, noWayBackStart, newState1, mapped)
	return newState1, mapped
}

func (md *mapData[PO1, PO2, PO3, PO4, PO5, MO]) mapnError(state gomme.State, startIdx int, out1 PO1, out2 PO2, out3 PO3, out4 PO4, out5 PO5) (gomme.State, MO) {
	var zeroMO MO

	// use cache to know result immediately (HasNoWayBack, NoWayBackIdx, NoWayBackStart)
	result, ok := state.CachedParserResult(md.id)
	if !ok {
		return state.NewSemanticError(
			"grammar error: cache was empty in `MapN(error)` parser",
		), zeroMO
	}
	// found in cache
	if result.HasNoWayBack { // we should be able to switch to mode=handle
		var newState gomme.State
		expected := ""
		switch result.NoWayBackIdx {
		case 0:
			expected = md.p1.Expected()
			newState, _ = md.p1.It(state)
		case 1:
			expected = md.p2.Expected()
			newState, _ = md.p2.It(state.MoveBy(result.NoWayBackStart))
		case 2:
			expected = md.p3.Expected()
			newState, _ = md.p3.It(state.MoveBy(result.NoWayBackStart))
		case 3:
			expected = md.p4.Expected()
			newState, _ = md.p4.It(state.MoveBy(result.NoWayBackStart))
		default:
			expected = md.p5.Expected()
			newState, _ = md.p5.It(state.MoveBy(result.NoWayBackStart))
		}
		if newState.ParsingMode() != gomme.ParsingModeHandle {
			return state.NewSemanticError(fmt.Sprintf(
				"programming error: sub-parser (index: %d, expected: %q) didn't switch to "+
					"parsing mode `handle` in `MapN(error)` parser, but mode is: `%s`",
				result.NoWayBackIdx, expected, newState.ParsingMode())), zeroMO
		}
		if result.Failed {
			return md.mapnHandle(newState, result.Idx, out1, out2, out3, out4, out5)
		}
		return state.Preserve(newState), zeroMO
	}
	return state, zeroMO // we can't do anything
}

func (md *mapData[PO1, PO2, PO3, PO4, PO5, MO]) mapnHandle(state gomme.State, startIdx int, out1 PO1, out2 PO2, out3 PO3, out4 PO4, out5 PO5) (gomme.State, MO) {
	var zeroMO MO

	// use cache to know result immediately (Failed, Idx, ErrorStart)
	result, ok := state.CachedParserResult(md.id)
	if !ok {
		return state.NewSemanticError(
			"grammar error: cache was empty in `MapN(handle)` parser",
		), zeroMO
	}
	// found in cache
	if result.Failed { // we should be able to switch to mode=happy (or escape)
		var newState gomme.State
		switch result.Idx {
		case 0:
			newState, out1 = gomme.HandleWitness(state, md.id, 0, md.p1)
		case 1:
			newState, out2 = gomme.HandleWitness(
				state.MoveBy(result.ErrorStart), md.id, 0, md.p2,
			)
		case 2:
			newState, out3 = gomme.HandleWitness(
				state.MoveBy(result.ErrorStart), md.id, 0, md.p3,
			)
		case 3:
			newState, out4 = gomme.HandleWitness(
				state.MoveBy(result.ErrorStart), md.id, 0, md.p4,
			)
		default:
			newState, out5 = gomme.HandleWitness(
				state.MoveBy(result.ErrorStart), md.id, 0, md.p5,
			)
		}
		return md.mapnAny(
			state, newState,
			result.Idx+1,
			result.NoWayBackStart, result.NoWayBackIdx,
			out1, out2, out3, out4, out5,
		)
	}
	return state, zeroMO // we can't do anything
}

func (md *mapData[PO1, PO2, PO3, PO4, PO5, MO]) mapnRewind(
	state gomme.State,
	startIdx int,
	out1 PO1, out2 PO2, out3 PO3, out4 PO4, out5 PO5,
) (gomme.State, MO) {
	var zeroMO MO

	// use cache to know result immediately (Failed, Idx, ErrorStart)
	result, ok := state.CachedParserResult(md.id)
	if !ok {
		return state.NewSemanticError(
			"grammar error: cache was empty in `MapN(rewind)` parser",
		), zeroMO
	}
	// found in cache
	if result.Failed { // we should be able to switch to mode=happy (or escape)
		var newState gomme.State
		switch result.Idx {
		case 0:
			newState, out1 = gomme.HandleWitness(state, md.id, 0, md.p1)
		case 1:
			newState, out2 = gomme.HandleWitness(
				state.MoveBy(result.ErrorStart), md.id, 0, md.p2,
			)
		case 2:
			newState, out3 = gomme.HandleWitness(
				state.MoveBy(result.ErrorStart), md.id, 0, md.p3,
			)
		case 3:
			newState, out4 = gomme.HandleWitness(
				state.MoveBy(result.ErrorStart), md.id, 0, md.p4,
			)
		default:
			newState, out5 = gomme.HandleWitness(
				state.MoveBy(result.ErrorStart), md.id, 0, md.p5,
			)
		}
		return md.mapnAny(
			state, newState,
			result.Idx+1,
			result.NoWayBackStart, result.NoWayBackIdx,
			out1, out2, out3, out4, out5,
		)
	}
	return state, zeroMO // we can't do anything
}

func (md *mapData[PO1, PO2, PO3, PO4, PO5, MO]) mapnEscape(
	state gomme.State, remaining gomme.State,
	startIdx int,
	out1 PO1, out2 PO2, out3 PO3, out4 PO4, out5 PO5,
) (gomme.State, MO) {
	var zeroMO MO

	idx := 0
	if startIdx <= 0 { // use md.noWayBackRecoverer
		ok := false
		idx, ok = md.noWayBackRecoverer.CachedIndex(state)
		if !ok {
			md.noWayBackRecoverer.Recover(state)
			idx = md.noWayBackRecoverer.LastIndex()
		}
	} else { // we have to use seq.subRecoverers
		recoverers := slices.Clone(md.subRecoverers) // make shallow copy, so we can set the first elements to nil
		for i := 0; i < startIdx; i++ {
			recoverers[i] = nil
		}
		crc := gomme.NewCombiningRecoverer(false, recoverers...)
		crc.Recover(remaining) // find best Recoverer
		idx = crc.LastIndex()
	}

	if idx < 0 {
		return state.Preserve(remaining.NewSemanticError(fmt.Sprintf(
			"programming error: no recoverer found in `MapN(escape)` parser "+
				"and `startIdx`: %d", startIdx,
		))), zeroMO
	}

	var newState gomme.State
	switch idx {
	case 0:
		newState, out1 = md.p1.It(remaining)
	case 1:
		newState, out2 = md.p2.It(remaining)
	case 2:
		newState, out3 = md.p3.It(remaining)
	case 3:
		newState, out4 = md.p4.It(remaining)
	default:
		newState, out5 = md.p5.It(remaining)
	}
	if newState.ParsingMode() == gomme.ParsingModeHappy {
		return md.mapnMap(state, out1, out2, out3, out4, out5)
	}
	return state, zeroMO // we can't do anything
}

func (md *mapData[PO1, PO2, PO3, PO4, PO5, MO]) mapnMap(
	state gomme.State,
	out1 PO1, out2 PO2, out3 PO3, out4 PO4, out5 PO5,
) (gomme.State, MO) {
	var zero, mo MO
	var err error

	switch md.n {
	case 1:
		mo, err = md.fn1(out1)
	case 2:
		mo, err = md.fn2(out1, out2)
	case 3:
		mo, err = md.fn3(out1, out2, out3)
	case 4:
		mo, err = md.fn4(out1, out2, out3, out4)
	case 5:
		mo, err = md.fn5(out1, out2, out3, out4, out5)
	}
	if err != nil {
		return state.NewSemanticError(err.Error()), zero
	}
	return state, mo
}
