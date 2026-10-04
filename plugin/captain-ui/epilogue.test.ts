import { expect, test } from "bun:test"
import { captainEpilogue, hookEpilogue, resumeCommand, rewriteEpilogue } from "./epilogue"

// The chunk stock opencode 1.18.34 writes on exit (its wordmark trimmed to one row).
const stock = [
  "  \x1b[90m█▀▀█\x1b[0m \x1b[0m█▀▀█\x1b[0m",
  "",
  "  \x1b[90mSession   \x1b[0m\x1b[1mFix the exit screen\x1b[0m",
  "  \x1b[90mContinue  \x1b[0m\x1b[1mopencode -s ses_2a7f0c1dffeAbC9\x1b[0m",
  "",
].join("\n") + "\n"

test("the stock exit chunk becomes Captain Code's", () => {
  const out = rewriteEpilogue(stock, "~/Gits/captaincode/captaincode.sh")!
  expect(out).toBeDefined()
  expect(out).not.toContain("opencode -s")
  expect(out).toContain("Fix the exit screen")
  expect(out).toContain("~/Gits/captaincode/captaincode.sh -s ses_2a7f0c1dffeAbC9")
  expect(out).toContain("\x1b[38;2;92;156;245m█") // CAPTAIN blue
  expect(out.endsWith("\n")).toBe(true)
})

test("other writes are left alone", () => {
  expect(rewriteEpilogue("plain output\n", "x")).toBeUndefined()
  expect(rewriteEpilogue("run opencode -s ses_1 to resume\n", "x")).toBeUndefined()
  expect(rewriteEpilogue("\x1b[2J\x1b[H frame", "x")).toBeUndefined()
})

test("a title cannot inject escape sequences, a bad id is not printed", () => {
  const out = captainEpilogue({ title: "evil\x1b]0;pwned\x07", sessionID: "ses_1", command: "c" })
  expect(out).not.toContain("\x1b]0;")
  expect(out).not.toContain("\x07")
  expect(captainEpilogue({ title: "t", sessionID: "ses;rm -rf ~", command: "c" })).not.toContain("Continue")
  expect(captainEpilogue({ title: "t", command: "c" })).not.toContain("Continue")
})

test("the resume command names the launcher, short under HOME", () => {
  expect(resumeCommand({ CAPTAIN_LAUNCHER: "/Users/a/cc/captaincode.sh", HOME: "/Users/a" })).toBe("~/cc/captaincode.sh")
  expect(resumeCommand({ CAPTAIN_LAUNCHER: "/opt/cc/captaincode.sh", HOME: "/Users/a" })).toBe("/opt/cc/captaincode.sh")
  expect(resumeCommand({})).toBe("opencode")
  expect(resumeCommand({ CAPTAIN_LAUNCHER: "/x\x1b[2J" })).toBe("opencode")
})

test("the hook rewrites one chunk, then restores the original write", () => {
  const seen: string[] = []
  const original = (chunk: any) => (seen.push(String(chunk)), true)
  const stream = { write: original as (chunk: any, ...rest: any[]) => boolean }
  hookEpilogue(stream, "captaincode.sh")
  hookEpilogue(stream, "captaincode.sh") // a reload does not wrap twice
  stream.write("frame")
  stream.write(stock)
  expect(stream.write).toBe(original)
  expect(seen[0]).toBe("frame")
  expect(seen[1]).toContain("captaincode.sh -s ses_2a7f0c1dffeAbC9")
  expect(seen[1]).not.toContain("opencode -s")
})

test("the hook survives the renderer putting its saved write back", () => {
  const seen: string[] = []
  const real = (chunk: any) => (seen.push("real:" + String(chunk)), true)
  const intercept = (chunk: any) => (seen.push("captured:" + String(chunk)), true)
  const stream = { write: real as (chunk: any, ...rest: any[]) => boolean }
  hookEpilogue(stream, "captaincode.sh")
  stream.write = intercept // the renderer starts
  stream.write = real // the renderer is destroyed
  stream.write(stock)
  expect(seen).toHaveLength(1)
  expect(seen[0]).toStartWith("real:")
  expect(seen[0]).toContain("captaincode.sh -s ses_2a7f0c1dffeAbC9")
  expect(stream.write).toBe(real)
})
