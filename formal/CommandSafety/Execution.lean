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
* `run_budget`, `run_turns_le` - with one budget shared by a command and every
  thread it starts, the turns run never exceed the budget, however the
  command nests.
Termination itself is by construction: Lean accepts `turnsNow` and `run`
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

/-- Run `g` k times, threading the budget. -/
def iter : Nat → (Nat → Nat × Nat) → Nat → Nat × Nat
  | 0, _, f => (f, 0)
  | k + 1, g, f =>
    let r := g f
    let r2 := iter k g r.1
    (r2.1, r.2 + r2.2)

/-- Run with a shared budget `f`: (budget left, turns run). A turn that finds
the budget empty does not start; the rest of the tree then runs nothing. -/
def run : Cmd → Nat → Nat × Nat
  | .turn, f => if f = 0 then (0, 0) else (f - 1, 1)
  | .rep n body, f =>
    if f = 0 then (0, 0) else
      let r := iter (rounds n) (run body) (f - 1)
      (r.1, r.2 + 1)
  | .seq a b, f =>
    let r1 := run a f
    let r2 := run b r1.1
    (r2.1, r1.2 + r2.2)

/-- A runner respects the budget: turns run plus budget left never exceed
the budget it was given. -/
def Respects (g : Nat → Nat × Nat) : Prop := ∀ f, (g f).2 + (g f).1 ≤ f

theorem iter_respects (g : Nat → Nat × Nat) (hg : Respects g) :
    ∀ k, Respects (iter k g) := by
  intro k
  induction k with
  | zero => intro f; simp [iter]
  | succ k ih =>
    intro f
    have h1 := hg f
    have h2 := ih (g f).1
    simp only [iter]
    omega

theorem run_budget : ∀ c : Cmd, Respects (run c) := by
  intro c
  induction c with
  | turn => intro f; simp only [run]; split <;> omega
  | rep n body ih =>
    intro f
    simp only [run]
    split
    · omega
    · have := iter_respects (run body) ih (rounds n) (f - 1)
      simp only at this ⊢
      omega
  | seq a b iha ihb =>
    intro f
    have h1 := iha f
    have h2 := ihb (run a f).1
    simp only [run]
    omega

/-- However a command nests, it runs at most `budget` turns. -/
theorem run_turns_le (c : Cmd) (budget : Nat) : (run c budget).2 ≤ budget := by
  have := run_budget c budget
  omega

/-- The budget does not cut a command that fits in it: a plain chain of
turns runs every step. -/
theorem run_seq_turns (f : Nat) (h : 2 ≤ f) : (run (.seq .turn .turn) f).2 = 2 := by
  simp only [run]
  split <;> split <;> omega

end CommandSafety
