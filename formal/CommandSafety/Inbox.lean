/-
The inbox (`captain send`, POST /v1/inbox) as an automaton.

Workers may run `captain send "<prompt>"`; the TUI submits the prompt as if
the user had typed it. Its events: the user types a turn, or a worker sends
a prompt, which may start a loop (a /repeat or a chain).

Proved here:
* `now_unbounded`, `now_loops_unbounded` - in the code before this change, a
  run with no user input can accept any number of sends, each starting a
  loop: a worker that sends a prompt telling the next worker to send again
  never stops, and a misled worker can start loops the user never typed.
* `fixed_accepted_le`, `fixed_no_loops` - with the change, between two user
  turns at most `quota` sends are accepted, and none of them starts a loop.
-/
namespace CommandSafety

inductive Event where
  | human
  | send (startsLoop : Bool)

/-- Sends accepted since the user last typed, and how many of them started
a loop. -/
structure Count where
  accepted : Nat
  loops : Nat

/-- Before: every send is accepted. -/
def stepNow (s : Count) : Event → Count
  | .human => ⟨0, 0⟩
  | .send loop => ⟨s.accepted + 1, s.loops + (if loop then 1 else 0)⟩

def runNow (events : List Event) : Count := events.foldl stepNow ⟨0, 0⟩

theorem foldl_now_sends (k : Nat) (s : Count) :
    (List.replicate k (Event.send true)).foldl stepNow s = ⟨s.accepted + k, s.loops + k⟩ := by
  induction k generalizing s with
  | zero => simp
  | succ k ih =>
    rw [List.replicate_succ, List.foldl_cons, ih]
    simp only [stepNow, if_true, Count.mk.injEq]
    omega

/-- With no user input at all, any number of sends is accepted. -/
theorem now_unbounded (M : Nat) :
    ∃ events : List Event, (∀ e ∈ events, e ≠ .human) ∧ M < (runNow events).accepted := by
  refine ⟨List.replicate (M + 1) (.send true), ?_, ?_⟩
  · intro e he
    rw [List.eq_of_mem_replicate he]
    intro h; cases h
  · simp [runNow, foldl_now_sends]

/-- …and every one of them may start a loop the user never typed. -/
theorem now_loops_unbounded (M : Nat) :
    ∃ events : List Event, (∀ e ∈ events, e ≠ .human) ∧ M < (runNow events).loops := by
  refine ⟨List.replicate (M + 1) (.send true), ?_, ?_⟩
  · intro e he
    rw [List.eq_of_mem_replicate he]
    intro h; cases h
  · simp [runNow, foldl_now_sends]

/-- After: a quota of sends, refilled only when the user types; a send that
starts a loop is refused. -/
structure Inbox where
  quota : Nat
  accepted : Nat
  loops : Nat

def stepFixed (quota : Nat) (s : Inbox) : Event → Inbox
  | .human => ⟨quota, 0, s.loops⟩
  | .send loop =>
    if loop then s
    else if s.quota = 0 then s
    else ⟨s.quota - 1, s.accepted + 1, s.loops⟩

/-- Between user turns, sends accepted plus quota left never grow, and no
loop is ever accepted. -/
theorem fixed_invariant (q : Nat) (events : List Event) (h : ∀ e ∈ events, e ≠ .human) :
    ∀ s : Inbox,
      (events.foldl (stepFixed q) s).accepted + (events.foldl (stepFixed q) s).quota
        ≤ s.accepted + s.quota ∧
      (events.foldl (stepFixed q) s).loops = s.loops := by
  induction events with
  | nil => intro s; simp
  | cons e es ih =>
    intro s
    have hes : ∀ e ∈ es, e ≠ .human := fun e he => h e (List.mem_cons_of_mem _ he)
    have he : e ≠ .human := h e (List.mem_cons_self ..)
    rw [List.foldl_cons]
    cases e with
    | human => exact absurd rfl he
    | send loop =>
      have ⟨h1, h2⟩ := ih hes (stepFixed q s (.send loop))
      refine ⟨?_, ?_⟩
      · refine Nat.le_trans h1 ?_
        simp only [stepFixed]
        split
        · omega
        · split <;> simp <;> omega
      · rw [h2]
        simp only [stepFixed]
        split
        · rfl
        · split <;> rfl

/-- After the user types, at most `quota` sends are accepted before they
type again. -/
theorem fixed_accepted_le (q : Nat) (s : Inbox) (events : List Event)
    (h : ∀ e ∈ events, e ≠ .human) :
    (events.foldl (stepFixed q) (stepFixed q s .human)).accepted ≤ q := by
  have := (fixed_invariant q events h (stepFixed q s .human)).1
  have hs : stepFixed q s .human = ⟨q, 0, s.loops⟩ := rfl
  rw [hs] at this ⊢
  simp only at this
  omega

/-- No send ever starts a loop. -/
theorem fixed_no_loops (q : Nat) (events : List Event) :
    (events.foldl (stepFixed q) ⟨q, 0, 0⟩).loops = 0 := by
  suffices ∀ s : Inbox, (events.foldl (stepFixed q) s).loops = s.loops by
    simpa using this ⟨q, 0, 0⟩
  induction events with
  | nil => intro s; rfl
  | cons e es ih =>
    intro s
    rw [List.foldl_cons, ih]
    cases e with
    | human => rfl
    | send loop =>
      simp only [stepFixed]
      split
      · rfl
      · split <;> rfl

end CommandSafety
