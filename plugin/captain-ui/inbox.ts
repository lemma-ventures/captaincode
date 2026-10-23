export type HeldMessage = {
  id: string
  text: string
  from?: string
  origin?: string
  at: string
  dir: string
  leg?: string
  session?: string
  findings?: { kind: string; severity?: string; excerpt?: string }[]
}

export type InboxSnapshot = { held: HeldMessage[]; closed: Record<string, string> }
export type InboxAction = "release" | "drop"

export function inboxClient(url: string, request: typeof fetch = fetch) {
  const call = async (init: RequestInit, signal?: AbortSignal) => {
    const timeout = AbortSignal.timeout(5000)
    const response = await request(url, {
      ...init,
      signal: signal ? AbortSignal.any([signal, timeout]) : timeout,
    })
    const body = await response.json() as Record<string, any>
    if (!response.ok) throw new Error(body.error || `Inbox request failed (HTTP ${response.status})`)
    return body
  }
  return {
    async list(signal?: AbortSignal): Promise<InboxSnapshot> {
      const body = await call({ method: "GET" }, signal)
      return { held: body.held ?? [], closed: body.closed ?? {} }
    },
    async act(id: string, action: InboxAction, signal?: AbortSignal) {
      const body = await call({
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ id, action }),
      }, signal)
      if (body.ok !== true) throw new Error("The brain did not confirm the inbox action")
    },
  }
}

export function inboxDisplay(text: string): string {
  return text.replace(/[\x00-\x08\x0b-\x1f\x7f-\x9f\u202a-\u202e\u2066-\u2069]/g, (c) =>
    `\\u${c.charCodeAt(0).toString(16).padStart(4, "0")}`)
}

export function inboxDetails(item: HeldMessage): string {
  return inboxDisplay([
    `From: ${item.origin || item.from || "unnamed sender"}`,
    `Received: ${item.at}`,
    `Folder: ${item.dir}`,
    `Message: ${item.id}`,
    ...(item.leg ? [`Leg: ${item.leg}`] : []),
    ...(item.session ? [`Session: ${item.session}`] : []),
    "",
    "Screening findings:",
    ...(item.findings?.length
      ? item.findings.map((f) => `${f.severity || "flagged"}: ${f.kind}${f.excerpt ? ` — ${f.excerpt}` : ""}`)
      : ["No findings recorded."]),
    "",
    "Message text:",
    item.text,
  ].join("\n"))
}
