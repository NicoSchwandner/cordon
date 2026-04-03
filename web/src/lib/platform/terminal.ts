import { Terminal } from "xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebLinksAddon } from "@xterm/addon-web-links";

export interface TerminalInstance {
  terminal: Terminal;
  fit: FitAddon;
  dispose: () => void;
}

export function createTerminal(element: HTMLElement): TerminalInstance {
  const terminal = new Terminal({
    cursorBlink: true,
    fontSize: 14,
    fontFamily: "'JetBrains Mono', 'Fira Code', 'Cascadia Code', monospace",
    theme: {
      background: "#0f172a",
      foreground: "#e2e8f0",
      cursor: "#e2e8f0",
      selectionBackground: "#334155",
      black: "#0f172a",
      red: "#ef4444",
      green: "#22c55e",
      yellow: "#f59e0b",
      blue: "#3b82f6",
      magenta: "#a855f7",
      cyan: "#06b6d4",
      white: "#e2e8f0",
    },
  });

  const fit = new FitAddon();
  terminal.loadAddon(fit);
  terminal.loadAddon(new WebLinksAddon());

  terminal.open(element);
  fit.fit();

  const resizeObserver = new ResizeObserver(() => {
    try {
      fit.fit();
    } catch {
      /* element might be detached */
    }
  });
  resizeObserver.observe(element);

  return {
    terminal,
    fit,
    dispose: () => {
      resizeObserver.disconnect();
      terminal.dispose();
    },
  };
}
