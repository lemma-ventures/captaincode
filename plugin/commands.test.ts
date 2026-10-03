import { expect, test } from "bun:test"
import { unknownCommand } from "./commands"

const known = new Set(["frontier", "quality", "save", "speed", "team", "codex", "codex-cli", "claude", "grok", "grok-max", "repeat", "btw", "noslop", "openshell", "fast"])

test("a typo is refused with the nearest command", () => {
  expect(unknownCommand("/fontier summarize the rounds", known)).toBe(
    "captain: /fontier is not a command - nothing was sent. Did you mean /frontier? To send it as plain text, remove the slash.",
  )
  expect(unknownCommand("/codex-ai fix it", known)).toContain("Did you mean /codex or /codex-cli?")
})

test("known words, opencode's words, custom commands and paths go through", () => {
  expect(unknownCommand("/frontier do it", known)).toBeNull()
  expect(unknownCommand("/Quality do it", known)).toBeNull()
  expect(unknownCommand("/team /noslop do it", known)).toBeNull()
  expect(unknownCommand("/compact", known)).toBeNull()
  expect(unknownCommand("/review the diff", known, ["review"])).toBeNull()
  expect(unknownCommand("/mycmd go", known, ["mycmd"])).toBeNull()
  expect(unknownCommand("/Users/me/notes.md is wrong", known)).toBeNull()
  expect(unknownCommand("fix /fontier in the docs", known)).toBeNull()
})

test("no list from the brain refuses nothing", () => {
  expect(unknownCommand("/fontier x", null)).toBeNull()
})

test("an unknown word with no near match lists the lanes", () => {
  expect(unknownCommand("/xyzzy go", known)).toContain("Lanes: /quality /frontier")
})
