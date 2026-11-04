import { parseAddress, parsePairingCode } from '@ariadne/daemon/protocol'
import { definePlugin, type ContextProvider } from '@ariadne/plugin-sdk'
import { connectRemote, type RemoteShell } from '@ariadne/remote'
import { posix } from 'node:path'
import { DaemonShell, type DaemonTarget } from './shell'

export { DaemonError, DaemonShell, pinnedCertificate, type DaemonTarget } from './shell'

export interface DaemonSettings {
  address: string
  pairing: string
  path: string
}

/** The "remote daemon" context source. */
export function daemonProvider(
  makeShell: (target: DaemonTarget) => RemoteShell = (target) => new DaemonShell(target),
): ContextProvider<DaemonSettings> {
  return {
    kind: 'daemon',
    title: { 'zh-CN': '远程 daemon', en: 'Remote daemon' },
    remote: true,
    summary: '{address} {path}',
    settings: {
      type: 'object',
      properties: {
        address: {
          type: 'string',
          title: { 'zh-CN': '地址', en: 'Address' },
          description: {
            'zh-CN': '主机:端口，端口默认 7429',
            en: 'host:port; the port defaults to 7429',
          },
        },
        pairing: {
          type: 'string',
          secret: true,
          title: { 'zh-CN': '配对码', en: 'Pairing code' },
          description: {
            'zh-CN': '在远端运行 ariadne-daemon pair 得到',
            en: 'Run ariadne-daemon pair on the remote machine',
          },
        },
        path: {
          type: 'string',
          title: { 'zh-CN': '远端目录', en: 'Remote directory' },
          description: {
            'zh-CN': '绝对路径。git 仓库会在远端为每个讨论串建一个独立的 worktree',
            en: 'An absolute path. A git repository gets a separate worktree per thread on the remote machine',
          },
        },
      },
      required: ['address', 'pairing', 'path'],
    },
    provides: ['workspace'],
    async connect(settings) {
      const address = parseAddress(settings.address ?? '')
      if (!address) throw new Error('the address is not valid')
      const pairing = parsePairingCode(settings.pairing ?? '')
      if (!pairing) throw new Error('the pairing code is not valid')
      const path = settings.path?.trim()
      if (!path || !posix.isAbsolute(path)) {
        throw new Error('the directory must be an absolute path')
      }
      const shell = makeShell({ ...address, ...pairing })
      return { workspace: await connectRemote(shell, path) }
    },
  }
}

export default definePlugin({
  id: 'daemon',
  name: { 'zh-CN': '远程 daemon', en: 'Remote daemon' },
  setup(ctx) {
    return ctx.registerContextProvider(daemonProvider() as ContextProvider<never>)
  },
})
