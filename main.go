package main

import (
	"fmt"
	"strconv"
	"strings"
)

// YELLOW: 1 All the Types & variable used Start here

type tokenType uint8

const (
	group           tokenType = iota // GREEN: (...) — a parenthesized subexpression
	bracket         tokenType = iota // GREEN: [...] — a character class
	or              tokenType = iota // GREEN: a|b — alternation between two expressions
	repeat          tokenType = iota // GREEN: * + ? {m,n} — repetition operators
	literal         tokenType = iota // GREEN: a single exact character
	groupUncaptured tokenType = iota // GREEN: synthetic wrapper used by `or`

)

type token struct {
	tokenType tokenType
	value     any
}

type repeatPayload struct {
	min   int
	max   int // GREEN: -1 -> infinity
	token token
}

type parseContext struct {
	pos    int     // BLUE: index of the  currently being processed
	tokens []token // BLUE: token produce so far
}

// BLUE: this is parsing algo how we extract the string and process that

func parse(regex string) *parseContext {
	ctx := &parseContext{
		pos:    0,
		tokens: []token{},
	}

	for ctx.pos < len(regex) {
		process(regex, ctx)
		ctx.pos++
	}

	return ctx
}

func process(regex string, ctx *parseContext) {

	ch := regex[ctx.pos]

	switch ch {
	case '(':
		groupctx := &parseContext{
			pos:    ctx.pos,
			tokens: []token{},
		}

		parseGroup(regex, groupctx)

		ctx.tokens = append(ctx.tokens, token{
			tokenType: group,
			value:     groupctx.tokens,
		})

	case '[':
		parseBracket(regex, ctx)

	case '|':
		parseOr(regex, ctx)

	case '*', '?', '+':
		parseRepeat(regex, ctx)

	case '{':
		parseRepeatSpecified(regex, ctx)

	default:
		ctx.tokens = append(ctx.tokens, token{
			tokenType: literal,
			value:     ch,
		})
	}

}

func parseGroup(regex string, ctx *parseContext) {
	ctx.pos++
	for regex[ctx.pos] != ')' {
		process(regex, ctx)
		ctx.pos++
	}
}

func parseBracket(regex string, ctx *parseContext) {
	ctx.pos++
	var literals []string

	for regex[ctx.pos] != ']' {
		ch := regex[ctx.pos]

		if ch == '-' {
			next := regex[ctx.pos+1]
			prev := literals[len(literals)-1][0]
			literals[len(literals)-1] = fmt.Sprintf("%c%c", prev, next)
			ctx.pos++
		} else {
			literals = append(literals, fmt.Sprintf("%c", ch))
		}
		ctx.pos++
	}

	literalSet := map[uint8]bool{}

	for _, l := range literals {
		literalSet[l[0]] = true
		for i := l[0] + 1; i <= l[len(l)-1]; i++ {
			literalSet[i] = true
		}
	}

	ctx.tokens = append(ctx.tokens, token{
		tokenType: bracket,
		value:     literalSet,
	})
}

func parseOr(regex string, ctx *parseContext) {

	rhsContext := &parseContext{
		pos:    ctx.pos,
		tokens: []token{},
	}
	rhsContext.pos++ // YELLOW: skipping the '|'

	for rhsContext.pos < len(regex) && regex[rhsContext.pos] != ')' {
		process(regex, rhsContext)
	}

	left := token{
		tokenType: groupUncaptured,
		value:     ctx.tokens,
	}

	right := token{
		tokenType: groupUncaptured,
		value:     rhsContext.tokens,
	}

	ctx.pos = rhsContext.pos
	ctx.tokens = []token{{
		tokenType: or,
		value:     []token{left, right},
	}}

}

const repeatInfinity = -1

func parseRepeat(regex string, ctx *parseContext) {
	ch := regex[ctx.pos]
	var min, max int

	switch ch {
	case '*':
		min = 0
		max = repeatInfinity
	case '?':
		min = 0
		max = 1
	default: // +
		min = 1
		max = repeatInfinity
	}

	lastToken := ctx.tokens[len(ctx.tokens)-1]
	ctx.tokens[len(ctx.tokens)-1] = token{
		tokenType: repeat,
		value: repeatPayload{
			min:   min,
			max:   max,
			token: lastToken,
		},
	}

}

func parseRepeatSpecified(regex string, ctx *parseContext) {
	start := ctx.pos + 1

	// BLUE: until we reach at } we will update the ctx.pos
	for regex[ctx.pos] != '}' {
		ctx.pos++
	}
	boundariesStr := regex[start:ctx.pos]
	pieces := strings.Split(boundariesStr, ",")
	var min, max int

	if len(pieces) == 1 {
		value, err := strconv.Atoi(pieces[0])
		if err != nil {
			panic(err.Error())
		}
		min = value
		max = value
	} else if len(pieces) == 2 {
		value, err := strconv.Atoi(pieces[0])
		if err != nil {
			panic(err.Error())
		}
		min = value
		if pieces[1] == "" {
			max = repeatInfinity
		} else {
			value, err := strconv.Atoi(pieces[1])
			if err != nil {
				panic(err.Error())
			}
			max = value
		}

	} else {
		panic(fmt.Sprintf("There must be either 1 or 2 values specified for the quantifier: provided '%s'", boundariesStr))
	}

	lastToken := ctx.tokens[len(ctx.tokens)-1]
	ctx.tokens[len(ctx.tokens)-1] = token{
		tokenType: repeat,
		value: repeatPayload{
			min:   min,
			max:   max,
			token: lastToken,
		},
	}

}

