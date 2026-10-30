# Building a Regex Engine from Scratch — Study Notes

> Based on [How to build a regex engine from scratch](https://rhaeguard.github.io/posts/regex/) by rhaeguard.
>
> **Scope:** exactly the subset that the article builds. That means:
> no escape sequences (`\d`, `\.`), no `.` wildcard, no `[^...]`, no anchors (`^`, `$`),
> no capture groups, no lazy quantifiers.
>
> The goal is to understand the three stages — **parse → build machine → match** — and to be
> able to type the whole thing out yourself.

**Contents**

- [0. The big picture](#0-the-big-picture)
- [Part 1 — Parsing](#part-1--parsing)
- [Part 2 — Building the state machine](#part-2--building-the-state-machine)
- [Part 3 — Matching](#part-3--matching)
- [Part 4 — Assembling the program](#part-4--assembling-the-program)
- [Part 5 — Cheatsheet](#part-5--cheatsheet)
- [Part 6 — Exercises](#part-6--exercises)

---

## 0. The big picture

The article builds an engine that can evaluate this pattern:

```
[a-zA-Z][a-zA-Z0-9_.]+@[a-zA-Z0-9]+.[a-zA-Z]{2,}
```

Note: in this engine `.` is a **literal dot**, not "any character". This is deliberate — the
email pattern needs a literal dot, and the article never implements the wildcard.

The pipeline is:

```
        regex string
             │
             │  1. parse
             ▼
        []token  (a tree)
             │
             │  2. Thompson's construction
             ▼
        ε-NFA  (a graph)
             │
             │  3. check()
             ▼
        true / false
```

Each stage answers a different question:

| Stage | Question it answers | Output |
|---|---|---|
| Parse | What does the regex *say*? | a tree of tokens |
| Build | What machine *accepts* that language? | a graph of states |
| Match | Does *this input* drive the machine to an accept state? | bool |

The single most important idea in the whole article:

> **A regular expression and a finite automaton describe the same thing.**
> The regex is the human-friendly notation; the automaton is the machine that runs it.

So the job is a translation: syntax → machine. Then matching is just walking the machine.

---

# Part 1 — Parsing

## 1.1 Why a flat string is not enough

Look at:

```
ab*
```

Read as three independent characters, it is ambiguous: does the `*` apply to `a`, to `b`, or to
`ab`? The answer is `b`, because a quantifier binds to the **single preceding atom**. But that
information is implicit in the string.

Now look at:

```
(ab)*
```

Here the `*` applies to the whole group. The parentheses change the meaning completely.

A parser's job is to recover this structure and make it explicit in memory. We turn the flat
string into a **tree** where each node knows what it is and what it contains.

## 1.2 The token types

There are only six kinds of nodes:

```go
type tokenType uint8

const (
	group           tokenType = iota // (...)  — a parenthesized subexpression
	bracket         tokenType = iota // [...]  — a character class
	or              tokenType = iota // a|b    — alternation
	repeat          tokenType = iota // * + ? {m,n} — a quantifier
	literal         tokenType = iota // a single exact character
	groupUncaptured tokenType = iota // synthetic wrapper used by `or`
)
```

Why two group types? `group` comes from real parentheses in the source. `groupUncaptured` is
created *by the parser itself* when it builds an alternation: it wraps the left and right
branches so that both sides have the same shape. The engine does not implement captures, so the
two types behave identically. The distinction is only bookkeeping.

Each token needs a different payload: a literal needs a byte, a bracket needs a set of bytes, an
`or` needs two branches, a repeat needs min/max plus the thing being repeated. Go has no unions,
so the article stores the payload in an `any`:

```go
type token struct {
	tokenType tokenType
	value     any // the concrete type depends on tokenType
}
```

The concrete payloads:

| `tokenType` | `value` type | Meaning |
|---|---|---|
| `literal` | `uint8` | exactly one character |
| `bracket` | `map[uint8]bool` | any one character from this set |
| `group` | `[]token` | a list of tokens, in sequence |
| `groupUncaptured` | `[]token` | same, used as an `or` branch |
| `or` | `[]token` of length 2 | `[left, right]` |
| `repeat` | `repeatPayload` | quantifier |

```go
type repeatPayload struct {
	min   int
	max   int   // -1 means infinity
	token token // the atom being repeated
}
```

## 1.3 The parsing context

The parser is a single left-to-right walk. It needs two pieces of state:

```go
type parseContext struct {
	pos    int     // index of the character currently being processed
	tokens []token // tokens produced so far
}
```

That is the entire state. No lookahead buffer, no stack of its own — recursion provides the
stack when we hit a group.

## 1.4 The main loop

```go
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
```

`process` handles **one construct** and leaves `ctx.pos` on the **last byte it consumed**. The
loop then advances past it. This contract is important; keep it in mind, because one of the
article's functions breaks it (see §1.12).

## 1.5 `process` — the dispatcher

```go
func process(regex string, ctx *parseContext) {
	ch := regex[ctx.pos]

	if ch == '(' {
		groupCtx := &parseContext{
			pos:    ctx.pos,
			tokens: []token{},
		}
		parseGroup(regex, groupCtx)
		ctx.tokens = append(ctx.tokens, token{
			tokenType: group,
			value:     groupCtx.tokens,
		})
	} else if ch == '[' {
		parseBracket(regex, ctx)
	} else if ch == '|' {
		parseOr(regex, ctx)
	} else if ch == '*' || ch == '?' || ch == '+' {
		parseRepeat(regex, ctx)
	} else if ch == '{' {
		parseRepeatSpecified(regex, ctx)
	} else {
		ctx.tokens = append(ctx.tokens, token{
			tokenType: literal,
			value:     ch,
		})
	}
}
```

> **Two omissions in this snippet.** First, after `parseGroup` it never does
> `ctx.pos = groupCtx.pos`, so the outer parser re-reads the group body. Second, `parseOr` leaves
> `pos` on the delimiter instead of on the last consumed byte. Both are fixed in §1.12. If you
> type the article verbatim, any regex containing `(...)` will misparse.

The logic is a flat if-chain:

| Character | Meaning | Handler |
|---|---|---|
| `(` | start of a group | `parseGroup` |
| `[` | start of a class | `parseBracket` |
| `|` | alternation | `parseOr` |
| `*` `+` `?` | simple quantifier | `parseRepeat` |
| `{` | quantifier with bounds | `parseRepeatSpecified` |
| anything else | literal character | append a `literal` token |

Notice what is **not** here: no error checking, no escapes, no anchoring. The article says so
explicitly and keeps the code short.

## 1.6 Groups — recursion

```go
func parseGroup(regex string, ctx *parseContext) {
	ctx.pos += 1 // get past the '('
	for regex[ctx.pos] != ')' {
		process(regex, ctx)
		ctx.pos += 1
	}
}
```

This is the same loop as `parse`, except it stops at `)`. `ctx` here is the **group's** context,
so `ctx.pos` points at the closing `)` when the loop ends.

> **Watch out:** the article's `process` never copies `groupCtx.pos` back into the outer
> `ctx.pos`. The outer parser then continues from the `(`, re-parsing the group body as
> top-level tokens. Add one line after `parseGroup`:
>
> ```go
> parseGroup(regex, groupCtx)
> ctx.pos = groupCtx.pos // <-- the article omits this
> ```
>
> See §1.12, which explains the failure and the second position bug in `parseOr`.

Why a **new** context in `process`? Because tokens inside the group must be collected separately.
Consider:

```
(a|b)c
```

The `c` must not become part of the group. The group gets its own `tokens` slice; when the group
ends, that whole slice is frozen into a single `group` token and appended to the outer context.

So a group is a **single atom** from the outside. That is exactly what makes `(ab)*` work: the
`*` sees one token, and that token happens to contain two literals.

## 1.7 Brackets — character classes

This is the most fiddly function in the parser.

```go
func parseBracket(regex string, ctx *parseContext) {
	ctx.pos++ // get past the '['
	var literals []string

	for regex[ctx.pos] != ']' {
		ch := regex[ctx.pos]

		if ch == '-' {
			next := regex[ctx.pos+1]
			prev := literals[len(literals)-1][0]
			literals[len(literals)-1] = fmt.Sprintf("%c%c", prev, next)
			ctx.pos++ // consume the character after '-'
		} else {
			literals = append(literals, fmt.Sprintf("%c", ch))
		}

		ctx.pos++
	}

	literalsSet := map[uint8]bool{}

	for _, l := range literals {
		for i := l[0]; i <= l[len(l)-1]; i++ {
			literalsSet[i] = true
		}
	}

	ctx.tokens = append(ctx.tokens, token{
		tokenType: bracket,
		value:     literalsSet,
	})
}
```

### The trick: "range" is just a two-character string

The parser does not store ranges as `(start, end)` pairs. It stores them as a **string of length
two**, where the first byte is the start and the last byte is the end.

So while scanning `[a-z]`:

| Step | `ch` | `literals` before | Action | `literals` after |
|---|---|---|---|---|
| 1 | `a` | `[]` | append | `["a"]` |
| 2 | `-` | `["a"]` | replace last with `prev` + `next` | `["az"]` |
| 3 | `z` | — | consumed by the `-` branch | `["az"]` |

Then the expansion loop handles both cases uniformly:

- `"a"` → `l[0] == l[len-1]` → adds just `a`
- `"az"` → `l[0] == 'a'`, `l[len-1] == 'z'` → adds `a` through `z`

That uniformity is the nice part of this representation.

### Duplicates and overlaps

`[a-me-s]` produces:

```
["am", "es"]
```

Expanding gives `a..m` and `e..s`. The sets overlap (`e..m`), but a `map[uint8]bool` silently
de-duplicates. That is why the article uses a map rather than a slice: a class is conceptually a
**set**, and a set has no duplicates.

Go has no built-in set type, so `map[uint8]bool` is the idiomatic stand-in.

### What this parser assumes

- Every `-` is a range operator.
- The `-` always has a character before it and a character after it.
- `]` always closes the class.

So `[a-]`, `[-a]`, and `[]]` are not handled. The article acknowledges that this is simplified.
This is not a bug in the tutorial's scope; it is a limit of the subset.

## 1.8 Alternation

```go
func parseOr(regex string, ctx *parseContext) {
	rhsContext := &parseContext{
		pos:    ctx.pos,
		tokens: []token{},
	}
	rhsContext.pos += 1 // get past the '|'

	for rhsContext.pos < len(regex) && regex[rhsContext.pos] != ')' {
		process(regex, rhsContext)
		rhsContext.pos += 1
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
```

> **Watch out:** the final assignment `ctx.pos = rhsContext.pos` is wrong — it points at `)` or
> at `len(regex)`, not at the last consumed byte. See §1.12 bug 2. The fix is
> `ctx.pos = rhsContext.pos - 1`.

The key observation: **by the time `process` sees `|`, everything to its left has already been
parsed.** That is simply how left-to-right parsing works. So the left branch is the current
token list, and we only need to parse the right branch.

```
before:  ctx.tokens = [a, b, c]     and we just saw '|'
left  := [a, b, c]
right := parse the rest of this context
after:   ctx.tokens = [ or([a,b,c], right) ]
```

The left side is wrapped in `groupUncaptured` so both branches have the same type before being
stored in the `or` token.

`parseOr` stops at `)` or at the end of the string, whichever comes first. That is what keeps an
alternation inside a group from swallowing characters after the group.

### Precedence check

Because `|` grabs everything parsed so far, and concatenation just appends tokens, we get the
standard precedence for free:

```
ab|cd   →  or( [a,b], [c,d] )      as intended
a|b|c   →  or( [a], or([b],[c]) )  right-nested, same meaning
```

## 1.9 Repetition — the simple symbols

```go
const repeatInfinity = -1

func parseRepeat(regex string, ctx *parseContext) {
	ch := regex[ctx.pos]
	var min, max int

	if ch == '*' {
		min = 0
		max = repeatInfinity
	} else if ch == '?' {
		min = 0
		max = 1
	} else { // '+'
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
```

This is the heart of the parser, and it is only a few lines:

> **A quantifier wraps the last token produced.**

That works because a quantifier is a **postfix** operator: the thing it applies to is always
immediately to its left, and that thing has already been parsed.

The three symbols are the same node with different bounds:

| Symbol | min | max |
|---|---|---|
| `*` | 0 | ∞ |
| `+` | 1 | ∞ |
| `?` | 0 | 1 |

The article stores `∞` as `-1`. Every later stage interprets `max == -1` as "no upper bound".

## 1.10 Repetition — `{m}`, `{m,}`, `{m,n}`

```go
func parseRepeatSpecified(regex string, ctx *parseContext) {
	start := ctx.pos + 1 // skip '{'
	for regex[ctx.pos] != '}' {
		ctx.pos++
	}

	boundariesStr := regex[start:ctx.pos]
	pieces := strings.Split(boundariesStr, ",")

	var min, max int

	if len(pieces) == 1 {
		// {n}
		value, err := strconv.Atoi(pieces[0])
		if err != nil {
			panic(err.Error())
		}
		min = value
		max = value
	} else if len(pieces) == 2 {
		// {m,} or {m,n}
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
```

The only new idea is parsing the inside of the braces:

| Written | Split on `,` | Result |
|---|---|---|
| `{3}` | `["3"]` | min = 3, max = 3 |
| `{3,7}` | `["3", "7"]` | min = 3, max = 7 |
| `{3,}` | `["3", ""]` | min = 3, max = ∞ |

After the bounds are known, the last step is identical to `parseRepeat`: wrap the last token.

## 1.11 Full trace of the email pattern

Input:

```
[a-zA-Z][a-zA-Z0-9_.]+@[a-zA-Z0-9]+.[a-zA-Z]{2,}
```

Top-level token list as parsing proceeds:

| Step | Input consumed | Handler | Top-level tokens after |
|---|---|---|---|
| 1 | `[a-zA-Z]` | `parseBracket` | `[B1]` |
| 2 | `[a-zA-Z0-9_.]` | `parseBracket` | `[B1, B2]` |
| 3 | `+` | `parseRepeat` | `[B1, repeat(1,∞,B2)]` |
| 4 | `@` | literal | `[B1, repeat(1,∞,B2), @]` |
| 5 | `[a-zA-Z0-9]` | `parseBracket` | `[..., B3]` |
| 6 | `+` | `parseRepeat` | `[..., repeat(1,∞,B3)]` |
| 7 | `.` | literal | `[..., .]` |
| 8 | `[a-zA-Z]` | `parseBracket` | `[..., B4]` |
| 9 | `{2,}` | `parseRepeatSpecified` | `[..., repeat(2,∞,B4)]` |

Where:

```
B1 = {a..z, A..Z}
B2 = {a..z, A..Z, 0..9, '_', '.'}
B3 = {a..z, A..Z, 0..9}
B4 = {a..z, A..Z}
```

Final tree (6 top-level tokens):

```
concat
├── bracket B1
├── repeat(1, ∞)
│   └── bracket B2
├── literal '@'
├── repeat(1, ∞)
│   └── bracket B3
├── literal '.'
└── repeat(2, ∞)
    └── bracket B4
```

> The article's token listing shows only five entries because it collapses the first bracket and
> the first repeat into one line. The real parse has six top-level tokens, as above.

### Reading the regex in English

> A letter, then one or more letters/digits/underscore/dot, then `@`, then one or more
> letters/digits, then a literal `.`, then two or more letters.

That is why `alice.smith123@email.co.uk` fails: the last part of the regex wants **only
letters**, but `co.uk` contains a dot.

## 1.12 Bugs in the article's parser (and the fixes)

The article's test regex contains **no groups**, which hides two position bugs. With the
snippets exactly as printed, `(ab)c` parses as `group(a,b), a, b, ')', c`. Here is why, and
here is how to fix it.

### Bug 1: the group's end position is never copied back

```go
groupCtx := &parseContext{
	pos:    ctx.pos,
	tokens: []token{},
}

parseGroup(regex, groupCtx)

// MISSING: ctx.pos = groupCtx.pos
ctx.tokens = append(ctx.tokens, token{
	tokenType: group,
	value:     groupCtx.tokens,
})
```

`groupCtx` and `ctx` are two different structs, and `pos` is an `int` (copied by value). Mutating
`groupCtx.pos` does **not** touch `ctx.pos`. So after the group token is appended, the outer
context is still sitting on the `(`. The main loop then does `ctx.pos++` and starts parsing the
group contents **again** as top-level tokens.

Trace of `(ab)c`:

```
index:   0 1 2 3 4
input:   ( a b ) c
```

1. `process` at 0 sees `(`. `parseGroup` runs on `groupCtx` and ends with
   `groupCtx.pos = 3` (the `)`) and `groupCtx.tokens = [a, b]`.
2. The outer context appends `group(a, b)`, but `ctx.pos` is still **0**.
3. Main loop `ctx.pos++` → 1. `process` sees `a` → appends another literal `a`.
4. `ctx.pos++` → 2 → appends literal `b`.
5. `ctx.pos++` → 3 → `)` is not special, so it becomes a **literal `)`**.
6. `ctx.pos++` → 4 → appends literal `c`.

Result:

```
group(a, b), a, b, lit(')'), c
```

No panic — just a wrong tree. Every group in the regex silently duplicates its body.

**Fix:**

```go
parseGroup(regex, groupCtx)
ctx.pos = groupCtx.pos // <-- add this
```

Now the outer context ends at the `)`, the main loop advances to `c`, and the result is
`group(a,b), c`.

### Bug 2: `parseOr` leaves `pos` on the delimiter

The contract from §1.4 says `process` must leave `ctx.pos` on the **last byte it consumed**.
`parseOr` does not: it sets `ctx.pos = rhsContext.pos`, and `rhsContext.pos` points at `)` or at
`len(regex)` — an **unconsumed** byte.

After fixing bug 1, this still breaks `parseGroup`, whose loop is:

```go
for regex[ctx.pos] != ')' {
	process(regex, ctx)
	ctx.pos++ // skips past the ')' that parseOr left behind
}
```

Consequences:

- `(a|b)` → `parseOr` leaves `pos` on `)`, the group loop increments past it, and the next
  condition reads `regex[len]` → **index out of range panic** (the article has no bounds check).
- `(a|b)c` → the same skip makes the loop treat `c` as part of the group, then panics.
- `(a|b)*` → the `*` is processed inside the group loop, then the loop panics.

**Fix:** one line at the end of `parseOr`:

```go
// leave pos on the last byte consumed, as process() promises
ctx.pos = rhsContext.pos - 1
```

### Trace with both fixes

`(a|b)c`:

```
index:   0 1 2 3 4 5
input:   ( a | b ) c

parse reaches '(' at 0; groupCtx.pos starts at 0.
parseGroup: groupCtx.pos = 1.

  process 'a' at 1
    group tokens = [a]
    loop pos++ → 2

  process '|' at 2 → parseOr:
      right branch starts at 3
      process 'b' at 3
        right tokens = [b]
        loop pos++ → 4
      regex[4] == ')' → stop
      left  = uncap([a])
      right = uncap([b])
      groupCtx.pos = 4 - 1 = 3      ← fix 2
      groupCtx.tokens = [or(left, right)]

  group loop pos++ → 4
  regex[4] == ')' → exit

back in process:
  ctx.pos = groupCtx.pos = 4        ← fix 1
  append group(or(a, b))

main loop pos++ → 5
  process 'c' → append literal c

result: [group(or(a,b)), c]   ✓
```

### Bug 3 (minor): a quantifier with nothing to its left

`*a` calls `ctx.tokens[len(ctx.tokens)-1]` on an empty slice and panics. A real engine would
report a syntax error. Within the article's scope, "don't write that" is the whole error
handling.

### Note: the article's repository fixes these

The blog says the final code "will not be the exact same". The repo
([`rhaeguard/rgx`](https://github.com/rhaeguard/rgx)) does propagate the group position
(`parseContext.advTo(groupContext.loc())`) and backs up before the `)` in the alternation helper
(`advTo(groupContext.loc() - 1)`). The snippets in the blog post predate those fixes. If you type
the blog code verbatim, apply the two fixes above.

---

# Part 2 — Building the state machine

## 2.1 What a finite state machine is

A **finite state machine** (or finite automaton) is:

- a finite set of **states**
- one **start** state
- zero or more **accept** (terminal) states
- **transitions** between states, each labeled with an input symbol

You run it by feeding it symbols one at a time. Each symbol moves you along a matching
transition. If you run out of input while standing on an accept state, the input is accepted.

Example: a machine that accepts exactly `abd` and `ace`:

```
                 b          d
          ┌──► (q2) ────► ((q4))
(q0) ─a──►(q1)
          └──► (q3) ────► ((q5))
                 c          e
```

- `((...))` = accept state
- `q0` is the start
- reading `abc` gets stuck at `q2` on `c` → reject
- reading `abd` ends on `q4` → accept

This machine is **deterministic**: for a given state and a given symbol, there is exactly one
possible next state.

## 2.2 DFA vs NFA

| | DFA | NFA |
|---|---|---|
| Next state for (state, symbol) | exactly one | possibly many, or none |
| Transitions | one per symbol per state | any number per symbol per state |
| Epsilon moves | not allowed | allowed |
| Building from a regex | hard | easy |
| Running | very fast (one table lookup) | slower (must track several states) |

The article builds an **NFA with epsilon transitions** (an ε-NFA), because Thompson's
construction produces one directly from the token tree. Turning it into a DFA is possible but is
not part of this article.

## 2.3 Epsilon transitions

An **ε-transition** is an edge you can take **without consuming an input character**. It is pure
control flow.

Why do we need it? Because it lets us glue machines together without duplicating them:

- branch: `ε` to both alternatives
- loop: `ε` from the end back to the start
- skip: `ε` straight from start to end

Without ε, expressing `a*` would require copying the machine for `a` into several places.

The article uses byte `0` as the ε label:

```go
const epsilonChar uint8 = 0
```

This has a consequence: the engine cannot distinguish ε from a real NUL byte in the input. For
the email pattern this never matters, but it is a design limitation.

## 2.4 The state

```go
type state struct {
	start       bool
	terminal    bool
	transitions map[uint8][]*state
}
```

| Field | Meaning |
|---|---|
| `start` | marks the entry point (informational; the matcher never reads it) |
| `terminal` | if we are here and input is exhausted → accept |
| `transitions` | symbol → list of next states |

Why `map[uint8][]*state`?

- The alphabet is 0–255, but most states have very few outgoing edges. A map is a sparse
  representation.
- The value is a **slice** because one symbol can lead to several states (that is what makes it
  an NFA), and because ε can lead to several states.

## 2.5 The invariant that makes Thompson's construction work

> Every sub-expression compiles to a **fragment**:
> a pair of states `(entry, exit)`, with exactly one way in and one way out.
>
> When you compose fragments, you only ever touch their `entry` and `exit`.
> You never look inside.

That is why each `tokenToNfa` call returns `(*state, *state)` and nothing else. The caller does
not need to know how complex the fragment is.

Each token type has a fixed "shape". The next sections are just those shapes.

## 2.6 The top level: `toNfa`

```go
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
```

Step by step:

1. Build an NFA for the first token.
2. For each remaining token, build its NFA and connect the **previous exit** to the **new
   entry** with ε. This is concatenation.
3. Create a fresh start state with an ε edge to the first fragment's entry.
4. Create a terminal state and connect the last fragment's exit to it with ε.
5. Return the start state.

The chain looks like:

```
(S) ─ε─► [token 1] ─ε─► [token 2] ─ε─► ... ─ε─► [token n] ─ε─► (T)
```

`s.start = true` and `T.terminal = true`.

Note: this panics on an empty token list (empty regex). The article assumes there is at least
one token.

## 2.7 The skeleton: `tokenToNfa`

```go
func tokenToNfa(t *token) (*state, *state) {
	start := &state{transitions: map[uint8][]*state{}}
	end := &state{transitions: map[uint8][]*state{}}

	switch t.tokenType {
	case literal:
		// ...
	case or:
		// ...
	case bracket:
		// ...
	case group, groupUncaptured:
		// ...
	case repeat:
		// ...
	default:
		panic("unknown type of token")
	}

	return start, end
}
```

Two fresh states are allocated up front. Each case wires them up in its own way. The function
always returns `(start, end)`.

## 2.8 Literal

A literal is one edge:

```
        'a'
(s) ─────────► (e)
```

```go
case literal:
	ch := t.value.(uint8)
	start.transitions[ch] = []*state{end}
```

Read as: "from `start`, on character `ch`, go to `end`."

## 2.9 Alternation (`or`)

An `or` token holds two branches. Each branch is itself a token list (`groupUncaptured`), so it
compiles recursively.

```
             ┌── ε ──► (L.start) ══ L ══► (L.end) ── ε ──┐
(s) ─────────┤                                          ├──► (e)
             └── ε ──► (R.start) ══ R ══► (R.end) ── ε ──┘
```

- ε from `start` to both branch entries
- ε from both branch exits to `end`

```go
case or:
	values := t.value.([]token)
	left := values[0]
	right := values[1]

	s1, e1 := tokenToNfa(&left)
	s2, e2 := tokenToNfa(&right)

	start.transitions[epsilonChar] = []*state{s1, s2}
	e1.transitions[epsilonChar] = []*state{end}
	e2.transitions[epsilonChar] = []*state{end}
```

At runtime, reaching `start` means "you may go left or right". That is the whole meaning of
`|`.

## 2.10 Bracket

A bracket is a set of characters. Semantically it is `a|b|c|...`, but it does not need ε at all:
just one parallel edge per character.

```
             ┌── 'a' ──┐
(s) ─────────┼── 'b' ──┼────► (e)
             └── 'c' ──┘
```

```go
case bracket:
	literals := t.value.(map[uint8]bool)

	for l := range literals {
		start.transitions[l] = []*state{end}
	}
```

This is why the bracket representation in the parser is a set: the NFA constructor just iterates
it. Map iteration order is random in Go, but order does not matter here.

## 2.11 Group

A group is a sequence of tokens, exactly like the top level, minus the wrapper start/terminal:

```go
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
```

Note `start, end = ...`: the two states allocated at the top of the function are thrown away and
replaced with the fragment returned by the first child. A group has no machine of its own; it is
just the concatenation of its children. That is also why `group` and `groupUncaptured` share a
case: the engine does not care about captures.

## 2.12 Repetition

This is the hardest case, but it is one construction with two optional extras.

Let `A` be the machine for the repeated atom.

### The general shape

```
        ┌──────────────── ε ────────────────┐   (skip: allowed when min == 0)
        │                                   ▼
(s) ────┴── ε ──► (A.start) ══ A ══► (A.end) ── ε ──► (e)
                       ▲                                │
                       └──────────── ε ─────────────────┘   (loop: allowed when max == ∞)
```

Two independent switches:

- **`min == 0`** → add the skip edge `start → end`.
- **`max == ∞`** → add the loop edge `end → A.start`.

### The five cases

| Written | min | max | Copies built | Skip edge | Loop edge |
|---|---|---|---|---|---|
| `A*` | 0 | ∞ | 1 | yes | yes |
| `A+` | 1 | ∞ | 1 | no | yes |
| `A?` | 0 | 1 | 1 | yes | no |
| `A{m}` | m | m | m | no | no |
| `A{m,}` | m | ∞ | m | no | yes |
| `A{m,n}` | m | n | n | no (but see below) | no |

For `A{m,n}` with `n > m`, the extra `n − m` copies are made **skippable** by adding an ε edge
from the start of each optional copy directly to the final `end`:

```
(s) ─ε─► A1 ─ε─► ... ─ε─► Am ─ε─► A(m+1) ─ε─► ... ─ε─► An ─ε─► (e)
                                      │                    │
                                      └──────── ε ─────────┴──► (e)
```

So after the mandatory `m` copies, you may stop at any point.

### The code

```go
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
```

> **Edge case in the article's code:** `{0}` and `{0,0}` are wrong. `tokenToNfa(&p.token)` is
> called unconditionally before the loop, so one copy is always built, and the skip edge makes
> both "zero" and "one" acceptable. `a{0}` therefore accepts `"a"`. Guard the first copy with
> `if copyCount > 0 { ... }` if you care. The article never tests `{0}`.

The `copyCount` logic is worth memorizing:

| Case | `copyCount` | Why |
|---|---|---|
| max = ∞, min = 0 | 1 | need at least one copy to loop through |
| max = ∞, min > 0 | min | the loop edge handles "more" |
| max finite | max | all copies are built; extras are skippable |

### Critical detail: build fresh states for every copy

`tokenToNfa(&p.token)` is called inside the loop, once per copy. It must be. If you built `A`
once and reused the same state pointers `n` times, the copies would be **aliased** — advancing
inside one copy would teleport you into another. The graph would be wrong in a way that is very
hard to see.

That is not an inefficiency in the article; it is a correctness requirement.

## 2.13 Worked example: `19[5-9][0-9]`

Tokens: `literal 1`, `literal 9`, `bracket {5..9}`, `bracket {0..9}`.

The top level concatenates four fragments and wraps them:

```
S ─ε─► s1 ─'1'─► e1 ─ε─► s2 ─'9'─► e2 ─ε─► s3 ─'5'..'9'─► e3 ─ε─► s4 ─'0'..'9'─► e4 ─ε─► T
                                                                                       (( ))

S = start, T = terminal
```

State count: 4 fragments × 2 states + start + terminal = **10 states**.

Edges:

| From | Label | To |
|---|---|---|
| S | ε | s1 |
| s1 | `'1'` | e1 |
| e1 | ε | s2 |
| s2 | `'9'` | e2 |
| e2 | ε | s3 |
| s3 | `'5'`…`'9'` | e3 |
| e3 | ε | s4 |
| s4 | `'0'`…`'9'` | e4 |
| e4 | ε | T |

This is exactly the graph drawn in the article.

## 2.14 Sanity checks

Before moving to matching, verify:

1. **Every fragment has exactly one entry and one exit.**
2. **No state has two character edges with the same label.** Thompson's construction never
   produces that. (Brackets have many edges, but with different labels.)
3. **`start` has no incoming edges** (except in loops, where the loop target is the *fragment*
   entry, not the top-level start).
4. **`terminal` has no outgoing edges.**
5. **A repeat with `min == 0` has a path from entry to exit that consumes nothing.**

Check #2 matters because the matcher does `states[0]` for character transitions (see §3.3).

---

# Part 3 — Matching

## 3.1 The cursor

At any moment the matcher knows two things:

- which state it is in
- how much of the input it has consumed (`pos`)

The article represents "before the first character" as `pos = -1`, and it uses two sentinel
values when reading:

```go
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
```

So `getChar` never panics. It returns:

| `pos` | Result |
|---|---|
| `< 0` | `startOfText` (1) |
| `0 .. len-1` | the actual byte |
| `>= len` | `endOfText` (2) |

This is a hack: it means an input containing byte `0x01` or `0x02` is misinterpreted. It is also
why the matcher can be called with `-1` and not crash.

## 3.2 The contract of `check`

```go
// Is there a path from s to a terminal state that consumes exactly input[pos:]?
func (s *state) check(input string, pos int) bool
```

If the answer is "yes", the suffix `input[pos:]` matches the fragment starting at `s`.

The base case is:

> `pos` is past the end **and** `s` is terminal → **accept**.

The recursive cases are the two kinds of edges:

> - character edge: if it matches `input[pos]`, recurse at `pos + 1`
> - ε edge: recurse at the **same** `pos`

If **any** edge leads to acceptance, the answer is yes. That "any" is the definition of
non-determinism.

## 3.3 The code

```go
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
```

Line by line:

1. **Accept.** If there is no input left and this state is terminal, we are done.
2. **Consume.** Look up the current character in the transition map. If there is an edge, follow
   the first target and advance `pos`. If that path fails, fall through to the ε edges — this is
   backtracking.
3. **Epsilon.** For every ε target, try at the same position. No character is consumed.
4. **The `startOfText` hack.** When called with `pos = -1`, `ch` is `startOfText`. The first
   attempt (`state.check(input, pos)`) explores the ε-closure at `-1`, which can never accept
   because acceptance requires `endOfText`. The second attempt retries the same target at
   `pos + 1`, i.e. `0`, which lets the machine begin consuming. This exists only to make
   `check(start, -1)` work as "start here".
5. **Fail.** No edge worked.

### Why `states[0]`

A state can have multiple targets for the same character in a general NFA, but Thompson's
construction never creates them (check #2 in §2.14). So taking the first is safe **for this
subset**. If you add features later, this line becomes a bug.

## 3.4 Successful trace: `19[5-9][0-9]` on `"1950"`

```
S ─ε─► s1 ─'1'─► e1 ─ε─► s2 ─'9'─► e2 ─ε─► s3 ─'5'..'9'─► e3 ─ε─► s4 ─'0'..'9'─► e4 ─ε─► T
```

Call: `S.check("1950", -1)`.

| Step | State | `pos` | `ch` | What happens |
|---|---|---|---|---|
| 1 | S | −1 | `startOfText` | no char edge; ε → s1 |
| 2 | s1 | −1 | `startOfText` | no edge; retry via special case at pos 0 |
| 3 | s1 | 0 | `'1'` | consume → e1, pos 1 |
| 4 | e1 | 1 | `'9'` | no char edge; ε → s2 |
| 5 | s2 | 1 | `'9'` | consume → e2, pos 2 |
| 6 | e2 | 2 | `'5'` | no char edge; ε → s3 |
| 7 | s3 | 2 | `'5'` | in `5..9`; consume → e3, pos 3 |
| 8 | e3 | 3 | `'0'` | no char edge; ε → s4 |
| 9 | s4 | 3 | `'0'` | in `0..9`; consume → e4, pos 4 |
| 10 | e4 | 4 | `endOfText` | no char edge; ε → T |
| 11 | T | 4 | `endOfText` | `terminal && endOfText` → **true** |

The result bubbles all the way back up as `true`.

## 3.5 Failure trace: `"1949"`

Same pattern. Steps 1–6 are identical, arriving at `s3` with `pos = 2`, looking at `'4'`.

| Step | State | `pos` | `ch` | What happens |
|---|---|---|---|---|
| 7 | s3 | 2 | `'4'` | not in `5..9`; no char edge; s3 has no ε → false |
| 8 | e2 | 2 | — | no other ε → false |
| 9 | s2 | 1 | — | char edge already failed → false |
| 10 | e1 | 1 | — | no other ε → false |
| 11 | s1 | 0 | — | no other edge → false |
| 12 | S | −1 | — | no other ε → **false** |

Notice how failure propagates: each call returns `false`, and its caller tries the next edge.
When there are no edges left, it returns `false` too.

## 3.6 Backtracking, concretely

Use `(a|b)c` on `"bd"`:

```
        ┌── ε ──► (As) ─'a'─► (Ae) ── ε ──┐
(S) ─ε─►(Os)                                 (Oe) ── ε ──► (Cs) ─'c'─► (Ce) ── ε ──► (T)
        └── ε ──► (Bs) ─'b'─► (Be) ── ε ──┘
```

| Step | State | `pos` | `ch` | Result |
|---|---|---|---|---|
| 1 | Os | 0 | `'b'` | ε → As first |
| 2 | As | 0 | `'b'` | `'a'` ≠ `'b'` → false |
| 3 | Os | 0 | `'b'` | next ε → Bs |
| 4 | Bs | 0 | `'b'` | consume → Be, pos 1 |
| 5 | Be | 1 | `'d'` | ε → Oe |
| 6 | Oe | 1 | `'d'` | ε → Cs |
| 7 | Cs | 1 | `'d'` | `'c'` ≠ `'d'` → false |
| 8 | Oe | 1 | — | no other ε → false |
| 9 | Be | 1 | — | no other edge → false |
| 10 | Bs | 0 | — | already tried → false |
| 11 | Os | 0 | — | no more ε → false |
| 12 | S | −1 | — | false |

Step 3 is backtracking: the first branch (`a`) failed, so the matcher returns and tries the
second branch (`b`). Step 8 is backtracking again: after choosing `b`, the rest failed, but
there is nothing else to try.

The "undo" is free because `pos` is passed **by value**. Returning from a failed call restores
the caller's `pos` automatically.

## 3.7 Why this is called a backtracking matcher

The recursion is a depth-first search over the graph. At each state it picks an edge and commits
to it; if the whole subtree fails, it comes back and picks the next edge. That is backtracking.

The search tree can be large. For a pattern with `k` independent choices and input of length
`n`, the worst case can be exponential — which is why real engines that use this approach
(PCRE, JavaScript, Python's `re`) can be attacked with "catastrophic backtracking" patterns.

The article's engine avoids the *typical* blowup because Thompson's construction has no
duplicate character edges, but it is still a DFS with no memoization.

## 3.8 The article's tests

```go
func TestNfa(t *testing.T) {
	data := []struct {
		email    string
		validity bool
	}{
		// valid for this simplified pattern
		{"valid_email@example.com", true},
		{"john.doe@email.com", true},
		{"user_name@email.org", true},
		{"support@email.io", true},
		{"contact@123.com", true},
		{"sales@email.biz", true},
		{"random.email@email.xyz", true},
		{"user@domain12345.com", true},
		{"user@12345domain.com", true},

		// invalid for this simplified pattern
		{"alice.smith123@email.co.uk", false},
		{"invalid.email@", false},
		{".invalid@email.com", false},
		{"email@invalid..com", false},
		{"user@-invalid.com", false},
		{"user@in valid.com", false},
		{"user@.com", false},
		{"user@domain.c", false},
		{"user@domain.1a", false},
		{"user@domain.c0m", false},
		{"user@e_mail.com", false},
		{"user@sub.domain.com", false},
	}

	ctx := parse(`[a-zA-Z][a-zA-Z0-9_.]+@[a-zA-Z0-9]+.[a-zA-Z]{2,}`)
	nfa := toNfa(ctx)

	for _, instance := range data {
		got := nfa.check(instance.email, -1)
		if got != instance.validity {
			t.Errorf("%q: expected %t, got %t", instance.email, instance.validity, got)
		}
	}
}
```

The full table in the article has ~47 cases; the ones above cover every distinct reason for
rejection.

### Why `alice.smith123@email.co.uk` is false

The pattern's tail is:

```
. [a-zA-Z]{2,}
```

That is a literal dot followed by **letters only**. In `email.co.uk`, after the first dot comes
`co.uk`, which contains another dot. The class `[a-zA-Z]` cannot match `.`, so the match fails.
This is a limitation of the simplified pattern, not of the engine.

## 3.9 Limitations of the matcher

These follow directly from the article's design:

| Limitation | Cause | Symptom |
|---|---|---|
| Whole-string match only | `check` starts at `-1` and accepts only at `endOfText` | no substring search |
| No `^`/`$` | not parsed | they are treated as literals |
| Exponential worst case | DFS without a visited set | slow on ambiguous patterns |
| ε-cycles hang | no visited set | `(a?)*` recurses forever |
| NUL byte unusable | `epsilonChar == 0` | can't match `\x00` |
| Bytes 1 and 2 unusable | sentinels | input containing them misbehaves |
| Bytes, not runes | `uint8` and `input[pos]` | multi-byte UTF-8 is matched byte-wise |
| No captures | `group` == `groupUncaptured` | no submatches |
| No backreferences | not parsed | `\1` is `\` then `1` |
| `{0}` matches once | first copy built unconditionally | `a{0}` accepts `"a"` |

### The ε-cycle problem, concretely

Take `(a?)*`. Two edges combine into a trap:

- the `?` fragment has a **skip edge** `Ps → Pe`, because its `min` is 0
- the `*` wrapper has a **loop edge** `Se → Ps`, because its `max` is ∞
- the fragment's normal exit is `Pe → Se`

Those three ε edges form a cycle that consumes nothing:

```
(Se) ──ε──► (Ps) ──ε──► (Pe) ──ε──► (Se) ──ε──► ...
```

(The `a` path still exists — `Ps → As ─'a'→ Ae ─ε→ Pe` — but the matcher never needs it to
spin, because the skip edge is always available.)

`check(Se, pos)` calls `check(Ps, pos)`, which calls `check(Pe, pos)`, which calls
`check(Se, pos)` — forever, consuming nothing.

The article's tests never include such a pattern, so the bug is invisible. The standard fix is
to remember which `(state, pos)` pairs have already been tried:

```go
// conceptual fix, not in the article
visited := map[[2]int]bool{}
key := [2]int{stateID, pos}
if visited[key] { return false }
visited[key] = true
```

That turns the DFS into a linear-time simulation. It is the natural next step after this
article.

---

# Part 4 — Assembling the program

## 4.1 File layout

A clean way to organize the code (the article keeps it in one package):

```
regex-algo/
├── token.go     tokenType, token, repeatPayload, parseContext
├── parse.go     parse, process, parseGroup, parseBracket,
│                parseOr, parseRepeat, parseRepeatSpecified
├── nfa.go       state, epsilonChar, toNfa, tokenToNfa
├── match.go     startOfText, endOfText, getChar, check
└── main.go      demo
```

Everything lives in `package main` for a learning project.

## 4.2 A driver

```go
package main

import "fmt"

func main() {
	pattern := `[a-zA-Z][a-zA-Z0-9_.]+@[a-zA-Z0-9]+.[a-zA-Z]{2,}`

	ctx := parse(pattern)
	nfa := toNfa(ctx)

	inputs := []string{
		"valid_email@example.com",
		"john.doe@email.com",
		"alice.smith123@email.co.uk",
		"user@domain.c",
		"user@.com",
	}

	for _, in := range inputs {
		fmt.Printf("%-30s %v\n", in, nfa.check(in, -1))
	}
}
```

Expected:

```
valid_email@example.com        true
john.doe@email.com             true
alice.smith123@email.co.uk     false
user@domain.c                  false
user@.com                      false
```

## 4.3 Build order (do not skip)

1. **`token.go`** — all types. Add a `String()` method for `token` that prints an S-expression.
   You will use it constantly.
2. **`parse.go`** — literals first, then brackets, then `* + ?`, then `{m,n}`, then groups, then
   alternation. Test after each one by printing the token list.
3. **`nfa.go`** — literal, then concat, then bracket, then group, then repeat, then or.
4. **`match.go`** — implement `check`, then run the test table.
5. **Only then** consider improvements.

The most common failure mode is writing all five files and then debugging blind. Parse in
isolation; compile in isolation; match in isolation.

## 4.4 Debugging aids

**Print the tree.** A `String()` like:

```
concat(
  bracket{a-z,A-Z},
  repeat(1,inf, bracket{a-z,A-Z,0-9,_,.}),
  lit('@'),
  ...
)
```

**Print the NFA as Graphviz.** Walk the states and emit:

```
digraph G {
  S -> s1 [label="ε"];
  s1 -> e1 [label="1"];
  ...
}
```

Then `dot -Tpng nfa.dot -o nfa.png`. Comparing the picture to the article's diagrams catches
almost every construction mistake.

---

# Part 5 — Cheatsheet

## 5.1 Token types

| Token | `value` | Meaning |
|---|---|---|
| `literal` | `uint8` | one exact character |
| `bracket` | `map[uint8]bool` | one character from a set |
| `group` | `[]token` | parenthesized sequence |
| `groupUncaptured` | `[]token` | alternation branch wrapper |
| `or` | `[]token` (len 2) | `left \| right` |
| `repeat` | `repeatPayload` | `{min, max, atom}` |

## 5.2 NFA shapes

| Construct | Shape |
|---|---|
| literal `a` | one edge `start --a--> end` |
| concat `AB` | `A.end --ε--> B.start` |
| or `A\|B` | `start --ε--> A.start`, `start --ε--> B.start`, `A.end --ε--> end`, `B.end --ε--> end` |
| bracket | one parallel edge per character |
| group | concat of children, no wrapper |
| `A*` | skip edge + loop edge |
| `A+` | loop edge only |
| `A?` | skip edge only |
| `A{m}` | m copies chained |
| `A{m,}` | m copies chained + loop edge |
| `A{m,n}` | n copies chained + skip edges on copies after m |

## 5.3 Invariants

1. A fragment has exactly one entry and one exit.
2. The exit has no outgoing edges until the parent composes it.
3. Every copy of a repeated atom is built with fresh states.
4. No state has two character edges with the same label.
5. `epsilonChar == 0` means NUL cannot be matched.
6. `check` accepts only when `pos` is past the end and the state is terminal.

## 5.4 The three loops

```go
// parse: one construct at a time
for ctx.pos < len(regex) {
	process(regex, ctx)
	ctx.pos++
}

// parseGroup: same, but stop at ')'
for regex[ctx.pos] != ')' {
	process(regex, ctx)
	ctx.pos++
}

// check: DFS over the graph
if ch == endOfText && s.terminal { return true }
if next := s.transitions[ch]; len(next) > 0 { ... }
for _, st := range s.transitions[epsilonChar] { ... }
```

---

# Part 6 — Exercises

Work these in order. Each one uses only features the article implements.

1. **Trace by hand.** Parse `[0-9]{2,4}x` and write out every token and its payload. Then draw
   the NFA. Count the states.

2. **Find the bug.** Run `(a|b)` through the article's parser as written. Where does it panic?
   Apply the one-line fix and re-trace.

3. **Draw `(ab|c)*`.** How many states? Where are the ε edges? Verify invariants 1, 2, and 4.

4. **Explain the hang.** Why does `a*` terminate but `(a?)*` not? Identify the exact cycle.

5. **Predict the result.** Without running the code, decide whether these match
   `[a-z]+@[a-z]+.[a-z]{2,}`:
   - `bob@x.io`
   - `bob@x.i`
   - `bob@x.y.io`
   - `bob@.io`
   Explain each answer in terms of the token tree.

6. **Count the states.** For the email pattern, how many states does `toNfa` create? Derive it
   from the token tree instead of counting the code.

7. **Break it.** Construct the shortest input that makes the article's matcher take
   noticeably long. (Hint: you need a pattern with many independent choices, but no ε-cycles.)

---

## Final mental model

Hold these five sentences:

1. A regex is a description of a set of strings.
2. Parsing turns that description into a tree, where each quantifier wraps one atom.
3. Thompson's construction turns each tree node into a fragment with one entry and one exit,
   using ε edges to compose them.
4. Matching is a DFS: consume a character on character edges, move for free on ε edges,
   accept at the terminal state when the input is exhausted.
5. Non-determinism means "try every legal edge; if any path accepts, the string matches" — and
   that "try" is exactly the recursion.

Once those five are solid, the code is just typing.
