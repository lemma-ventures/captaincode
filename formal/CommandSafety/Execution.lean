/-
How many turns a command runs.

A command is a tree: a single turn (a solo leg, a lane, /frontier, /team or a
CWL workflow - each runs a bounded number of workers), `/repeat n body`, or a
chain step followed by the rest of the chain (`a > b`). Each /repeat round
and each chain step is a dispatched turn, and a round's text may itself be
a /repeat or a chain, so commands nest.

Proved here:
* `turnsNow_nest`, `turnsNow_unbounded` - without a shared budget (the code
  before this change), nesting multiplies: k nested `/repeat`s run at least
  100^k turns, so no constant bounds the work one prompt can start.
* `runP_budget`, `runP_turns_le` - a program (chains, `||` fallbacks, `/repeat
  … until:` loops, gated turns with one repair) shares one turn budget: it
  never dispatches more turns than the budget, however it nests and whatever
  succeeds or fails. `nestP_bounded`: nested `/repeat`s are such a program.
Termination itself is by construction: Lean accepts `turnsNow` and `runP`
only because they are structurally recursive.
-/
namespace CommandSafety

/-- Rounds of one /repeat: `n`, or the hard cap when `n` is 0 (until stopped)
or above it (CAPTAIN_REPEAT_MAX, default 100). -/
def repeatCap : Nat := 100

def rounds (n : Nat) : Nat := if n = 0 ∨ repeatCap < n then repeatCap else n

theorem rounds_le (n : Nat) : rounds n ≤ repeatCap := by
  unfold rounds; split <;> omega

inductive Cmd where
  | turn
  | rep (n : Nat) (body : Cmd)
  | seq (a b : Cmd)

/-- Turns run by the code before this change: every round and step runs its
body in full. -/
def turnsNow : Cmd → Nat
  | .turn => 1
  | .rep n body => 1 + rounds n * turnsNow body
  | .seq a b => turnsNow a + turnsNow b

/-- `/repeat /repeat … /repeat x`, k levels deep. -/
def nest : Nat → Cmd
  | 0 => .turn
  | k + 1 => .rep 0 (nest k)

theorem turnsNow_nest (k : Nat) : repeatCap ^ k ≤ turnsNow (nest k) := by
  induction k with
  | zero => simp [nest, turnsNow]
  | succ k ih =>
    simp only [nest, turnsNow, rounds, repeatCap] at ih ⊢
    simp only [true_or, if_true, Nat.pow_succ]
    omega

theorem lt_pow (k : Nat) : k < repeatCap ^ k := by
  induction k with
  | zero => simp [repeatCap]
  | succ k ih =>
    rw [Nat.pow_succ]
    simp only [repeatCap] at ih ⊢
    omega

/-- No constant bounds the turns one typed prompt starts, before the budget. -/
theorem turnsNow_unbounded (M : Nat) : ∃ c : Cmd, M < turnsNow c :=
  ⟨nest M, Nat.lt_of_lt_of_le (lt_pow M) (turnsNow_nest M)⟩

/-! ## Programs with a shared turn budget

A program (pkg/captaincode/program.go, run by cmd/captaincode/brain_program.go)
is a tree of turns: chains (`a > b`, a failed step stops the chain),
fallbacks (`a || b`, `b` runs only when `a` failed), and loops (`/repeat N
body until: check`). A gated turn may take one repair turn. Every turn the
program dispatches spends one unit of the program's budget
(`CAPTAIN_REPEAT_MAX`); a turn that finds the budget empty does not run.

Whether a turn succeeds, and whether an `until:` check passes, is up to the
world: the theorems below hold for every `ok` oracle, so no pattern of
successes and failures can make a program run past its budget. -/

inductive Prog where
  | turn (gated : Bool)
  | seq (a b : Prog)
  | alt (a b : Prog)
  | loop (n : Nat) (untilCheck : Bool) (body : Prog)

/-- The outcome of running a node: budget left, turns run, success. -/
structure Out where
  left : Nat
  turns : Nat
  ok : Bool

