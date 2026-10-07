/-
The program scanner (pkg/captaincode/program.go, `scanTop`) as a token
automaton.

A prompt is read as tokens: prose words, slash commands, the operators `>`
(or `->`) and `||`, parentheses, a code span in backticks (one token: code is
never syntax), and a paragraph break (a blank line).

The automaton reads left to right with three pieces of state, as `scanTop`
does:
* `atStart` - only blanks since the step began. A `(` followed by a command
  opens a group only here; a group is skipped whole, every parenthesis inside
  it counted (`matchParen`).
* `para` - a paragraph break since the last operator. An operator after one is
  prose: pasted content never becomes topology.
* `depth` - inside a group, the parenthesis depth still open.

An operator splits only outside groups, before a command (or a `(` opening on
one), with no paragraph break since the previous operator.

Proved here:
* `join_scan`        - the segments, rejoined with the operators found, are the
                       input: the scanner drops and invents nothing.
* `scan_noBoundary`  - text with no operator before a command is one segment:
                       prose never becomes topology.
* `segment_shorter`  - when the input splits, every segment is strictly
                       shorter, so parsing a group's inside again terminates.
-/
namespace CommandSafety

inductive Tok where
  | word
  | cmd
  | seq        -- ">" or "->"
  | alt        -- "||"
  | lp
  | rp
  | code       -- a backtick span
  | para       -- a blank line
  deriving DecidableEq, Repr

def isOp : Tok → Bool
  | .seq => true
  | .alt => true
  | _ => false

/-- A step starts here: a command, or `(` then a command (`commandHeadAt`). -/
def stepStart : List Tok → Bool
  | .cmd :: _ => true
  | .lp :: .cmd :: _ => true
  | _ => false

/-- The scanner's output: segments and the operators between them. -/
structure Scan where
  segs : List (List Tok)
  ops : List Tok

/-- The automaton. `d > 0`: inside a group (depth `d`). `st`: at a step start.
`pb`: a paragraph break since the last operator. `cur`: the segment being read;
`segs`/`ops`: what is closed so far. -/
def scan : Nat → Bool → Bool → List Tok → List (List Tok) → List Tok → List Tok → Scan
  | _, _, _, cur, segs, ops, [] => ⟨segs ++ [cur], ops⟩
  -- inside a group: count every parenthesis, split nothing
  | d + 1, _, _, cur, segs, ops, .lp :: ts => scan (d + 2) false false (cur ++ [.lp]) segs ops ts
  | d + 1, _, pb, cur, segs, ops, .rp :: ts => scan d false pb (cur ++ [.rp]) segs ops ts
  | d + 1, _, pb, cur, segs, ops, t :: ts => scan (d + 1) false pb (cur ++ [t]) segs ops ts
  -- top level
  | 0, st, pb, cur, segs, ops, .lp :: ts =>
      if st && stepStart (.lp :: ts) then scan 1 false pb (cur ++ [.lp]) segs ops ts
      else scan 0 false pb (cur ++ [.lp]) segs ops ts
  | 0, st, pb, cur, segs, ops, t :: ts =>
      if isOp t && stepStart ts && !pb then scan 0 true false [] (segs ++ [cur]) (ops ++ [t]) ts
      else scan 0 (st && t == .para) (pb || t == .para) (cur ++ [t]) segs ops ts

def scanAll (input : List Tok) : Scan := scan 0 true false [] [] [] input

/-- Segments rejoined with their operators: the first segment, then each
operator followed by the next segment. -/
def joinRest : List (List Tok) → List Tok → List Tok
  | [], _ => []
  | b :: rest, op :: ops => op :: (b ++ joinRest rest ops)
  | b :: rest, [] => b ++ joinRest rest []

def rejoin : List (List Tok) → List Tok → List Tok
  | [], _ => []
  | a :: rest, ops => a ++ joinRest rest ops

