import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { spawn } from "node:child_process";
import { existsSync } from "node:fs";

const HOLOCRON = "/Users/matt/git/mattwynne/holocron/holocron";
const REGISTER_TIMEOUT_MS = 5000;

export default function (pi: ExtensionAPI) {
  let sessionFile: string | undefined;

  const register = () => {
    if (!sessionFile || process.env.HOLOCRON_REGISTER_SESSION === "0") return false;
    if (!existsSync(sessionFile)) return false;

    const child = spawn(HOLOCRON, ["register-session", "pi", sessionFile], {
      stdio: "ignore",
      detached: true,
    });

    const timeout = setTimeout(() => child.kill(), REGISTER_TIMEOUT_MS);
    timeout.unref();
    child.on("exit", () => clearTimeout(timeout));
    child.unref();

    return true;
  };

  const registerSoon = (attempt = 0) => {
    const timeout = setTimeout(() => {
      if (!register() && attempt < 20) registerSoon(attempt + 1);
    }, 250);
    timeout.unref();
  };

  pi.on("session_start", async (_event, ctx) => {
    sessionFile = ctx.sessionManager.getSessionFile();
    registerSoon();
  });

  pi.on("agent_end", async () => {
    register();
  });

  pi.on("session_shutdown", async () => {
    register();
  });
}
