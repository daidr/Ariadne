import type { JsonRpcMessage } from './protocol'

/** How messages reach a server and come back. */
export interface Transport {
  /**
   * Begin: `onMessage` gets every message from the server, `onClose` is called once when
   * the connection ends on its own (the process exited, the stream broke).
   */
  start(
    onMessage: (message: JsonRpcMessage) => void,
    onClose: (reason: string) => void,
  ): Promise<void>
  /** Send one message; for HTTP this is a request whose answer arrives through `onMessage`. */
  send(message: JsonRpcMessage): Promise<void>
  /** After `initialize`: the protocol version both sides agreed on. */
  setProtocolVersion?(version: string): void
  close(): Promise<void>
}

/** A single message larger than this is refused, so a broken server cannot fill memory. */
export const MAX_MESSAGE_BYTES = 20 * 1024 * 1024
