module github.com/lemma-ventures/captaincode

go 1.24.2

// v0.3.0 to v0.3.2 can freeze the brain: a sampled jev triage shadow
// deadlocked on the brain lock. Fixed in v0.3.3.
retract [v0.3.0, v0.3.2]

require github.com/stretchr/testify v1.10.0

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
