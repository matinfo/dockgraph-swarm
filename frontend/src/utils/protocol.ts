/**
 * WebSocket wire protocol version this bundle understands. Must match
 * collector.ProtocolVersion in the backend.
 */
export const PROTOCOL_VERSION = 2;

const RELOAD_KEY = 'dg_protocol_reload';

/** What to do with a message carrying a given protocol version. */
export type ProtocolAction = 'accept' | 'reload' | 'ignore';

/**
 * Decides how to handle a message whose envelope carries `version`.
 *
 * A matching version is accepted. A different one means this bundle is stale
 * (or the server is), so the page reloads once to fetch a matching bundle.
 * If a reload for that server version already happened this session and the
 * bundle still disagrees, the message is ignored rather than looping reloads
 * or rendering a payload this bundle cannot interpret.
 */
export function protocolAction(version: number, storage: Storage | undefined = safeSessionStorage()): ProtocolAction {
  if (version === PROTOCOL_VERSION) {
    storage?.removeItem(RELOAD_KEY);
    return 'accept';
  }
  if (storage?.getItem(RELOAD_KEY) === String(version)) return 'ignore';
  storage?.setItem(RELOAD_KEY, String(version));
  return 'reload';
}

function safeSessionStorage(): Storage | undefined {
  try {
    return window.sessionStorage;
  } catch {
    return undefined;
  }
}
