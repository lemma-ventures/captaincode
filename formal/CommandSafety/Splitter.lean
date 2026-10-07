/-
The chain splitter (pkg/captaincode/chain.go, `SplitChain`) as a token
automaton.

A prompt is read as tokens: prose words, slash commands, the connector `>`
(or `->`), and parentheses. The automaton keeps one counter, the group depth:
at depth 0 a `>` followed by the start of a step closes the current step; a
`(` followed by a slash command opens a group, inside which every
parenthesis is counted and no `>` splits.

Proved here:
* `join_split`    - the steps, rejoined with `>`, are exactly the input: the
                    splitter drops and invents nothing.
* `split_noBoundary` - text with no `>` before a step start is one step:
                    prose never becomes topology.
* `step_shorter`  - when the input splits, every step is strictly shorter, so
                    re-splitting a step (a nested group) terminates.
-/
namespace CommandSafety

inductive Tok where
  | word                      -- prose
  | cmd (runsWork : Bool)     -- a slash command; runsWork: a leg, lane, /team, /repeat…
  | gt                        -- ">" or "->"
  | lp
  | rp
  deriving DecidableEq, Repr

/-- A step starts here: a command that runs work, or `(` then a command. -/
def stepStart : List Tok → Bool
  | .cmd true :: _ => true
  | .lp :: .cmd _ :: _ => true
  | _ => false

/-- A `(` here opens a group: it is followed by a slash command. -/
def opensGroup : List Tok → Bool
  | .cmd _ :: _ => true
  | _ => false

/-- The automaton. `d` is the group depth, `cur` the step being read, `done`
the steps already closed. One token per transition. -/
def scan : Nat → List Tok → List (List Tok) → List Tok → List (List Tok)
  | _, cur, done, [] => done ++ [cur]
  | 0, cur, done, .gt :: ts =>
      if stepStart ts then scan 0 [] (done ++ [cur]) ts
      else scan 0 (cur ++ [.gt]) done ts
  | 0, cur, done, .lp :: ts =>
      if opensGroup ts then scan 1 (cur ++ [.lp]) done ts
      else scan 0 (cur ++ [.lp]) done ts
  | d + 1, cur, done, .lp :: ts => scan (d + 2) (cur ++ [.lp]) done ts
  | d + 1, cur, done, .rp :: ts => scan d (cur ++ [.rp]) done ts
  | d, cur, done, t :: ts => scan d (cur ++ [t]) done ts

def split (input : List Tok) : List (List Tok) := scan 0 [] [] input

/-- Steps rejoined with the connector. -/
def join : List (List Tok) → List Tok
  | [] => []
  | [a] => a
  | a :: rest => a ++ Tok.gt :: join rest

theorem join_cons_cons (a b : List Tok) (rest : List (List Tok)) :
    join (a :: b :: rest) = a ++ Tok.gt :: join (b :: rest) := rfl

/-- Closing a step at a connector: `xs ++ [a, b]` joins like `xs ++ [a ++ > ++ b]`. -/
theorem join_close (xs : List (List Tok)) (a b : List Tok) :
    join (xs ++ [a, b]) = join (xs ++ [a ++ Tok.gt :: b]) := by
  induction xs with
  | nil => simp [join]
  | cons x xs ih =>
    cases xs with
    | nil => simp [join]
    | cons y ys =>
      simp only [List.cons_append] at ih ⊢
      rw [join_cons_cons, join_cons_cons, ih]

/-- The automaton's invariant: what it will return rejoins to the steps
closed so far followed by the current step and the unread input. -/
theorem join_scan (d : Nat) (cur : List Tok) (done : List (List Tok)) (ts : List Tok) :
    join (scan d cur done ts) = join (done ++ [cur ++ ts]) := by
  induction ts generalizing d cur done with
  | nil => simp [scan]
  | cons t ts ih =>
    cases d with
    | zero =>
      cases t with
      | gt =>
        simp only [scan]
        split
        · rw [ih]; simp only [List.append_assoc, List.singleton_append, List.nil_append]
          rw [← join_close]
        · rw [ih]; simp
      | lp =>
        simp only [scan]
        split <;> (rw [ih]; simp)
      | word => simp only [scan]; rw [ih]; simp
      | cmd b => simp only [scan]; rw [ih]; simp
      | rp => simp only [scan]; rw [ih]; simp
    | succ d =>
      cases t <;> simp only [scan] <;> rw [ih] <;> simp

theorem join_split (input : List Tok) : join (split input) = input := by
  simp [split, join_scan, join]

/-- No `>` anywhere is followed by a step start. -/
def noBoundary : List Tok → Prop
  | [] => True
  | .gt :: ts => stepStart ts = false ∧ noBoundary ts
  | _ :: ts => noBoundary ts

theorem scan_noBoundary (d : Nat) (cur : List Tok) (done : List (List Tok)) (ts : List Tok)
    (h : noBoundary ts) : scan d cur done ts = done ++ [cur ++ ts] := by
  induction ts generalizing d cur done with
  | nil => simp [scan]
  | cons t ts ih =>
    cases d with
    | zero =>
      cases t with
      | gt =>
        simp only [noBoundary] at h
        simp only [scan, h.1, Bool.false_eq_true, if_false]
        rw [ih _ _ _ h.2]; simp
      | lp =>
        simp only [noBoundary] at h
        simp only [scan]; split <;> (rw [ih _ _ _ h]; simp)
      | word => simp only [noBoundary] at h; simp only [scan]; rw [ih _ _ _ h]; simp
      | cmd b => simp only [noBoundary] at h; simp only [scan]; rw [ih _ _ _ h]; simp
      | rp => simp only [noBoundary] at h; simp only [scan]; rw [ih _ _ _ h]; simp
    | succ d =>
      cases t <;> simp only [noBoundary] at h <;> simp only [scan] <;>
        first
        | (rw [ih _ _ _ h]; simp)
        | (rw [ih _ _ _ h.2]; simp)

/-- Prose never becomes topology: without a `>` before a step start, the
whole input is one step. -/
theorem split_noBoundary (input : List Tok) (h : noBoundary input) :
    split input = [input] := by
  simp [split, scan_noBoundary 0 [] [] input h]

/-- Total length of the steps. -/
def total : List (List Tok) → Nat
  | [] => 0
  | a :: rest => a.length + total rest

theorem length_join (a : List Tok) (rest : List (List Tok)) :
    (join (a :: rest)).length = total (a :: rest) + rest.length := by
  induction rest generalizing a with
  | nil => simp [join, total]
  | cons b rest ih =>
    rw [join_cons_cons]
    simp only [List.length_append, List.length_cons, ih, total]
    omega

theorem le_total (s : List Tok) (steps : List (List Tok)) (h : s ∈ steps) :
    s.length ≤ total steps := by
  induction steps with
  | nil => cases h
  | cons a rest ih =>
    simp only [total]
    cases h with
    | head => omega
    | tail _ h' => have := ih h'; omega

/-- When the input splits into two or more steps, each step is strictly
shorter than the input: re-splitting a step cannot loop. -/
theorem step_shorter (input : List Tok) (s : List Tok)
    (hmem : s ∈ split input) (hmany : 2 ≤ (split input).length) :
    s.length < input.length := by
  have hj := join_split input
  match hs : split input with
  | [] => simp [hs] at hmany
  | [_] => simp [hs] at hmany
  | a :: b :: rest =>
    rw [hs] at hj hmem
    have hlen := length_join a (b :: rest)
    rw [hj] at hlen
    have := le_total s _ hmem
    simp only [List.length_cons] at hlen
    omega

end CommandSafety
