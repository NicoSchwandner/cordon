import type { AuditEntry, ApprovalRequest } from "./types";

type CleanupFn = () => void;

function wsUrl(path: string): string {
  if (typeof window === "undefined") return "";
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${proto}//${window.location.host}${path}`;
}

function createReconnectingWS(
  path: string,
  onMessage: (data: string) => void,
  onOpen?: () => void,
): CleanupFn {
  let ws: WebSocket | null = null;
  let delay = 1000;
  let closed = false;
  let timer: ReturnType<typeof setTimeout>;

  function connect() {
    if (closed) return;
    ws = new WebSocket(wsUrl(path));

    ws.onopen = () => {
      delay = 1000;
      onOpen?.();
    };

    ws.onmessage = (ev) => {
      onMessage(ev.data);
    };

    ws.onclose = () => {
      if (closed) return;
      timer = setTimeout(() => {
        delay = Math.min(delay * 2, 30000);
        connect();
      }, delay);
    };

    ws.onerror = () => ws?.close();
  }

  connect();

  return () => {
    closed = true;
    clearTimeout(timer);
    ws?.close();
  };
}

export function connectAuditWS(
  onEntry: (entry: AuditEntry) => void,
): CleanupFn {
  return createReconnectingWS("/ws/audit", (data) => {
    try {
      onEntry(JSON.parse(data));
    } catch {
      /* ignore parse errors */
    }
  });
}

export function connectApprovalsWS(
  onRequest: (req: ApprovalRequest) => void,
): CleanupFn {
  return createReconnectingWS("/ws/approvals", (data) => {
    try {
      onRequest(JSON.parse(data));
    } catch {
      /* ignore parse errors */
    }
  });
}

export interface TerminalWS {
  send: (data: string) => void;
  resize: (cols: number, rows: number) => void;
  onData: (cb: (data: string | Uint8Array) => void) => void;
  close: () => void;
}

export function connectTerminalWS(workspaceId: string): TerminalWS {
  const url = wsUrl(`/ws/terminal/${workspaceId}`);
  const ws = new WebSocket(url);
  ws.binaryType = "arraybuffer";
  let dataCb: ((data: string | Uint8Array) => void) | null = null;
  const pending: (string | Uint8Array)[] = [];
  let pendingResize: { cols: number; rows: number } | null = null;
  let lastResize: { cols: number; rows: number } | null = null;
  let resent = false;

  ws.onopen = () => {
    if (pendingResize) {
      ws.send(JSON.stringify({ type: "resize", ...pendingResize }));
      lastResize = pendingResize;
      pendingResize = null;
    }
  };

  ws.onmessage = (ev) => {
    const chunk =
      ev.data instanceof ArrayBuffer ? new Uint8Array(ev.data) : ev.data;

    // Re-send resize after first data arrives (tmux is now attached and ready)
    if (!resent && lastResize) {
      resent = true;
      setTimeout(() => {
        if (ws.readyState === WebSocket.OPEN && lastResize) {
          ws.send(JSON.stringify({ type: "resize", ...lastResize }));
        }
      }, 100);
    }

    if (dataCb) {
      dataCb(chunk);
    } else {
      pending.push(chunk);
    }
  };

  return {
    send: (data: string) => {
      if (ws.readyState === WebSocket.OPEN) ws.send(data);
    },
    resize: (cols: number, rows: number) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: "resize", cols, rows }));
      } else {
        pendingResize = { cols, rows };
      }
    },
    onData: (cb) => {
      dataCb = cb;
      for (const chunk of pending) cb(chunk);
      pending.length = 0;
    },
    close: () => ws.close(),
  };
}