theorem joinRest_snoc (ss : List (List Tok)) (ops : List Tok) (cur ts : List Tok) (t : Tok)
    (h : ops.length = ss.length + 1) :
    joinRest (ss ++ [cur, ts]) (ops ++ [t]) = joinRest (ss ++ [cur ++ t :: ts]) ops := by
  induction ss generalizing ops with
  | nil =>
    match ops, h with
    | [o], _ => simp [joinRest]
  | cons s ss ih =>
    match ops, h with
    | o :: os, h =>
      have h' : os.length = ss.length + 1 := by simp at h; omega
      simp only [List.cons_append, joinRest, ih os h']

theorem rejoin_snoc (segs : List (List Tok)) (ops : List Tok) (cur ts : List Tok) (t : Tok)
    (h : ops.length = segs.length) :
    rejoin (segs ++ [cur, ts]) (ops ++ [t]) = rejoin (segs ++ [cur ++ t :: ts]) ops := by
  match segs, ops, h with
  | [], [], _ => simp [rejoin, joinRest]
  | s :: ss, o :: os, h =>
    have h' : (o :: os).length = ss.length + 1 := by simp at h ⊢; omega
    simp only [List.cons_append, rejoin]
    rw [show o :: (os ++ [t]) = (o :: os) ++ [t] by simp, joinRest_snoc ss (o :: os) cur ts t h']

/-- The automaton's invariant: what it will return rejoins to the segments
closed so far, then the current segment and the unread input. -/
theorem join_scan (d : Nat) (st pb : Bool) (cur : List Tok) (segs : List (List Tok)) (ops : List Tok)
    (ts : List Tok) (h : ops.length = segs.length) :
    rejoin (scan d st pb cur segs ops ts).segs (scan d st pb cur segs ops ts).ops =
      rejoin (segs ++ [cur ++ ts]) ops := by
  induction ts generalizing d st pb cur segs ops with
  | nil => simp [scan]
  | cons t ts ih =>
    cases d with
    | zero =>
      cases t with
      | lp =>
        simp only [scan]
        split <;> (rw [ih _ _ _ _ _ _ h]; simp)
      | _ =>
        simp only [scan]
        split
        · rw [ih _ _ _ _ _ _ (by simp [h])]
          rw [show segs ++ [cur] ++ [[] ++ ts] = segs ++ [cur, ts] by simp]
          rw [rejoin_snoc _ _ _ _ _ h]
        · rw [ih _ _ _ _ _ _ h]; simp
    | succ d =>
      cases t <;> simp only [scan] <;> rw [ih _ _ _ _ _ _ h] <;> simp

theorem join_scanAll (input : List Tok) :
    rejoin (scanAll input).segs (scanAll input).ops = input := by
  unfold scanAll
  rw [join_scan 0 true false [] [] [] input rfl]
  simp [rejoin, joinRest]

/-- No operator anywhere is followed by a step start. -/
def noBoundary : List Tok → Prop
  | [] => True
  | t :: ts => (isOp t = true → stepStart ts = false) ∧ noBoundary ts

theorem scan_noBoundary (d : Nat) (st pb : Bool) (cur : List Tok) (segs : List (List Tok)) (ops : List Tok)
    (ts : List Tok) (h : noBoundary ts) :
    scan d st pb cur segs ops ts = ⟨segs ++ [cur ++ ts], ops⟩ := by
  induction ts generalizing d st pb cur with
  | nil => simp [scan]
  | cons t ts ih =>
    obtain ⟨h1, h2⟩ := h
    cases d with
    | zero =>
      cases t with
      | lp => simp only [scan]; split <;> (rw [ih _ _ _ _ h2]; simp)
      | seq =>
        simp only [scan, isOp] at h1 ⊢
        simp only [h1 trivial, Bool.false_and, Bool.and_false]
        rw [ih _ _ _ _ h2]; simp
      | alt =>
        simp only [scan, isOp] at h1 ⊢
        simp only [h1 trivial, Bool.false_and, Bool.and_false]
        rw [ih _ _ _ _ h2]; simp
      | _ => simp only [scan, isOp, Bool.false_and, if_false]; rw [ih _ _ _ _ h2]; simp
    | succ d => cases t <;> simp only [scan] <;> rw [ih _ _ _ _ h2] <;> simp

/-- Prose never becomes topology: without an operator before a command, the
whole input is one segment. -/
theorem scanAll_noBoundary (input : List Tok) (h : noBoundary input) :
    scanAll input = ⟨[input], []⟩ := by
  simp [scanAll, scan_noBoundary 0 true false [] [] [] input h]

/-- Total length of the segments. -/
def total : List (List Tok) → Nat
  | [] => 0
  | a :: rest => a.length + total rest

theorem scan_ops_len (d : Nat) (st pb : Bool) (cur : List Tok) (segs : List (List Tok)) (ops : List Tok)
    (ts : List Tok) (h : ops.length = segs.length) :
    (scan d st pb cur segs ops ts).ops.length + 1 = (scan d st pb cur segs ops ts).segs.length := by
  induction ts generalizing d st pb cur segs ops with
  | nil => simp [scan, h]
  | cons t ts ih =>
    cases d with
    | zero =>
      cases t with
      | lp => simp only [scan]; split <;> exact ih _ _ _ _ _ _ h
      | _ =>
        simp only [scan]
        split
        · exact ih _ _ _ _ _ _ (by simp [h])
        · exact ih _ _ _ _ _ _ h
    | succ d => cases t <;> simp only [scan] <;> exact ih _ _ _ _ _ _ h

theorem length_joinRest (rest : List (List Tok)) (ops : List Tok) (h : ops.length = rest.length) :
    (joinRest rest ops).length = total rest + ops.length := by
  induction rest generalizing ops with
  | nil => simp at h; simp [joinRest, total, h]
  | cons b rest ih =>
    match ops, h with
    | o :: os, h =>
      have h' : os.length = rest.length := by simp at h; omega
      simp only [joinRest, List.length_cons, List.length_append, ih os h', total]
      omega

theorem length_rejoin (segs : List (List Tok)) (ops : List Tok) (h : ops.length + 1 = segs.length) :
    (rejoin segs ops).length = total segs + ops.length := by
  match segs, h with
  | a :: rest, h =>
    have h' : ops.length = rest.length := by simp at h; omega
    simp only [rejoin, List.length_append, length_joinRest rest ops h', total]
    omega

theorem le_total (s : List Tok) (segs : List (List Tok)) (h : s ∈ segs) : s.length ≤ total segs := by
  induction segs with
  | nil => cases h
  | cons a rest ih =>
    simp only [total]
    cases h with
    | head => omega
    | tail _ h' => have := ih h'; omega

/-- When the input splits, every segment is strictly shorter than the input:
re-parsing a segment (a group's inside) cannot loop. -/
theorem segment_shorter (input : List Tok) (s : List Tok)
    (hmem : s ∈ (scanAll input).segs) (hmany : 2 ≤ (scanAll input).segs.length) :
    s.length < input.length := by
  have hj := join_scanAll input
  have hl := scan_ops_len 0 true false [] [] [] input rfl
  simp only [scanAll] at hj hmem hmany hl
  have := length_rejoin _ _ hl
  rw [hj] at this
  have := le_total s _ hmem
  omega

end CommandSafety