const epsilonChar uint8 = 0

type state struct {
	start       bool
	terminal    bool
	transitions map[uint8][]*state
}

func toNfa(ctx *parseContext) *state {
	startState, endState := tokenToNfa(&ctx.tokens[0])

	for i := 1; i < len(ctx.tokens); i++ {
		startNext, endNext := tokenToNfa(&ctx.tokens[i])
		endState.transitions[epsilonChar] = append(
			endState.transitions[epsilonChar],
			startNext,
		)
		endState = endNext
	}

	start := &state{
		transitions: map[uint8][]*state{
			epsilonChar: {startState},
		},
		start: true,
	}
	end := &state{
		transitions: map[uint8][]*state{},
		terminal:    true,
	}

	endState.transitions[epsilonChar] = append(
		endState.transitions[epsilonChar],
		end,
	)

	return start
}

func tokenToNfa(t *token) (*state, *state) {
	start := &state{transitions: map[uint8][]*state{}}
	end := &state{transitions: map[uint8][]*state{}}

	switch t.tokenType {
	case literal:
		ch := t.value.(uint8)
		start.transitions[ch] = []*state{end}
	case or:
		values := t.value.([]token)
		left := values[0]
		right := values[1]

		s1, e1 := tokenToNfa(&left)
		s2, e2 := tokenToNfa(&right)

		start.transitions[epsilonChar] = []*state{s1, s2}
		e1.transitions[epsilonChar] = []*state{end}
		e2.transitions[epsilonChar] = []*state{end}
	case bracket:
		literals := t.value.(map[uint8]bool)

		for l := range literals {
			start.transitions[l] = []*state{end}
		}
	case group, groupUncaptured:
		tokens := t.value.([]token)

		start, end = tokenToNfa(&tokens[0])

		for i := 1; i < len(tokens); i++ {
			ts, te := tokenToNfa(&tokens[i])
			end.transitions[epsilonChar] = append(
				end.transitions[epsilonChar],
				ts,
			)
			end = te
		}
	case repeat:
		p := t.value.(repeatPayload)

		// 1. min == 0: you may skip the whole thing
		if p.min == 0 {
			start.transitions[epsilonChar] = []*state{end}
		}

		// 2. how many copies to build
		var copyCount int
		if p.max == repeatInfinity {
			if p.min == 0 {
				copyCount = 1
			} else {
				copyCount = p.min
			}
		} else {
			copyCount = p.max
		}

		// 3. build the first copy
		from, to := tokenToNfa(&p.token)
		start.transitions[epsilonChar] = append(
			start.transitions[epsilonChar],
			from,
		)

		// 4. build and chain the rest
		for i := 2; i <= copyCount; i++ {
			s, e := tokenToNfa(&p.token)

			to.transitions[epsilonChar] = append(
				to.transitions[epsilonChar],
				s,
			)

			from = s
			to = e

			// any copy beyond the mandatory minimum may be skipped
			if i > p.min {
				s.transitions[epsilonChar] = append(
					s.transitions[epsilonChar],
					end,
				)
			}
		}

		// 5. last copy connects to the fragment exit
		to.transitions[epsilonChar] = append(
			to.transitions[epsilonChar],
			end,
		)

		// 6. unbounded: loop back to the last mandatory copy
		if p.max == repeatInfinity {
			end.transitions[epsilonChar] = append(
				end.transitions[epsilonChar],
				from,
			)
		}
	default:
		panic("unknown type of token")
	}

	return start, end

}

const (
	startOfText uint8 = 1
	endOfText   uint8 = 2
)

func getChar(input string, pos int) uint8 {
	if pos >= len(input) {
		return endOfText
	}
	if pos < 0 {
		return startOfText
	}
	return input[pos]
}

func (s *state) check(input string, pos int) bool {
	ch := getChar(input, pos)

	// 1. accept
	if ch == endOfText && s.terminal {
		return true
	}

	// 2. consume one character
	if states := s.transitions[ch]; len(states) > 0 {
		nextState := states[0]
		if nextState.check(input, pos+1) {
			return true
		}
	}

	// 3. take epsilon transitions (consume nothing)
	for _, state := range s.transitions[epsilonChar] {
		if state.check(input, pos) {
			return true
		}

		// 4. special case: at the very beginning, allow the first
		//    epsilon-only step to also start consuming
		if ch == startOfText && state.check(input, pos+1) {
			return true
		}
	}

	// 5. no path worked
	return false
}

func main() {
	pattern := `a*lok`

	ctx := parse(pattern)
	nfa := toNfa(ctx)

	inputs := []string{
		"valid_email@example.com",
		"alok",
		"aallok",
		"loka",
		"aalok",
	}

	for _, in := range inputs {
		fmt.Printf("%-30s %v\n", in, nfa.check(in, -1))
	}
}
