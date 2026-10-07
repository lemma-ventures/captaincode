import { expect, test } from "bun:test"
import { jailCommand } from "./jail"

test("a sent turn's command becomes one quoted argument of the jail", () => {
  expect(jailCommand(["/bin/captain", "jail", "--cwd", "/w/it's", "--"], `echo 'a' "$HOME"; rm x`)).toBe(
    `'/bin/captain' 'jail' '--cwd' '/w/it'\\''s' '--' 'echo '\\''a'\\'' "$HOME"; rm x'`,
  )
})
