/** @jsxImportSource @opentui/solid */
import type { TuiPluginApi } from "@opencode-ai/plugin/tui"
import { useTerminalDimensions } from "@opentui/solid"
import { createSignal, onCleanup, Show } from "solid-js"
import { inboxClient, inboxDetails, inboxDisplay, type HeldMessage, type InboxAction } from "./inbox"

export function openInboxDialog(api: TuiPluginApi, url: string, changed?: (waiting: number) => void) {
  api.ui.dialog.replace(() => <InboxDialog api={api} url={url} changed={changed} />)
}

function InboxDialog(props: { api: TuiPluginApi; url: string; changed?: (waiting: number) => void }) {
  const client = inboxClient(props.url)
  const dimensions = useTerminalDimensions()
  const [items, setItems] = createSignal<HeldMessage[]>([])
  const [selected, setSelected] = createSignal<HeldMessage>()
  const [confirming, setConfirming] = createSignal<InboxAction>()
  const [loading, setLoading] = createSignal(false)
  const [error, setError] = createSignal("")
  const cancel = new AbortController()
  onCleanup(() => cancel.abort())
  const theme = () => props.api.theme.current
  const refresh = async (preserveError = false) => {
    if (loading()) return
    setLoading(true)
    if (!preserveError) setError("")
    try {
      const result = await client.list(cancel.signal)
      if (cancel.signal.aborted) return
      setItems(result.held)
      props.changed?.(result.held.length)
    } catch (e) {
      if (!cancel.signal.aborted) setError(String((e as Error).message))
    } finally {
      if (!cancel.signal.aborted) setLoading(false)
    }
  }
  const act = async () => {
    const item = selected()
    const action = confirming()
    if (!item || !action || loading()) return
    setLoading(true)
    setError("")
    try {
      await client.act(item.id, action, cancel.signal)
      if (cancel.signal.aborted) return
      setItems((rows) => rows.filter((row) => row.id !== item.id))
      props.changed?.(items().length)
      props.api.ui.toast({
        title: action === "release" ? "Message released" : "Message dropped",
        message: action === "release" ? "Queued for its session. Sent-turn restrictions still apply." : "The message will not run.",
        variant: "success",
        duration: 5000,
      })
      setSelected(undefined)
    } catch (e) {
      if (!cancel.signal.aborted) {
        setError(String((e as Error).message))
        setSelected(undefined)
      }
    } finally {
      if (!cancel.signal.aborted) {
        setConfirming(undefined)
        setLoading(false)
        await refresh(true)
      }
    }
  }
  void refresh()
  return (
    <box>
      <Show when={error()}>
        <text fg={theme().error} wrapMode="word">{inboxDisplay(error())}</text>
      </Show>
      <Show when={loading()} fallback={
        <Show when={selected()} fallback={
          <box>
            <Show when={!items().length}>
              <text fg={theme().textMuted}>No held messages in this folder.</text>
            </Show>
            <props.api.ui.DialogSelect<string>
              title={`Pending inbox (${items().length})`}
              placeholder="Filter messages…"
              options={[
                ...items().map((item) => ({
                  title: inboxDisplay(item.text).replace(/\s+/g, " ").slice(0, 90),
                  value: item.id,
                  category: "Held messages",
                  description: inboxDisplay(`${item.origin || item.from || "unnamed sender"} · ${item.at}`),
                  onSelect: () => { setError(""); setSelected(item) },
                })),
                { title: "Refresh", value: "refresh", category: "Controls", onSelect: () => void refresh() },
              ]}
            />
          </box>
        }>
          {(item) => (
            <box>
              <scrollbox height={Math.max(3, Math.min(18, dimensions().height - 17))}>
                <text fg={theme().text} wrapMode="word">{inboxDetails(item())}</text>
              </scrollbox>
              <text fg={theme().textMuted}>Scroll to read the full message. Release keeps sent-turn restrictions.</text>
              <Show when={confirming()} fallback={
                <props.api.ui.DialogSelect<string>
                  title="Held message"
                  skipFilter
                  options={[
                    { title: "Back to inbox", value: "back", onSelect: () => setSelected(undefined) },
                    { title: "Release…", value: "release", description: "Queue this message for its session", onSelect: () => setConfirming("release") },
                    { title: "Drop…", value: "drop", description: "Delete this message without running it", onSelect: () => setConfirming("drop") },
                  ]}
                />
              }>
                <props.api.ui.DialogSelect<string>
                  title={confirming() === "release" ? "Release this message?" : "Drop this message?"}
                  skipFilter
                  options={[
                    { title: "Cancel", value: "cancel", onSelect: () => setConfirming(undefined) },
                    { title: confirming() === "release" ? "Confirm release" : "Confirm drop", value: "confirm", onSelect: () => void act() },
                  ]}
                />
              </Show>
            </box>
          )}
        </Show>
      }>
        <text fg={theme().textMuted}>Loading inbox…</text>
      </Show>
    </box>
  )
}
