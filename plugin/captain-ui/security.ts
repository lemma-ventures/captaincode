export type SecurityStatus = {
  workspace: string
  mode: string
  mode_valid: boolean
  claude_hook?: boolean
  snapshot_id?: string
  snapshot_age_seconds?: number
  snapshot_ok: boolean
  total: number
  dispositions: Record<string, number>
  hosts: Record<string, number>
  uncovered: string[]
  recent: Record<string, unknown>[]
}

function validCounts(value: unknown): value is Record<string, number> {
  return !!value && typeof value === "object" && !Array.isArray(value) && Object.values(value).every((n) => Number.isInteger(n) && n >= 0)
}

function validStatus(value: any): value is SecurityStatus {
  return value && typeof value.workspace === "string" && /^ws_[a-f0-9]{16}$/.test(value.workspace) &&
    typeof value.mode === "string" && typeof value.mode_valid === "boolean" && typeof value.snapshot_ok === "boolean" &&
    Number.isInteger(value.total) && value.total >= 0 && validCounts(value.dispositions) && validCounts(value.hosts) &&
    Array.isArray(value.uncovered) && value.uncovered.length <= 8 && value.uncovered.every((v: unknown) => typeof v === "string") &&
    Array.isArray(value.recent) && value.recent.length <= 20
}

export function securityClient(url: string, request: typeof fetch = fetch) {
  return {
    async read(signal?: AbortSignal): Promise<SecurityStatus> {
      const timeout = AbortSignal.timeout(2500)
      const response = await request(url, { method: "GET", signal: signal ? AbortSignal.any([signal, timeout]) : timeout })
      const body = await response.json()
      if (!response.ok) throw new Error(body?.error || `Security status failed (HTTP ${response.status})`)
      if (!validStatus(body)) throw new Error("Invalid security status")
      return body
    },
  }
}

function display(value: string): string {
  return value.replace(/[\x00-\x1f\x7f-\x9f\u202a-\u202e\u2066-\u2069]/g, "").slice(0, 120)
}

export function securitySummary(status: SecurityStatus): string {
  const safeMode = display(status.mode)
  const snapshot = status.snapshot_ok ? "" : " · snapshot unreadable"
  if (!status.mode_valid) return `unknown mode ${safeMode}${snapshot}`
  const uncovered = status.uncovered.length ? ` · ${display(status.uncovered.join(", "))} uncovered` : ""
  if (status.mode === "enforce") {
    if (status.total === 0) return `enforcing · no events in this folder${snapshot}${uncovered}`
    const refusals = [
      ["blocked", "blocked"],
      ["overridden", "overridden"],
      ["security_check_unavailable", "check unavailable"],
      ["degraded", "degraded"],
    ].filter(([key]) => (status.dispositions[key] ?? 0) > 0).map(([key, label]) => `${status.dispositions[key]} ${label}`)
    return `enforcing · ${status.total} events${refusals.length ? " · " + refusals.join(" · ") : ""}${snapshot}${uncovered}`
  }
  if (status.mode !== "audit") return `${safeMode} · audit recording disabled${snapshot}`
  if (status.total === 0) return `audit only · no events in this folder${snapshot}${uncovered}`
  const counts = [
    ["would_block", "would block"],
    ["degraded", "degraded"],
    ["not_covered", "not covered"],
  ].filter(([key]) => (status.dispositions[key] ?? 0) > 0).map(([key, label]) => `${status.dispositions[key]} ${label}`)
  return `audit only · ${status.total} events${counts.length ? " · " + counts.join(" · ") : ""}${snapshot}${uncovered}`
}
