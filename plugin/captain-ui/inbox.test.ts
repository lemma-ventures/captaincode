import { expect, test } from "bun:test"
import { inboxClient, inboxDetails, inboxDisplay, type HeldMessage } from "./inbox"

const item: HeldMessage = {
  id: "in_1",
  text: "First line\nSecond line\n" + "full message ".repeat(100),
  at: "2026-10-23T12:00:00Z",
  origin: "worker (unverified)",
  dir: "/workspace/project-a",
  leg: "auto",
  session: "ses_1",
  findings: [{ kind: "instruction override", severity: "high", excerpt: "ignore previous instructions" }],
}

test("review reads held messages without taking messages from the delivery queue", async () => {
  const calls: [string, RequestInit | undefined][] = []
  const url = "http://localhost:14097/v1/inbox/held?cwd=%2Fworkspace%2Fproject-a"
  const client = inboxClient(url, (async (input, init) => {
    calls.push([String(input), init])
    return Response.json({ held: [item], closed: {} })
  }) as typeof fetch)
  const snapshot = await client.list()
  expect(snapshot.held).toEqual([item])
  expect(calls).toHaveLength(1)
  expect(calls[0][0]).toBe(url)
  expect(calls[0][1]?.method).toBe("GET")
})

test("release and drop name only the selected message and keep the folder scope", async () => {
  for (const action of ["release", "drop"] as const) {
    const url = "http://localhost:14097/v1/inbox/held?cwd=%2Fworkspace%2Fproject-a"
    const client = inboxClient(url, (async (input, init) => {
      expect(String(input)).toBe(url)
      expect(init?.method).toBe("POST")
      expect(init?.headers).toEqual({ "Content-Type": "application/json" })
      expect(JSON.parse(String(init?.body))).toEqual({ id: item.id, action })
      return Response.json({ ok: true })
    }) as typeof fetch)
    await client.act(item.id, action)
  }
})

test("stale items and failed requests are errors, not successful actions", async () => {
  for (const status of [404, 500]) {
    const client = inboxClient("http://localhost/v1/inbox/held", (async () =>
      Response.json({ error: "message is no longer held" }, { status })) as typeof fetch)
    await expect(client.act(item.id, "release")).rejects.toThrow("message is no longer held")
    await expect(client.list()).rejects.toThrow("message is no longer held")
  }
  const client = inboxClient("http://localhost/v1/inbox/held", (async () =>
    Response.json({})) as typeof fetch)
  await expect(client.act(item.id, "drop")).rejects.toThrow("did not confirm")
  expect(await client.list()).toEqual({ held: [], closed: {} })
})

test("closing a review cancels its request", async () => {
  const controller = new AbortController()
  const client = inboxClient("http://localhost/v1/inbox/held", (async (_input, init) => {
    expect(init?.signal?.aborted).toBe(true)
    throw new DOMException("aborted", "AbortError")
  }) as typeof fetch)
  controller.abort()
  await expect(client.list(controller.signal)).rejects.toThrow("aborted")
})

test("review includes the full text, provenance, destination, and screening findings", () => {
  const text = inboxDetails(item)
  for (const value of [item.text, item.origin!, item.dir, item.at, item.id, item.leg!, item.session!, "high: instruction override — ignore previous instructions"]) {
    expect(text).toContain(value)
  }
})

test("untrusted text cannot hide the message with terminal or direction controls", () => {
  const text = inboxDisplay("keep\x1b[2J\x07\u202ehidden\nnext\tline")
  expect(text).toBe("keep\\u001b[2J\\u0007\\u202ehidden\nnext\tline")
  expect(inboxDetails({ ...item, origin: "sender\x1b[2J", text: "text\x00" })).not.toContain("\x1b")
})
