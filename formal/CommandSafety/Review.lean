namespace ReviewPolicy

structure Candidate where
  id : Nat
  allowed : Bool
  tierMatches : Bool
  vendorAllowed : Bool
  toolFree : Bool
  deriving Repr, DecidableEq

def eligible (c : Candidate) : Bool :=
  c.allowed && c.tierMatches && c.vendorAllowed && c.toolFree

def plan (budget : Nat) (xs : List Candidate) : List Candidate :=
  (xs.filter eligible).take budget

theorem plan_member (c : Candidate) (h : c ∈ plan n xs) : c ∈ xs := by
  have hf := List.mem_of_mem_take h
  exact (List.mem_filter.mp hf).1

theorem plan_eligible (c : Candidate) (h : c ∈ plan n xs) : eligible c = true := by
  have hf := List.mem_of_mem_take h
  exact (List.mem_filter.mp hf).2

theorem plan_allowed (c : Candidate) (h : c ∈ plan n xs) : c.allowed = true := by
  have he := plan_eligible c h
  simp only [eligible, Bool.and_eq_true] at he
  exact he.1.1.1

theorem plan_bound : (plan n xs).length ≤ n := by
  simp only [plan, List.length_take]
  exact Nat.min_le_left _ _

theorem fallback_eligible (c : Candidate) (h : c ∈ (plan n xs).drop k) :
    eligible c = true := by
  exact plan_eligible c (List.mem_of_mem_drop h)

#print axioms plan_member
#print axioms plan_eligible
#print axioms plan_allowed
#print axioms plan_bound
#print axioms fallback_eligible

end ReviewPolicy