/-- Run `g` for up to `k` rounds, threading the budget. A round that finds the
budget empty stops the loop; a passing `until:` check stops it too. -/
def rounds' (ok : Nat → Bool) (untilCheck : Bool) : Nat → (Nat → Out) → Nat → Out
  | 0, _, f => ⟨f, 0, true⟩
  | k + 1, g, f =>
    if f = 0 then ⟨0, 0, false⟩ else
      let r := g f
      if untilCheck && ok r.left then ⟨r.left, r.turns, true⟩ else
        let r2 := rounds' ok untilCheck k g r.left
        ⟨r2.left, r.turns + r2.turns, r2.ok⟩

/-- Run a program with budget `f`. `ok` says, from the budget left, whether
the turn (or the check) at that point succeeds. -/
def runP (ok : Nat → Bool) : Prog → Nat → Out
  | .turn gated, f =>
    if f = 0 then ⟨0, 0, false⟩
    else if ok f then ⟨f - 1, 1, true⟩
    else if gated && f ≥ 2 then ⟨f - 2, 2, ok (f - 1)⟩  -- one repair turn
    else ⟨f - 1, 1, false⟩
  | .seq a b, f =>
    let r1 := runP ok a f
    if r1.ok then
      let r2 := runP ok b r1.left
      ⟨r2.left, r1.turns + r2.turns, r2.ok⟩
    else r1
  | .alt a b, f =>
    let r1 := runP ok a f
    if r1.ok then r1 else
      let r2 := runP ok b r1.left
      ⟨r2.left, r1.turns + r2.turns, r2.ok⟩
  | .loop n untilCheck body, f => rounds' ok untilCheck (rounds n) (runP ok body) f

/-- A runner respects the budget: turns run plus budget left never exceed the
budget it was given. -/
def RespectsP (g : Nat → Out) : Prop := ∀ f, (g f).turns + (g f).left ≤ f

theorem rounds'_respects (ok : Nat → Bool) (u : Bool) (g : Nat → Out) (hg : RespectsP g) :
    ∀ k, RespectsP (rounds' ok u k g) := by
  intro k
  induction k with
  | zero => intro f; simp [rounds']
  | succ k ih =>
    intro f
    have h1 := hg f
    have h2 := ih (g f).left
    simp only [rounds']
    split
    · simp
    · split
      · simp only; omega
      · simp only; omega

theorem runP_budget (ok : Nat → Bool) : ∀ p : Prog, RespectsP (runP ok p) := by
  intro p
  induction p with
  | turn gated =>
    intro f
    simp only [runP]
    split
    · simp
    · split
      · simp only; omega
      · split
        · rename_i hg
          simp only [Bool.and_eq_true, decide_eq_true_eq] at hg
          simp only; omega
        · simp only; omega
  | seq a b iha ihb =>
    intro f
    have h1 := iha f
    have h2 := ihb (runP ok a f).left
    simp only [runP]
    split
    · simp only; omega
    · exact h1
  | alt a b iha ihb =>
    intro f
    have h1 := iha f
    have h2 := ihb (runP ok a f).left
    simp only [runP]
    split
    · exact h1
    · simp only; omega
  | loop n u body ih =>
    intro f
    simp only [runP]
    exact rounds'_respects ok u _ ih (rounds n) f

/-- However a program nests chains, fallbacks and loops, and whatever succeeds
or fails, it dispatches at most `budget` turns. -/
theorem runP_turns_le (ok : Nat → Bool) (p : Prog) (budget : Nat) :
    (runP ok p budget).turns ≤ budget := by
  have := runP_budget ok p budget
  omega

/-- `/repeat /repeat /repeat x`, k levels deep, is a program now: its loops
share one budget, so it runs at most `budget` turns, where the same text once
ran 100^k (`turnsNow_nest`). -/
def nestP : Nat → Prog
  | 0 => .turn false
  | k + 1 => .loop 0 false (nestP k)

theorem nestP_bounded (ok : Nat → Bool) (k budget : Nat) :
    (runP ok (nestP k) budget).turns ≤ budget := runP_turns_le ok _ _

/-- The budget does not cut a program that fits in it: a fallback whose first
alternative succeeds runs one turn. -/
theorem runP_alt_first (budget : Nat) (h : 1 ≤ budget) :
    (runP (fun _ => true) (.alt (.turn false) (.turn false)) budget).turns = 1 := by
  simp only [runP]
  split <;> simp_all

end CommandSafety
